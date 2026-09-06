package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/tiny-password/tiny-password/internal/platform/archive"
	"github.com/tiny-password/tiny-password/internal/platform/crypto"
	"github.com/tiny-password/tiny-password/internal/platform/sqlite"
	"github.com/tiny-password/tiny-password/internal/vault"
)

// Stable restore failure codes (display-safe; stage + code are the whole
// operator-facing report — design §11.5, T25).
const (
	RestoreCodeDataDirLocked   = "RESTORE_DATA_DIR_LOCKED"
	RestoreCodePassphrase      = "RESTORE_PASSPHRASE_INVALID"
	RestoreCodeArchive         = "RESTORE_ARCHIVE_INVALID"
	RestoreCodeFormat          = "RESTORE_FORMAT_UNSUPPORTED"
	RestoreCodeSchemaTooNew    = "RESTORE_SCHEMA_TOO_NEW"
	RestoreCodeSpace           = "RESTORE_INSUFFICIENT_SPACE"
	RestoreCodeTargetKey       = "RESTORE_TARGET_KEY_INVALID"
	RestoreCodeSnapshot        = "RESTORE_PRESNAPSHOT_FAILED"
	RestoreCodeRekey           = "RESTORE_REKEY_FAILED"
	RestoreCodeMigrate         = "RESTORE_MIGRATE_FAILED"
	RestoreCodeVerify          = "RESTORE_VERIFY_FAILED"
	RestoreCodeSwitch          = "RESTORE_SWITCH_FAILED"
	RestoreCodeRolledBack      = "RESTORE_ROLLED_BACK"
	RestoreCodeCanceled        = "RESTORE_CANCELED"
	RestoreCodeInternal        = "RESTORE_INTERNAL"
	RestoreCodeStateUnreadable = "RESTORE_STATE_UNREADABLE"
)

// RestoreError carries the failure stage and stable code.
type RestoreError struct {
	Stage string
	Code  string
	err   error
}

func (e *RestoreError) Error() string { return e.Code + " at " + e.Stage }
func (e *RestoreError) Unwrap() error { return e.err }

func restoreErr(stage, code string, err error) *RestoreError {
	return &RestoreError{Stage: stage, Code: code, err: err}
}

// RestoreHooks are fault-injection points for the staged tests; production
// leaves them zero.
type RestoreHooks struct {
	AfterSnapshot func() error
	AfterRekey    func() error
	AfterMigrate  func() error
	// AfterSwitch runs after the atomic rename but before the post-switch
	// check; returning ErrRestoreCrash emulates a process death at exactly
	// that breakpoint.
	AfterSwitch func() error
	SpaceCheck  func(dir string, need int64) error
}

// RestoreOptions configures one offline restore.
type RestoreOptions struct {
	DataDir     string // target instance data directory
	WorkDir     string // restricted tmpfs for extraction (source key passes through)
	ArchivePath string
	Passphrase  string
	// TargetKeyRaw is the NEW master key mounted on this instance (D03:
	// read-only secret, never written).
	TargetKeyRaw []byte
	// Migrations is the embedded migration set used to bring the candidate
	// to the current schema and to learn the maximum known version.
	Migrations fs.FS
	AppVersion string
	Now        func() time.Time
	Logger     *slog.Logger
	Hooks      RestoreHooks
}

// RestoreResult reports a completed restore.
type RestoreResult struct {
	BackupID        string
	Items           int
	Versions        int
	AuthCleared     int64
	PresnapshotPath string // kept as the recoverable pre-restore evidence
	Resumed         bool
}

