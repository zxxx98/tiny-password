package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/tiny-password/tiny-password/internal/audit"
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
	// BeforeSwitch runs after the switch-ready state is durable but before
	// the live database is touched; returning ErrRestoreCrash emulates a
	// process death in the rename-pending window.
	BeforeSwitch func() error
	// AfterSwitch runs after the atomic rename but before the post-switch
	// check; returning ErrRestoreCrash emulates a process death at exactly
	// that breakpoint.
	AfterSwitch func() error
	SpaceCheck  func(dir string, need int64) error
}

// RestoreOptions configures one offline restore.
type RestoreOptions struct {
	DataDir string // target instance data directory
	// WorkDir is the restricted tmpfs base for extraction (the source key
	// passes through it). The restore creates its own private session child
	// below it and only ever removes that child, never WorkDir itself.
	WorkDir     string
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
	Audit      *audit.Service
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
// candidate build (rekey + migrate + verify + identity stamp) → atomic
// switch → post-check with backup identity. The caller must have stopped
// the HTTP service; the data-dir lock makes concurrent operation impossible.
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
	var result *RestoreResult
	var restoreErrValue error
	if state, stateErr := ReadRecoveryState(options.DataDir); stateErr != nil {
		restoreErrValue = restoreErr("start", RestoreCodeStateUnreadable, stateErr)
	} else if state != nil {
		result, restoreErrValue = resumeRestore(ctx, options, targetKey, state)
	} else {
		result, restoreErrValue = runRestore(ctx, options, targetKey, nil)
	}
	recordRestoreAudit(options, result, restoreErrValue)
	return result, restoreErrValue
}

// recordRestoreAudit writes a redacted outcome to the target database after
// the restore attempt. It is best effort: failures before a database exists
// have nowhere safe to persist an event, and an audit outage must not alter
// the restore result.
func recordRestoreAudit(options RestoreOptions, result *RestoreResult, restoreErrValue error) {
	if options.Audit == nil {
		return
	}
	livePath := filepath.Join(options.DataDir, "tiny-password.db")
	if _, err := os.Stat(livePath); err != nil {
		return
	}
	db, err := sqlite.Open(livePath)
	if err != nil {
		return
	}
	defer db.Close()
	targetID := ""
	if result != nil {
		targetID = result.BackupID
	}
	auditResult := audit.ResultSuccess
	if restoreErrValue != nil {
		auditResult = audit.ResultFailure
	}
	_ = options.Audit.Record(context.Background(), db.DB, audit.Event{
		Name:       audit.EventBackupRestore,
		ActorID:    audit.Anonymous,
		TargetType: audit.TargetBackup,
		TargetID:   targetID,
		Result:     auditResult,
	})
}

