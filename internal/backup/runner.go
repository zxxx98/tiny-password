package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

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
}

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

// RunInput describes one backup execution.
type RunInput struct {
	Target     Target
	Trigger    Trigger
	Passphrase string
	// LocalDir is the publish directory for TargetLocal.
	LocalDir string
}

// RunResult reports a delivered archive.
type RunResult struct {
	RunID     string
	BackupID  string
	Path      string
	SizeBytes int64
	SHA256    string
}

// Run executes one backup: consistent snapshot → manifest → encrypted
// archive → full verification → atomic publish. Manual and scheduled runs
// share the instance mutex; an overlapping run returns ErrBusy without
// recording anything. Failures record a failed run row and never publish.
func (r *Runner) Run(ctx context.Context, input RunInput) (*RunResult, error) {
	if input.Passphrase == "" || hasLineBreak(input.Passphrase) {
		return nil, r.recordFailure("", input, ErrPassphraseInvalid)
	}
	switch input.Trigger {
	case TriggerManual, TriggerScheduled:
	default:
		return nil, fmt.Errorf("backup: invalid trigger %q", input.Trigger)
	}

	select {
	case instanceMutex <- struct{}{}:
		defer func() { <-instanceMutex }()
	default:
		return nil, ErrBusy
	}

	runID := ident.NewUUIDv7()
	if err := r.insertRunningRow(runID, input); err != nil {
		return nil, err
	}
	result, err := r.execute(ctx, runID, input)
	if err != nil {
		r.failRun(runID, errorCode(err))
		return nil, err
	}
	r.succeedRun(runID, result)
	return result, nil
}

func (r *Runner) execute(ctx context.Context, runID string, input RunInput) (*RunResult, error) {
	if input.Target != TargetLocal {
		return nil, fmt.Errorf("backup: unsupported target %q", input.Target)
	}
	if input.LocalDir == "" {
		return nil, fmt.Errorf("%w: local dir missing", ErrPublish)
	}

	// Staging lives inside the restricted work dir; everything is dropped
	// no matter how the run ends (D11).
	staging, err := os.MkdirTemp(r.opts.WorkDir, "run-")
	if err != nil {
		return nil, fmt.Errorf("%w: staging dir", ErrInternal)
	}
	defer os.RemoveAll(staging)
	if err := os.Chmod(staging, 0o700); err != nil {
		return nil, fmt.Errorf("%w: staging dir", ErrInternal)
	}
	for _, sub := range []string{filepath.Dir(SnapshotPath), filepath.Dir(MasterKeyPath)} {
		if err := os.MkdirAll(filepath.Join(staging, sub), 0o700); err != nil {
			return nil, fmt.Errorf("%w: staging layout", ErrInternal)
		}
	}

	// 1. Consistent snapshot via the Online Backup API — never a raw copy
	// of the live WAL files, and no long-held write transaction.
	snapshotPath := filepath.Join(staging, SnapshotPath)
	if err := r.opts.DB.Snapshot(ctx, snapshotPath); err != nil {
		return nil, classifyStep(err, CodeSnapshotFailed)
	}
	info, err := os.Stat(snapshotPath)
	if err != nil {
		return nil, classifyStep(err, CodeSnapshotFailed)
	}

	// 2. Space gate: the archive, its verification extract, and slack must
	// fit in the work dir and the target dir.
	need := info.Size()*3 + 16<<20
	if err := r.spaceCheck(r.opts.WorkDir, need); err != nil {
		return nil, classifyStep(err, CodeSpace)
	}
	if err := r.spaceCheck(input.LocalDir, need); err != nil {
		return nil, classifyStep(err, CodeSpace)
	}

	// 3. Master key copy (mode 0600, inside the archive only).
	if err := os.WriteFile(filepath.Join(staging, MasterKeyPath), r.opts.MasterKeyRaw, 0o600); err != nil {
		return nil, classifyStep(err, CodeInternal)
	}

	// 4. Manifest with schema version, ids, and per-file digests.
	schemaVersion, err := sqlite.SchemaVersion(r.opts.DB.DB)
	if err != nil {
		return nil, classifyStep(err, CodeInternal)
	}
	instanceID, err := EnsureInstanceID(r.opts.DB.DB)
	if err != nil {
		return nil, classifyStep(err, CodeInternal)
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
			return nil, classifyStep(err, CodeInternal)
		}
		manifest.Files = append(manifest.Files, entry)
	}
	if err := writeManifestFile(staging, &manifest); err != nil {
		return nil, classifyStep(err, CodeInternal)
	}

	// 5. Encrypted archive (AES-256, encrypted file names, passphrase via
	// stdin only).
	archivePath, err := archive.Create(ctx, archive.CreateOptions{
		SourceDir:  staging,
		WorkDir:    r.opts.WorkDir,
		Passphrase: input.Passphrase,
		Timeout:    r.opts.Timeout,
	})
	if err != nil {
		return nil, classifyStep(err, CodeArchiveFailed)
	}
	defer os.RemoveAll(filepath.Dir(archivePath))

	// 6. Fault-injection point (tests); then full verification: re-open the
	// archive, check the extracted set against the manifest whitelist, and
	// re-verify the snapshot integrity.
	if r.opts.Hooks.AfterArchiveCreated != nil {
		if err := r.opts.Hooks.AfterArchiveCreated(ctx, archivePath); err != nil {
			return nil, classifyStep(err, CodeVerifyFailed)
		}
	}
	if err := r.verifyArchive(ctx, archivePath, input.Passphrase, &manifest); err != nil {
		return nil, err
	}

	// 7. Atomic publish on the target filesystem.
	store := LocalStore{Dir: input.LocalDir}
	finalPath, size, sha, err := store.Publish(archivePath, backupID+".7z")
	if err != nil {
		return nil, classifyStep(err, CodePublishFailed)
	}
	return &RunResult{
		RunID:     runID,
		BackupID:  backupID,
		Path:      finalPath,
		SizeBytes: size,
		SHA256:    sha,
	}, nil
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