// Restore executes the offline instance restore: verify → pre-snapshot →
// candidate build (rekey + migrate + verify) → atomic switch → post-check.
// The caller must have stopped the HTTP service; the data-dir lock makes
// concurrent operation impossible.
func Restore(ctx context.Context, options RestoreOptions) (*RestoreResult, error) {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if len(options.TargetKeyRaw) != 32 {
		return nil, restoreErr("start", RestoreCodeTargetKey, errors.New("target key must be 32 bytes"))
	}
	targetKey, err := crypto.NewMasterKey(options.TargetKeyRaw)
	if err != nil {
		return nil, restoreErr("start", RestoreCodeTargetKey, err)
	}
	if options.Passphrase == "" || hasLineBreak(options.Passphrase) {
		return nil, restoreErr("start", RestoreCodePassphrase, errors.New("passphrase empty"))
	}

	lock, err := AcquireDataDirLock(options.DataDir)
	if err != nil {
		return nil, restoreErr("start", RestoreCodeDataDirLocked, err)
	}
	defer lock.Release()

	// Breakpoint recovery: a state file from a crashed earlier attempt
	// selects the resume path.
	if state, err := ReadRecoveryState(options.DataDir); err != nil {
		return nil, restoreErr("start", RestoreCodeStateUnreadable, err)
	} else if state != nil {
		return resumeRestore(ctx, options, targetKey, state)
	}
	return runRestore(ctx, options, targetKey, nil)
}