func runRestore(ctx context.Context, options RestoreOptions, targetKey *crypto.MasterKey, existing *RecoveryState) (*RestoreResult, error) {
	// All extraction happens inside this instance's private session child of
	// the configured work dir; only the session is ever removed — never the
	// configured parent, which may hold unrelated content (a shared tmpfs
	// mount is a valid target). The extracted set includes the archive's
	// plaintext source master key, so the session is wiped on every start,
	// including a resumed one.
	session, err := restoreSessionDir(options)
	if err != nil {
		return nil, restoreErr("start", RestoreCodeInternal, err)
	}
	defer os.RemoveAll(session)
	// 1. Extract and verify the archive under the instance limits.
	extractDir, entries, err := archive.ExtractLimited(ctx, options.ArchivePath, options.Passphrase, session, InstanceLimits(0))
	if err != nil {
		return nil, restoreErr("extract", RestoreCodeArchive, err)
	}
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
	} else {
		// A retry may present a different archive: nothing is committed
		// before the switch, so the freshly verified manifest becomes the
		// attempt's identity and stays consistent with the marker stamped
		// into the new candidate.
		state.BackupID = manifest.BackupID
		state.ArchivePath = options.ArchivePath
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
	// the candidate BEFORE it becomes the live database. The candidate is
	// also stamped with the backup it was built from: the post-switch check
	// needs that identity to prove the live database really is this
	// candidate (and not, after a restart, the old database under the same
	// target key or an accidentally created empty one).
	candidate, err := sqlite.Open(candidatePath)
	if err != nil {
		return nil, restoreErr("migrate", RestoreCodeMigrate, err)
	}
	if err := sqlite.Migrate(candidate.DB, options.Migrations); err != nil {
		candidate.Close()
		return nil, restoreErr("migrate", RestoreCodeMigrate, err)
	}
	if err := stampRestoreMarker(candidate.DB, manifest.BackupID); err != nil {
		candidate.Close()
		return nil, restoreErr("migrate", RestoreCodeInternal, err)
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
	//
	// The state distinguishes "switch pending" (written before the rename)
	// from "switch done" (written after): a crash in between can then never
	// mistake the untouched live database for the restored candidate.
	state.Stage = StageSwitchReady
	if err := writeRecoveryState(options.DataDir, state); err != nil {
		return nil, restoreErr("switch", RestoreCodeInternal, err)
	}
	if options.Hooks.BeforeSwitch != nil {
		if err := options.Hooks.BeforeSwitch(); err != nil {
			return hookFailure("switch", options.DataDir, state, err)
		}
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(livePath + suffix)
	}
	if err := os.Rename(candidatePath, livePath); err != nil {
		// The live database is still the previous one; the switch-ready
		// state and the verified candidate remain for the retry to resume.
		return nil, restoreErr("switch", RestoreCodeSwitch, err)
	}
	state.Stage = StageSwitched
	if err := writeRecoveryState(options.DataDir, state); err != nil {
		// The rename succeeded; the switch-ready state (candidate gone)
		// makes the next start infer and verify the completed switch.
		return nil, restoreErr("switch", RestoreCodeInternal, err)
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
	// verify, and it must carry this restore's backup identity; any failure
	// rolls back to the retained pre-snapshot.
	if err := postSwitchCheck(livePath, options, targetKey, manifest.BackupID); err != nil {
		return rollbackRestore(options, state, err)
	}
	removeRecoveryState(options.DataDir)
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
// From switch-ready on, the candidate is complete and verified: complete a
// pending rename, then re-run the post-switch check — it must confirm the
// backup identity before the restore may report success, and any failure
// rolls back to the pre-snapshot.
func resumeRestore(ctx context.Context, options RestoreOptions, targetKey *crypto.MasterKey, state *RecoveryState) (*RestoreResult, error) {
	livePath := filepath.Join(options.DataDir, "tiny-password.db")
	switch state.Stage {
	case StageSwitchReady, StageSwitched:
		if state.Stage == StageSwitchReady && state.CandidatePath != "" {
			if _, err := os.Stat(state.CandidatePath); err == nil {
				// The rename never happened: the live database is still the
				// previous one (or the target is still empty). Complete the
				// pending switch — the verified candidate is already stamped
				// with the recorded backup identity.
				for _, suffix := range []string{"-wal", "-shm"} {
					_ = os.Remove(livePath + suffix)
				}
				if err := os.Rename(state.CandidatePath, livePath); err != nil {
					return nil, restoreErr("switch", RestoreCodeSwitch, err)
				}
				state.Stage = StageSwitched
				if err := writeRecoveryState(options.DataDir, state); err != nil {
					return nil, restoreErr("switch", RestoreCodeInternal, err)
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, restoreErr("switch", RestoreCodeInternal, err)
			}
			// A missing candidate means the rename already happened and only
			// the switched-state write was lost; fall through to the check.
		}
		if _, err := os.Stat(livePath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// Never let opening the live path manufacture an empty
				// database: a switched state without a database fails and
				// rolls back instead of reporting success.
				return rollbackRestore(options, state, errors.New("switched database missing"))
			}
			return nil, restoreErr("switch", RestoreCodeInternal, err)
		}
		if err := postSwitchCheck(livePath, options, targetKey, state.BackupID); err != nil {
			return rollbackRestore(options, state, err)
		}
		removeRecoveryState(options.DataDir)
		return &RestoreResult{
			BackupID:        state.BackupID,
			PresnapshotPath: state.PresnapshotPath,
			Resumed:         true,
		}, nil
	default:
		// Incomplete candidate: drop it, then restart cleanly — runRestore
		// recreates and wipes this instance's restore session, clearing the
		// previous attempt's leftovers (including its extracted source key).
		if state.CandidatePath != "" {
			os.Remove(state.CandidatePath)
		}
		return runRestore(ctx, options, targetKey, state)
	}
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
// and decrypt verification in its final location, then confirms that the
// database really is the candidate built from the recorded backup.
func postSwitchCheck(livePath string, options RestoreOptions, targetKey *crypto.MasterKey, backupID string) error {
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
	if err := verifyCandidate(livePath, targetKey); err != nil {
		return err
	}
	marker, err := readRestoreMarker(db.DB)
	if err != nil {
		return err
	}
	if marker != backupID {
		return fmt.Errorf("live database is not the restored candidate: marker %q, want backup %s", marker, backupID)
	}
	return nil
}

// restoreSessionDir derives this instance's private extraction directory
// below the configured work dir. The name is a hash of the data dir:
// restores of one instance are serialized by the data-dir lock, so the only
// directory this name can ever collide with is a leftover of an earlier
// attempt of the same restore — exactly what may be removed. Neighbouring
// sessions of other instances are never touched.
func restoreSessionDir(options RestoreOptions) (string, error) {
	if err := ensureDir(options.WorkDir); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(options.DataDir))
	session := filepath.Join(options.WorkDir, "restore-"+hex.EncodeToString(sum[:8]))
	if err := os.RemoveAll(session); err != nil {
		return "", err
	}
	if err := os.Mkdir(session, 0o700); err != nil {
		return "", err
	}
	return session, nil
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
