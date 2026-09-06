package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/tiny-password/tiny-password/internal/audit"
	"github.com/tiny-password/tiny-password/internal/platform/archive"
	"github.com/tiny-password/tiny-password/internal/platform/ident"
	"github.com/tiny-password/tiny-password/internal/platform/sqlite"
)

// Target names a backup destination; the values match the backup_runs
// CHECK constraint. Targets execute and report independently (design §11.2).
type Target string

const (
	TargetLocal Target = "local"
	TargetR2    Target = "r2"
)

// Trigger distinguishes manual runs from scheduled ones.
type Trigger string

const (
	TriggerManual    Trigger = "manual"
	TriggerScheduled Trigger = "scheduled"
)

// Stable error codes persisted in backup_runs.error_code. They are the only
// failure details that ever leave the process (T25 keeps reports free of
// secrets and filesystem paths).
const (
	CodePassphraseInvalid = "BACKUP_PASSPHRASE_INVALID"
	CodeSnapshotFailed    = "BACKUP_SNAPSHOT_FAILED"
	CodeSpace             = "BACKUP_INSUFFICIENT_SPACE"
	CodeArchiveFailed     = "BACKUP_ARCHIVE_FAILED"
	CodeVerifyFailed      = "BACKUP_VERIFY_FAILED"
	CodePublishFailed     = "BACKUP_PUBLISH_FAILED"
	CodeCanceled          = "BACKUP_CANCELED"
	CodeInternal          = "BACKUP_INTERNAL"
)

var (
	// ErrBusy reports the instance backup mutex is held (manual and
	// scheduled runs share it).
	ErrBusy = errors.New("backup: another backup is running")
	// ErrPassphraseInvalid reports an empty passphrase or one containing
	// line breaks (which the 7z stdin contract forbids).
	ErrPassphraseInvalid = errors.New("backup: passphrase invalid")
	// ErrSpace reports an insufficient-space refusal.
	ErrSpace = errors.New("backup: insufficient space")
	// errSpaceUnsupported backs the windows stub of freeBytes.
	errSpaceUnsupported = errors.New("backup: free space check unsupported")
)

// DefaultMaxArchiveBytes caps an instance archive unless configured
// otherwise (T01: instance archive limits are explicit configuration).
const DefaultMaxArchiveBytes int64 = 1 << 30

// defaultTimeout bounds each 7zz invocation for instance archives (larger
// than the personal 120 s budget; a full snapshot may be hundreds of MiB).
const defaultTimeout = 10 * time.Minute

// Hooks inject fault points for tests. Production leaves them zero.
type Hooks struct {
	// AfterArchiveCreated runs after the staged archive exists but before
	// verification.
	AfterArchiveCreated func(ctx context.Context, archivePath string) error
	// SpaceCheck replaces the default free-space gate.
	SpaceCheck func(dir string, need int64) error
}

// Options configures the runner. WorkDir should be a restricted tmpfs mount
// in deployment (D11) — the master key copy and snapshot pass through it.
type Options struct {
	DB              *sqlite.DB
	WorkDir         string
	AppVersion      string
	MasterKeyRaw    []byte // exactly 32 bytes; copied into every archive
	MaxArchiveBytes int64
	Timeout         time.Duration
	Now             func() time.Time
	Logger          *slog.Logger
	Hooks           Hooks
	// Scheduled delivery configuration (T23): the passphrase comes from a
	// secret file, target settings from operator configuration. They never
	// live in the database.
	ScheduledPassphrase string
	ScheduledLocalDir   string
	// R2 resolves the R2 delivery at run time (T27): the non-sensitive
	// endpoint/bucket/prefix come from the admin settings store and the
	// credentials from secret files, so configuration changes take effect
	// on the next run without a restart. A nil result (with a nil error)
	// means the target is not configured.
	R2 R2Resolver
	// Audit records retention deletions (T24); optional.
	Audit *audit.Service
}