func runRestore(ctx context.Context, options RestoreOptions, targetKey *crypto.MasterKey, existing *RecoveryState) (*RestoreResult, error) {
	if err := ensureDir(options.WorkDir); err != nil {
		return nil, restoreErr("start", RestoreCodeInternal, err)
	}
	// 1. Extract and verify the archive under the instance limits.
	extractDir, entries, err := archive.ExtractLimited(ctx, options.ArchivePath, options.Passphrase, options.WorkDir, InstanceLimits(0))
	if err != nil {
		return nil, restoreErr("extract", RestoreCodeArchive, err)
	}
	defer os.RemoveAll(extractDir)
	if err := verifyRestoreTree(extractDir, entries); err != nil {
		return nil, restoreErr("extract", RestoreCodeArchive, err)
	}
	manifest, err := readManifestFile(extractDir)
	if err != nil {
		return nil, restoreErr("extract", RestoreCodeArchive, err)
	}
	if manifest.FormatVersion != FormatVersion {
		return nil, restoreErr("extract", RestoreCodeFormat, fmt.Errorf("format version %d", manifest.FormatVersion))
	}
	// Schema compatibility: older candidates are migrated; newer ones are
	// refused (restore can never downgrade a database).
	maxKnown, err := maxMigrationVersion(options.Migrations)
	if err != nil {
		return nil, restoreErr("extract", RestoreCodeInternal, err)
	}
	if manifest.SchemaVersion > maxKnown {
		return nil, restoreErr("extract", RestoreCodeSchemaTooNew, fmt.Errorf("archive schema %d > known %d", manifest.SchemaVersion, maxKnown))
	}
	sourceKeyRaw, err := os.ReadFile(filepath.Join(extractDir, MasterKeyPath))
	if err != nil {
		return nil, restoreErr("extract", RestoreCodeArchive, err)
	}
	sourceKey, err := crypto.NewMasterKey(sourceKeyRaw)
	if err != nil {
		return nil, restoreErr("extract", RestoreCodeArchive, err)
	}
	snapshotInfo, err := os.Stat(filepath.Join(extractDir, SnapshotPath))
	if err != nil {
		return nil, restoreErr("extract", RestoreCodeArchive, err)
	}

	// 2. Space gate for candidate + pre-snapshot + slack on the data dir.
	if err := restoreSpaceCheck(options, snapshotInfo.Size()*3+16<<20); err != nil {
		return nil, restoreErr("space", RestoreCodeSpace, err)
	}

	state := existing
	if state == nil {
		state = &RecoveryState{
			Stage:       StageSnapshot,
			BackupID:    manifest.BackupID,
			ArchivePath: options.ArchivePath,
			StartedAt:   options.Now().UTC().Format(time.RFC3339Nano),
		}
	}

	// 3. Recoverable pre-snapshot of an existing target; an empty target is
	// recorded as empty instead of assuming an old database exists.
	livePath := filepath.Join(options.DataDir, "tiny-password.db")
	if _, err := os.Stat(livePath); errors.Is(err, os.ErrNotExist) {
		state.EmptyTarget = true
	} else if err != nil {
		return nil, restoreErr("presnapshot", RestoreCodeSnapshot, err)
	} else if state.PresnapshotPath == "" {
		presnapshot := filepath.Join(options.DataDir, "pre-restore-"+options.Now().UTC().Format("20060102T150405")+".db")
		live, err := sqlite.Open(livePath)
		if err != nil {
			return nil, restoreErr("presnapshot", RestoreCodeSnapshot, err)
		}
		err = live.Snapshot(ctx, presnapshot)
		live.Close()
		if err != nil {
			return nil, restoreErr("presnapshot", RestoreCodeSnapshot, err)
		}
		if integrity, err := sqlite.VerifySnapshot(presnapshot); err != nil || integrity != "ok" {
			return nil, restoreErr("presnapshot", RestoreCodeSnapshot, err)
		}
		state.PresnapshotPath = presnapshot
	}
	state.Stage = StageSnapshot
	if err := writeRecoveryState(options.DataDir, state); err != nil {
		return nil, restoreErr("presnapshot", RestoreCodeInternal, err)
	}
	if options.Hooks.AfterSnapshot != nil {
		if err := options.Hooks.AfterSnapshot(); err != nil {
			return hookFailure("snapshot", options.DataDir, state, err)
		}
	}

	// 4. Candidate database on the target filesystem (same fs as the live
	// database, so the final rename is atomic).
	candidatePath := filepath.Join(options.DataDir, "restore-candidate.db")
	state.CandidatePath = candidatePath
	if err := copyFile(filepath.Join(extractDir, SnapshotPath), candidatePath); err != nil {
		return nil, restoreErr("candidate", RestoreCodeInternal, err)
	}
	state.Stage = StageCandidate
	if err := writeRecoveryState(options.DataDir, state); err != nil {
		return nil, restoreErr("candidate", RestoreCodeInternal, err)
	}

	// 5. Re-encrypt every payload under the mounted key; clear short-term
	// auth state. The source key exists only in memory and the restricted
	// work dir (D11).
	report, err := RekeyCandidate(candidatePath, sourceKey, targetKey)
	if err != nil {
		return nil, restoreErr("rekey", RestoreCodeRekey, err)
	}
	state.Stage = StageRekeyed
	if err := writeRecoveryState(options.DataDir, state); err != nil {
		return nil, restoreErr("rekey", RestoreCodeInternal, err)
	}
	if options.Hooks.AfterRekey != nil {
		if err := options.Hooks.AfterRekey(); err != nil {
			return hookFailure("rekey", options.DataDir, state, err)
		}
	}

	// 6. Compatible migration, integrity, and full decrypt verification of
	// the candidate BEFORE it becomes the live database.
	candidate, err := sqlite.Open(candidatePath)
	if err != nil {
		return nil, restoreErr("migrate", RestoreCodeMigrate, err)
	}
	if err := sqlite.Migrate(candidate.DB, options.Migrations); err != nil {
		candidate.Close()
		return nil, restoreErr("migrate", RestoreCodeMigrate, err)
	}
	if err := candidate.Checkpoint(); err != nil {
		candidate.Close()
		return nil, restoreErr("migrate", RestoreCodeMigrate, err)
	}
	candidate.Close()
	if err := verifyCandidate(candidatePath, targetKey); err != nil {
		return nil, restoreErr("verify", RestoreCodeVerify, err)
	}
	state.Stage = StageRekeyed // migrated but not yet switched
	if err := writeRecoveryState(options.DataDir, state); err != nil {
		return nil, restoreErr("migrate", RestoreCodeInternal, err)
	}
	if options.Hooks.AfterMigrate != nil {
		if err := options.Hooks.AfterMigrate(); err != nil {
			return hookFailure("migrate", options.DataDir, state, err)
		}
	}

	// 7. Atomic switch. The old database's WAL sidecars belong to the old
	// database only: with the service stopped they are removed before the
	// rename so the candidate is never paired with a stale WAL.
	state.Stage = StageSwitched
	if err := writeRecoveryState(options.DataDir, state); err != nil {
		return nil, restoreErr("switch", RestoreCodeInternal, err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(livePath + suffix)
	}
	if err := os.Rename(candidatePath, livePath); err != nil {
		return nil, restoreErr("switch", RestoreCodeSwitch, err)
	}
	if options.Hooks.AfterSwitch != nil {
		if err := options.Hooks.AfterSwitch(); err != nil {
			if errors.Is(err, ErrRestoreCrash) {
				// Emulated process death: the switched database stays in
				// place and the next start resumes the post-check.
				return nil, restoreErr("switch", RestoreCodeSwitch, err)
			}
			return rollbackRestore(options, state, err)
		}
	}

	// 8. Post-switch check: the switched database must open, migrate and
	// verify; any failure rolls back to the retained pre-snapshot.
	if err := postSwitchCheck(livePath, options, targetKey); err != nil {
		return rollbackRestore(options, state, err)
	}
	removeRecoveryState(options.DataDir)
	os.RemoveAll(filepath.Join(options.WorkDir))
	return &RestoreResult{
		BackupID:        manifest.BackupID,
		Items:           report.Items,
		Versions:        report.Versions,
		AuthCleared:     report.AuthCleared,
		PresnapshotPath: state.PresnapshotPath,
	}, nil
}

// resumeRestore completes a restore whose process died. Before the switch
// the old database is intact: discard the half-built candidate and restart.
// After the switch the candidate is complete and verified, so only the
// post-switch check remains — it either keeps the new database or rolls
// back to the pre-snapshot.
func resumeRestore(ctx context.Context, options RestoreOptions, targetKey *crypto.MasterKey, state *RecoveryState) (*RestoreResult, error) {
	livePath := filepath.Join(options.DataDir, "tiny-password.db")
	if state.Stage == StageSwitched {
		live, err := sqlite.Open(livePath)
		if err != nil {
			return rollbackRestore(options, state, err)
		}
		err = postSwitchCheck(livePath, options, targetKey)
		live.Close()
		if err != nil {
			return rollbackRestore(options, state, err)
		}
		removeRecoveryState(options.DataDir)
		return &RestoreResult{
			BackupID:        state.BackupID,
			PresnapshotPath: state.PresnapshotPath,
			Resumed:         true,
		}, nil
	}
	// Incomplete candidate: drop it and any work dir, then restart cleanly.
	if state.CandidatePath != "" {
		os.Remove(state.CandidatePath)
	}
	os.RemoveAll(filepath.Join(options.WorkDir))
	return runRestore(ctx, options, targetKey, state)
}

// rollbackRestore restores the retained pre-snapshot over a failed switch.
// An empty target is cleaned back to empty.
func rollbackRestore(options RestoreOptions, state *RecoveryState, cause error) (*RestoreResult, error) {
	livePath := filepath.Join(options.DataDir, "tiny-password.db")
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(livePath + suffix)
	}
	if state.EmptyTarget {
		_ = os.Remove(livePath)
	} else if state.PresnapshotPath != "" {
		if err := os.Rename(state.PresnapshotPath, livePath); err != nil {
			return nil, restoreErr("rollback", RestoreCodeSwitch, err)
		}
	}
	removeRecoveryState(options.DataDir)
	return nil, restoreErr("switch", RestoreCodeRolledBack, cause)
}