// R2Resolver returns the current R2 delivery configuration, or nil when
// the target is not configured. An error reports a resolution failure
// (settings store, credential file); scheduled runs fail closed on it.
type R2Resolver func(ctx context.Context) (*R2Delivery, error)

// Runner produces whole-instance backups. The run mutex is process-wide
// (single-container deployment): every Runner instance in this process
// shares it, so manual and scheduled runs can never overlap.
type Runner struct {
	opts Options
}

// instanceMutex serializes backup runs for the whole process (design
// §11.3 step 1; T23: manual and scheduled runs share it).
var instanceMutex = make(chan struct{}, 1)

// NewRunner validates the configuration.
func NewRunner(options Options) (*Runner, error) {
	if options.DB == nil {
		return nil, errors.New("backup: db is required")
	}
	if options.WorkDir == "" {
		return nil, errors.New("backup: work dir is required")
	}
	if len(options.MasterKeyRaw) != 32 {
		return nil, errors.New("backup: master key must be exactly 32 bytes")
	}
	if err := ensureDir(options.WorkDir); err != nil {
		return nil, err
	}
	if options.MaxArchiveBytes <= 0 {
		options.MaxArchiveBytes = DefaultMaxArchiveBytes
	}
	if options.Timeout <= 0 {
		options.Timeout = defaultTimeout
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	return &Runner{opts: options}, nil
}

// InstanceLimits returns the archive limits used for instance backups with
// the given archive size cap.
func InstanceLimits(maxArchiveBytes int64) archive.Limits {
	if maxArchiveBytes <= 0 {
		maxArchiveBytes = DefaultMaxArchiveBytes
	}
	return archive.Limits{
		MaxArchiveBytes: maxArchiveBytes,
		MaxExtractBytes: 2 * maxArchiveBytes,
		MaxFiles:        16,
		Timeout:         defaultTimeout,
	}
}

func (r *Runner) limits() archive.Limits { return InstanceLimits(r.opts.MaxArchiveBytes) }

// RunInput describes one backup execution across independent targets
// (design §11.2: each target executes and reports on its own).
type RunInput struct {
	Targets    []Target
	Trigger    Trigger
	Passphrase string
	// LocalDir is the publish directory for TargetLocal.
	LocalDir string
	// R2 is the delivery configuration for TargetR2.
	R2 *R2Delivery
}

// TargetResult reports one target's delivery outcome.
type TargetResult struct {
	Target    Target
	Path      string // local file path or object key
	SizeBytes int64
	SHA256    string
	Err       error
}

// RunSummary reports one Run across all requested targets.
type RunSummary struct {
	BackupID string
	Results  []TargetResult
}

// For returns the result of one target.
func (s *RunSummary) For(t Target) *TargetResult {
	for i := range s.Results {
		if s.Results[i].Target == t {
			return &s.Results[i]
		}
	}
	return nil
}

// Err joins every delivery failure (nil when all targets succeeded).
func (s *RunSummary) Err() error {
	var errs []error
	for _, res := range s.Results {
		if res.Err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", res.Target, res.Err))
		}
	}
	return errors.Join(errs...)
}

// ValidateRunInput performs the pre-flight checks shared by Run and
// RunAsync (T27: the manual-run endpoint refuses misconfiguration with a
// synchronous error before answering 202).
func (r *Runner) ValidateRunInput(input RunInput) error {
	switch input.Trigger {
	case TriggerManual, TriggerScheduled:
	default:
		return fmt.Errorf("backup: invalid trigger %q", input.Trigger)
	}
	if len(input.Targets) == 0 {
		return errors.New("backup: no targets requested")
	}
	for _, t := range dedupTargets(input.Targets) {
		switch t {
		case TargetLocal:
			if input.LocalDir == "" {
				return fmt.Errorf("backup: local dir missing for local target")
			}
		case TargetR2:
			if input.R2 == nil || input.R2.Client == nil {
				return fmt.Errorf("backup: r2 delivery is not configured")
			}
		default:
			return fmt.Errorf("backup: unsupported target %q", t)
		}
	}
	if input.Passphrase == "" || hasLineBreak(input.Passphrase) {
		return ErrPassphraseInvalid
	}
	return nil
}

// RunAsync starts a backup in the background when the process mutex is
// free (T27 manual trigger). It reports started=false when a run is
// already in progress (HTTP 409 BACKUP_BUSY) and a validation error for
// misconfiguration.
func (r *Runner) RunAsync(ctx context.Context, input RunInput) (bool, error) {
	input.Targets = dedupTargets(input.Targets)
	if err := r.ValidateRunInput(input); err != nil {
		return false, err
	}
	select {
	case instanceMutex <- struct{}{}:
	default:
		return false, nil
	}
	// The run outlives the HTTP request: detach the cancellation signal
	// (values like the request id are preserved) so finishing the response
	// cannot kill the backup.
	detached := context.WithoutCancel(ctx)
	go func() {
		defer func() { <-instanceMutex }()
		if _, err := r.run(detached, input); err != nil {
			r.opts.Logger.Error("async backup run failed", "error", err.Error())
		}
	}()
	return true, nil
}

// Run executes one backup: consistent snapshot → manifest → encrypted
// archive → full verification → per-target delivery. Manual and scheduled
// runs share the process mutex; an overlapping run returns ErrBusy without
// recording anything. An archive-phase failure fails every requested
// target's row; a delivery failure never rewrites another target's result.
func (r *Runner) Run(ctx context.Context, input RunInput) (*RunSummary, error) {
	input.Targets = dedupTargets(input.Targets)
	switch input.Trigger {
	case TriggerManual, TriggerScheduled:
	default:
		return nil, fmt.Errorf("backup: invalid trigger %q", input.Trigger)
	}
	if len(input.Targets) == 0 {
		return nil, errors.New("backup: no targets requested")
	}
	for _, t := range input.Targets {
		switch t {
		case TargetLocal:
			if input.LocalDir == "" {
				return nil, fmt.Errorf("backup: local dir missing for local target")
			}
		case TargetR2:
			if input.R2 == nil || input.R2.Client == nil {
				return nil, fmt.Errorf("backup: r2 delivery is not configured")
			}
		default:
			return nil, fmt.Errorf("backup: unsupported target %q", t)
		}
	}
	if input.Passphrase == "" || hasLineBreak(input.Passphrase) {
		return nil, r.recordFailures(input, ErrPassphraseInvalid)
	}

	select {
	case instanceMutex <- struct{}{}:
		defer func() { <-instanceMutex }()
	default:
		return nil, ErrBusy
	}
	return r.run(ctx, input)
}

// run is the lock-free body of Run (the caller holds instanceMutex).
func (r *Runner) run(ctx context.Context, input RunInput) (*RunSummary, error) {
	// One lifecycle row per target; they share the archive phase and then
	// report independently.
	runIDs := map[Target]string{}
	for _, t := range dedupTargets(input.Targets) {
		runID := ident.NewUUIDv7()
		if err := r.insertRunningRow(runID, t, input.Trigger); err != nil {
			return nil, err
		}
		runIDs[t] = runID
	}

	staged, manifest, err := r.buildAndVerifyArchive(ctx, input)
	if err != nil {
		code := errorCode(err)
		for _, t := range dedupTargets(input.Targets) {
			r.failRun(runIDs[t], code)
		}
		return nil, err
	}
	defer staged.cleanup()

	summary := &RunSummary{BackupID: manifest.BackupID}
	for _, t := range input.Targets {
		summary.Results = append(summary.Results, r.deliverTarget(ctx, runIDs[t], t, input, staged, manifest))
	}
	return summary, summary.Err()
}

// stagedArchive is the verified archive and its digest; cleanup drops the
// whole staging area.
type stagedArchive struct {
	path      string
	sizeBytes int64
	sha256    string
	cleanup   func()
}