// verifyRestoreTree checks the extracted set against the manifest
// whitelist: snapshot + master key + manifest, nothing more.
func verifyRestoreTree(extractDir string, entries []archive.Entry) error {
	want := map[string]bool{ManifestPath: true, SnapshotPath: true, MasterKeyPath: true}
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		if !want[e.Path] {
			return fmt.Errorf("unexpected archive entry %s", e.Path)
		}
		delete(want, e.Path)
	}
	if len(want) != 0 {
		return fmt.Errorf("archive entries missing")
	}
	manifest, err := readManifestFile(extractDir)
	if err != nil {
		return err
	}
	for _, f := range manifest.Files {
		entry, err := fileDigest(extractDir, f.Path)
		if err != nil {
			return err
		}
		if entry.SHA256 != f.SHA256 || entry.Size != f.Size {
			return fmt.Errorf("digest mismatch for %s", f.Path)
		}
	}
	integrity, err := sqlite.VerifySnapshot(filepath.Join(extractDir, SnapshotPath))
	if err != nil || integrity != "ok" {
		return fmt.Errorf("snapshot integrity check failed")
	}
	return nil
}

// verifyCandidate decrypts every current and historical payload with the
// target key — the restore succeeds only when nothing is unreadable.
func verifyCandidate(candidatePath string, targetKey *crypto.MasterKey) error {
	return iteratePayloads(candidatePath, func(version uint16, nonce, ct []byte, aad crypto.AAD) error {
		_, err := targetKey.DecryptColumns(version, nonce, ct, aad)
		return err
	})
}

// postSwitchCheck opens the switched database and re-runs the integrity
// and decrypt verification in its final location.
func postSwitchCheck(livePath string, options RestoreOptions, targetKey *crypto.MasterKey) error {
	db, err := sqlite.Open(livePath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := sqlite.Migrate(db.DB, options.Migrations); err != nil {
		return err
	}
	integrity, err := db.IntegrityCheck()
	if err != nil || integrity != "ok" {
		return fmt.Errorf("integrity check failed")
	}
	return verifyCandidate(livePath, targetKey)
}

// hookFailure maps a stage-hook failure: a simulated crash leaves the state
// untouched (resume later); any other failure cleans the candidate.
func hookFailure(stage, dataDir string, state *RecoveryState, err error) (*RestoreResult, error) {
	if errors.Is(err, ErrRestoreCrash) {
		return nil, restoreErr(stage, RestoreCodeSwitch, err)
	}
	if state.Stage != StageSwitched {
		if state.CandidatePath != "" {
			_ = os.Remove(state.CandidatePath)
		}
		removeRecoveryState(dataDir)
	}
	return nil, restoreErr(stage, RestoreCodeSwitch, err)
}

func restoreSpaceCheck(options RestoreOptions, need int64) error {
	if options.Hooks.SpaceCheck != nil {
		return options.Hooks.SpaceCheck(options.DataDir, need)
	}
	if err := ensureDir(options.DataDir); err != nil {
		return err
	}
	free, err := freeBytes(options.DataDir)
	if err != nil {
		return err
	}
	if free < need {
		return errors.New("insufficient space")
	}
	return nil
}

// iteratePayloads walks every encrypted payload (current rows and history
// versions) of a database and invokes fn with its row-bound AAD.
func iteratePayloads(dbPath string, fn func(version uint16, nonce, ct []byte, aad crypto.AAD) error) error {
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return fmt.Errorf("open for verify: %w", err)
	}
	defer db.Close()

	type identity struct {
		scope   string
		subject string
	}
	items := map[string]identity{}
	rows, err := db.Query(`SELECT id, vault_scope, COALESCE(owner_user_id, ''), COALESCE(created_by_user_id, '') FROM vault_items`)
	if err != nil {
		return fmt.Errorf("list identities: %w", err)
	}
	for rows.Next() {
		var id, scope, owner, creator string
		if err := rows.Scan(&id, &scope, &owner, &creator); err != nil {
			rows.Close()
			return err
		}
		subject := owner
		if subject == "" {
			subject = creator
		}
		items[id] = identity{scope: scope, subject: subject}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, query := range []string{
		`SELECT id, payload_version, nonce, ciphertext, revision FROM vault_items`,
		`SELECT item_id, payload_version, nonce, ciphertext, revision FROM item_versions`,
	} {
		rows, err := db.Query(query)
		if err != nil {
			return fmt.Errorf("read payloads: %w", err)
		}
		for rows.Next() {
			var id string
			var version uint16
			var nonce, ct []byte
			var revision uint64
			if err := rows.Scan(&id, &version, &nonce, &ct, &revision); err != nil {
				rows.Close()
				return err
			}
			it, ok := items[id]
			if !ok {
				rows.Close()
				return fmt.Errorf("verify: missing identity for %s", id)
			}
			aad := vault.AADFor(id, it.scope, it.subject, "", version, revision)
			if err := fn(version, nonce, ct, aad); err != nil {
				rows.Close()
				return err
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := out.ReadFrom(in); err != nil {
		return err
	}
	return out.Sync()
}

func maxMigrationVersion(fsys fs.FS) (int64, error) {
	names, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return 0, err
	}
	var max int64
	for _, name := range names {
		var v int64
		if _, err := fmt.Sscanf(name, "%d", &v); err == nil && v > max {
			max = v
		}
	}
	return max, nil
}