// deliverTarget publishes the verified archive on one target and closes
// that target's run row.
func (r *Runner) deliverTarget(ctx context.Context, runID string, target Target, input RunInput, staged *stagedArchive, manifest *Manifest) TargetResult {
	result := TargetResult{Target: target, SizeBytes: staged.sizeBytes, SHA256: staged.sha256}
	var path string
	var err error
	switch target {
	case TargetLocal:
		path, result.SizeBytes, result.SHA256, err = LocalStore{Dir: input.LocalDir}.Publish(staged.path, runID+".7z")
	case TargetR2:
		path, err = input.R2.Deliver(ctx, staged.path, staged.sizeBytes, staged.sha256, runID)
	default:
		err = fmt.Errorf("backup: unsupported target %q", target)
	}
	if err != nil {
		r.failRun(runID, errorCode(err))
		result.Err = err
		return result
	}
	result.Path = path
	r.succeedRun(runID, result.SizeBytes, result.SHA256)
	// Retention runs only after this target gained a verified backup
	// (T24); failures never affect the recorded success.
	r.ApplyRetention(ctx, target, input)
	return result
}

// buildAndVerifyArchive runs the shared phases 1–6 (snapshot, manifest,
// archive, verification) and computes the staged archive's digest.
func (r *Runner) buildAndVerifyArchive(ctx context.Context, input RunInput) (*stagedArchive, *Manifest, error) {
	// Staging lives inside the restricted work dir; everything is dropped
	// no matter how the run ends (D11). cleanup also removes the archive
	// workspace once archive.Create has produced one. Failure paths clean
	// via the deferred call; on success the ownership moves to the
	// caller's stagedArchive.cleanup.
	staging, err := os.MkdirTemp(r.opts.WorkDir, "run-")
	if err != nil {
		return nil, nil, fmt.Errorf("%w: staging dir", ErrInternal)
	}
	archiveWS := ""
	cleanup := func() {
		os.RemoveAll(staging)
		if archiveWS != "" {
			os.RemoveAll(archiveWS)
		}
	}
	handedOff := false
	defer func() {
		if !handedOff {
			cleanup()
		}
	}()
	if err := os.Chmod(staging, 0o700); err != nil {
		return nil, nil, fmt.Errorf("%w: staging dir", ErrInternal)
	}
	for _, sub := range []string{filepath.Dir(SnapshotPath), filepath.Dir(MasterKeyPath)} {
		if err := os.MkdirAll(filepath.Join(staging, sub), 0o700); err != nil {
			return nil, nil, fmt.Errorf("%w: staging layout", ErrInternal)
		}
	}

	// 1. Consistent snapshot via the Online Backup API — never a raw copy
	// of the live WAL files, and no long-held write transaction.
	snapshotPath := filepath.Join(staging, SnapshotPath)
	if err := r.opts.DB.Snapshot(ctx, snapshotPath); err != nil {
		return nil, nil, classifyStep(err, CodeSnapshotFailed)
	}
	info, err := os.Stat(snapshotPath)
	if err != nil {
		return nil, nil, classifyStep(err, CodeSnapshotFailed)
	}

	// 2. Space gate: the archive, its verification extract, and slack must
	// fit in the work dir and (for local delivery) the target dir.
	need := info.Size()*3 + 16<<20
	if err := r.spaceCheck(r.opts.WorkDir, need); err != nil {
		return nil, nil, classifyStep(err, CodeSpace)
	}
	for _, t := range dedupTargets(input.Targets) {
		if t == TargetLocal {
			if err := r.spaceCheck(input.LocalDir, need); err != nil {
				return nil, nil, classifyStep(err, CodeSpace)
			}
		}
	}

	// 3. Master key copy (mode 0600, inside the archive only).
	if err := os.WriteFile(filepath.Join(staging, MasterKeyPath), r.opts.MasterKeyRaw, 0o600); err != nil {
		return nil, nil, classifyStep(err, CodeInternal)
	}

	// 4. Manifest with schema version, ids, and per-file digests.
	schemaVersion, err := sqlite.SchemaVersion(r.opts.DB.DB)
	if err != nil {
		return nil, nil, classifyStep(err, CodeInternal)
	}
	instanceID, err := EnsureInstanceID(r.opts.DB.DB)
	if err != nil {
		return nil, nil, classifyStep(err, CodeInternal)
	}
	backupID := ident.NewUUIDv7()
	manifest := Manifest{
		FormatVersion: FormatVersion,
		AppVersion:    r.opts.AppVersion,
		SchemaVersion: schemaVersion,
		BackupID:      backupID,
		CreatedAt:     r.opts.Now().UTC().Format(time.RFC3339Nano),
		InstanceID:    instanceID,
		Arch:          goArchLabel(),
	}
	for _, p := range []string{SnapshotPath, MasterKeyPath} {
		entry, err := fileDigest(staging, p)
		if err != nil {
			return nil, nil, classifyStep(err, CodeInternal)
		}
		manifest.Files = append(manifest.Files, entry)
	}
	if err := writeManifestFile(staging, &manifest); err != nil {
		return nil, nil, classifyStep(err, CodeInternal)
	}

	// 5. Encrypted archive (AES-256, encrypted file names, passphrase via
	// stdin only). The archive lives in its own workspace under WorkDir;
	// from here on cleanup must remove that workspace too.
	archivePath, err := archive.Create(ctx, archive.CreateOptions{
		SourceDir:  staging,
		WorkDir:    r.opts.WorkDir,
		Passphrase: input.Passphrase,
		Timeout:    r.opts.Timeout,
	})
	if err != nil {
		return nil, nil, classifyStep(err, CodeArchiveFailed)
	}
	archiveWS = filepath.Dir(archivePath)

	// 6. Fault-injection point (tests); then full verification: re-open the
	// archive, check the extracted set against the manifest whitelist, and
	// re-verify the snapshot integrity.
	if r.opts.Hooks.AfterArchiveCreated != nil {
		if err := r.opts.Hooks.AfterArchiveCreated(ctx, archivePath); err != nil {
			return nil, nil, classifyStep(err, CodeVerifyFailed)
		}
	}
	if err := r.verifyArchive(ctx, archivePath, input.Passphrase, &manifest); err != nil {
		return nil, nil, err
	}

	// The verified archive leaves its own workspace: responsibility for
	// both scratch areas moves to the caller's stagedArchive.cleanup.
	size, sha, err := fileHash(archivePath)
	if err != nil {
		return nil, nil, classifyStep(err, CodeInternal)
	}
	handedOff = true // the deferred cleanup must not fire on success
	staged := &stagedArchive{path: archivePath, sizeBytes: size, sha256: sha, cleanup: cleanup}
	return staged, &manifest, nil
}

func dedupTargets(targets []Target) []Target {
	seen := map[Target]bool{}
	var out []Target
	for _, t := range targets {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// fileHash streams a file's size and SHA-256.
func fileHash(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	sum := sha256.New()
	size, err := io.Copy(sum, f)
	if err != nil {
		return 0, "", err
	}
	return size, hex.EncodeToString(sum.Sum(nil)), nil
}

// verifyArchive re-opens the archive under the instance limits and checks
// every whitelisted file's digest plus the snapshot's integrity.
func (r *Runner) verifyArchive(ctx context.Context, archivePath, passphrase string, manifest *Manifest) error {
	dest, entries, err := archive.ExtractLimited(ctx, archivePath, passphrase, r.opts.WorkDir, r.limits())
	if err != nil {
		return classifyStep(err, CodeVerifyFailed)
	}
	defer os.RemoveAll(dest)

	// The extracted file set must be exactly the whitelist: the listed
	// files plus the manifest itself, nothing more.
	want := map[string]bool{ManifestPath: true}
	for _, f := range manifest.Files {
		want[f.Path] = true
	}
	got := map[string]bool{}
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		if !want[e.Path] {
			return fmt.Errorf("%w: unexpected archive entry", ErrVerify)
		}
		got[e.Path] = true
	}
	for path := range want {
		if !got[path] {
			return fmt.Errorf("%w: archive entry %s missing", ErrVerify, path)
		}
	}

	extracted, err := readManifestFile(dest)
	if err != nil {
		return classifyStep(err, CodeVerifyFailed)
	}
	if extracted.BackupID != manifest.BackupID {
		return fmt.Errorf("%w: manifest backup id mismatch", ErrVerify)
	}
	for _, f := range extracted.Files {
		entry, err := fileDigest(dest, f.Path)
		if err != nil {
			return classifyStep(err, CodeVerifyFailed)
		}
		if entry.SHA256 != f.SHA256 || entry.Size != f.Size {
			return fmt.Errorf("%w: digest mismatch for %s", ErrVerify, f.Path)
		}
	}
	integrity, err := sqlite.VerifySnapshot(filepath.Join(dest, SnapshotPath))
	if err != nil || integrity != "ok" {
		return fmt.Errorf("%w: snapshot integrity check failed", ErrVerify)
	}
	return nil
}

// spaceCheck gates the run on free space; the hook replaces the statfs
// default in tests. The target dir may not exist yet, so the default check
// creates it first (0700) — matching LocalStore.Publish.
func (r *Runner) spaceCheck(dir string, need int64) error {
	if r.opts.Hooks.SpaceCheck != nil {
		return r.opts.Hooks.SpaceCheck(dir, need)
	}
	if err := ensureDir(dir); err != nil {
		return fmt.Errorf("%w: cannot prepare target dir", ErrSpace)
	}
	free, err := freeBytes(dir)
	if err != nil {
		if errors.Is(err, errSpaceUnsupported) {
			return errSpaceUnsupported
		}
		return fmt.Errorf("%w: cannot stat free space", ErrSpace)
	}
	if free < need {
		return ErrSpace
	}
	return nil
}

// sentinel errors used for classification inside execute.
var (
	ErrInternal = errors.New("backup: internal failure")
	ErrVerify   = errors.New("backup: verification failed")
)

// classifyStep wraps err for the caller while mapping it to a stable code.
// Multiple %w verbs keep both the sentinel and the original error
// (e.g. context.Canceled) unwrap-able.
func classifyStep(err error, code string) error {
	if err == nil {
		return nil
	}
	switch code {
	case CodeSnapshotFailed, CodeArchiveFailed, CodeVerifyFailed:
		if canceledOrError(err) {
			return fmt.Errorf("%w: %w", ErrCanceled, err)
		}
	}
	switch code {
	case CodeInternal:
		return fmt.Errorf("%w: %w", ErrInternal, err)
	case CodeSnapshotFailed:
		return fmt.Errorf("%w: %w", ErrSnapshot, err)
	case CodeArchiveFailed:
		return fmt.Errorf("%w: %w", ErrArchive, err)
	case CodeVerifyFailed:
		return fmt.Errorf("%w: %w", ErrVerify, err)
	case CodePublishFailed:
		return fmt.Errorf("%w: %w", ErrPublish, err)
	case CodeSpace:
		return fmt.Errorf("%w: %w", ErrSpace, err)
	}
	return err
}

var (
	ErrCanceled = errors.New("backup: canceled")
	ErrSnapshot = errors.New("backup: snapshot failed")
	ErrArchive  = errors.New("backup: archive failed")
)

// errorCode maps a failure to its stable backup_runs code.
func errorCode(err error) string {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), errors.Is(err, ErrCanceled):
		return CodeCanceled
	case errors.Is(err, ErrPassphraseInvalid):
		return CodePassphraseInvalid
	case errors.Is(err, ErrSpace):
		return CodeSpace
	case errors.Is(err, ErrSnapshot):
		return CodeSnapshotFailed
	case errors.Is(err, ErrArchive):
		return CodeArchiveFailed
	case errors.Is(err, ErrVerify):
		return CodeVerifyFailed
	case errors.Is(err, ErrPublish):
		return CodePublishFailed
	case errors.Is(err, ErrUploadVerify):
		return CodeUploadVerifyFailed
	case errors.Is(err, ErrUpload):
		return CodeUploadFailed
	default:
		return CodeInternal
	}
}

func hasLineBreak(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' || s[i] == '\r' {
			return true
		}
	}
	return false
}
