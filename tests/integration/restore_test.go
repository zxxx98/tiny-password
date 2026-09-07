package integration

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tiny-password/tiny-password/internal/audit"
	"github.com/tiny-password/tiny-password/internal/auth"
	"github.com/tiny-password/tiny-password/internal/backup"
	"github.com/tiny-password/tiny-password/internal/platform/archive"
	"github.com/tiny-password/tiny-password/internal/platform/crypto"
	"github.com/tiny-password/tiny-password/internal/platform/sqlite"
	"github.com/tiny-password/tiny-password/internal/vault"
	"github.com/tiny-password/tiny-password/migrations"
)

// restoreFixture holds a source instance with data, a produced backup, and
// a target data dir with its own (new) master key.
type restoreFixture struct {
	source     *backupHarness
	sourceKey  []byte
	archive    string
	targetDir  string
	targetKey  []byte
	workDir    string
	ownerID    string
	itemID     string
	backupPass string
}

func newRestoreFixture(t *testing.T) *restoreFixture {
	t.Helper()
	archiveBin(t)
	source := newBackupHarness(t)
	sourceKey := source.keyRaw

	key, err := crypto.NewMasterKey(sourceKey)
	if err != nil {
		t.Fatal(err)
	}
	vsvc, err := vault.NewService(source.db.DB, key, vault.Options{Audit: audit.NewService(audit.Options{})})
	if err != nil {
		t.Fatal(err)
	}
	owner := "restore-user-0000000000000000"
	if _, err := source.db.Exec(
		`INSERT INTO users (id, username_norm, username_display, role, status, must_change_password, password_hash, created_at, updated_at)
		 VALUES (?, 'restore-user', 'restore-user', 'member', 'active', 0, 'synthetic', ?, ?)`,
		owner, time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		t.Fatal(err)
	}
	actor := &auth.Principal{UserID: owner, Username: "restore-user", Role: "member"}
	detail, err := vsvc.Create(context.Background(), actor, vault.CreateInput{
		Scope:    "personal",
		ItemType: "secure_note",
		Tags:     []string{"keep"},
		Payload:  json.RawMessage(`{"name":"restorable","body":"restore me"}`),
	}, nil)
	if err != nil {
		t.Fatalf("seed item: %v", err)
	}
	// One history revision so item_versions participates in the rekey.
	if _, err := vsvc.Update(context.Background(), actor, detail.ID, vault.UpdateInput{
		Revision: detail.Revision,
		Payload:  json.RawMessage(`{"name":"restorable v2","body":"restored body"}`),
	}); err != nil {
		t.Fatalf("seed revision: %v", err)
	}

	if _, err := source.run(t, source.runner); err != nil {
		t.Fatalf("backup: %v", err)
	}
	archivePath := filepath.Join(source.localDir, listDirNames(t, source.localDir)[0])

	targetKey := make([]byte, 32)
	if _, err := rand.Read(targetKey); err != nil {
		t.Fatal(err)
	}
	return &restoreFixture{
		source:     source,
		sourceKey:  sourceKey,
		archive:    archivePath,
		targetDir:  t.TempDir(),
		targetKey:  targetKey,
		workDir:    filepath.Join(t.TempDir(), "restore-work"),
		ownerID:    owner,
		itemID:     detail.ID,
		backupPass: "backup-passphrase-1",
	}
}

func (f *restoreFixture) opts() backup.RestoreOptions {
	return backup.RestoreOptions{
		DataDir:      f.targetDir,
		WorkDir:      f.workDir,
		ArchivePath:  f.archive,
		Passphrase:   f.backupPass,
		TargetKeyRaw: f.targetKey,
		Migrations:   migrations.FS,
		AppVersion:   "test",
		Audit:        audit.NewService(audit.Options{}),
	}
}

// verifyAllWithKey decrypts every current and historical payload with the
// given key; the first failure is returned.
func verifyAllWithKey(t *testing.T, dbPath string, key *crypto.MasterKey) error {
	t.Helper()
	db, err := sql.Open(sqlite.DriverName, "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	type identity struct {
		scope   string
		subject string
	}
	items := map[string]identity{}
	rows, err := db.Query(`SELECT id, vault_scope, COALESCE(owner_user_id, ''), COALESCE(created_by_user_id, '') FROM vault_items`)
	if err != nil {
		return err
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
			return err
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
				return fmt.Errorf("missing identity for %s", id)
			}
			aad := vault.AADFor(id, it.scope, it.subject, "", version, revision)
			if _, err := key.DecryptColumns(version, nonce, ct, aad); err != nil {
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

// openRestored opens the restored database and verifies the new key
// decrypts every payload while the old key cannot.
func (f *restoreFixture) openRestored(t *testing.T) *vault.Service {
	t.Helper()
	livePath := filepath.Join(f.targetDir, "tiny-password.db")
	if _, err := os.Stat(livePath); err != nil {
		t.Fatalf("restored db missing: %v", err)
	}
	db, err := sqlite.Open(livePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := sqlite.Migrate(db.DB, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if integrity, err := db.IntegrityCheck(); err != nil || integrity != "ok" {
		t.Fatalf("integrity: %q %v", integrity, err)
	}
	newKey, err := crypto.NewMasterKey(f.targetKey)
	if err != nil {
		t.Fatal(err)
	}
	vsvc, err := vault.NewService(db.DB, newKey, vault.Options{Audit: audit.NewService(audit.Options{})})
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyAllWithKey(t, livePath, newKey); err != nil {
		t.Fatalf("new key verification: %v", err)
	}
	oldKey, err := crypto.NewMasterKey(f.sourceKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyAllWithKey(t, livePath, oldKey); err == nil {
		t.Fatal("old key still decrypts the rekeyed database")
	}
	return vsvc
}

func TestRestoreFullRoundTripWithNewKey(t *testing.T) {
	f := newRestoreFixture(t)
	result, err := backup.Restore(context.Background(), f.opts())
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if result.Items != 1 || result.Versions != 1 {
		t.Fatalf("rekey counts: %+v", result)
	}
	if result.PresnapshotPath != "" {
		t.Fatalf("unexpected pre-snapshot for empty target: %s", result.PresnapshotPath)
	}

	vsvc := f.openRestored(t)
	var restoreAuditCount int
	if err := f.source.db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE event = ? AND result = 'success'`, audit.EventBackupRestore).Scan(&restoreAuditCount); err != nil {
		t.Fatal(err)
	}
	if restoreAuditCount != 0 {
		t.Fatal("restore audit was written to source database")
	}
	readDB, err := sql.Open(sqlite.DriverName, "file:"+filepath.Join(f.targetDir, "tiny-password.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer readDB.Close()
	var restoredAuditCount int
	if err := readDB.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE event = ? AND result = 'success'`, audit.EventBackupRestore).Scan(&restoredAuditCount); err != nil {
		t.Fatal(err)
	}
	if restoredAuditCount != 1 {
		t.Fatalf("restore audit rows=%d, want 1", restoredAuditCount)
	}
	actor := &auth.Principal{UserID: f.ownerID, Username: "restore-user", Role: "member"}
	detail, err := vsvc.Get(context.Background(), actor, f.itemID)
	if err != nil {
		t.Fatalf("read restored item: %v", err)
	}
	if detail.Revision != 2 {
		t.Fatalf("revision preserved: %d", detail.Revision)
	}
	if title := vault.TitleOf(detail.Payload); title != "restorable v2" {
		t.Fatalf("payload: %v", title)
	}

	// Sessions, rate-limit aggregation and idempotency claims were cleared.
	livePath := filepath.Join(f.targetDir, "tiny-password.db")
	rowDB, err := sql.Open(sqlite.DriverName, "file:"+livePath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer rowDB.Close()
	for _, query := range []string{
		"SELECT COUNT(*) FROM sessions",
		"SELECT COUNT(*) FROM login_attempts",
		"SELECT COUNT(*) FROM idempotency_keys",
	} {
		var n int
		if err := rowDB.QueryRow(query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("%s: %d rows", query, n)
		}
	}
	// The restored database carries the restore marker identifying the
	// backup it was built from — the identity the post-switch check relies
	// on after a restart.
	var marker string
	if err := rowDB.QueryRow(`SELECT value FROM system_state WHERE key = 'restore_backup_id'`).Scan(&marker); err != nil {
		t.Fatal(err)
	}
	if marker != result.BackupID {
		t.Fatalf("restore marker: got %q, want backup %s", marker, result.BackupID)
	}
}

// Restoring over an EXISTING instance takes a recoverable pre-snapshot
// before the switch and keeps it afterwards.
func TestRestoreOverExistingInstancePreservesSnapshot(t *testing.T) {
	f := newRestoreFixture(t)
	db, err := sqlite.Open(filepath.Join(f.targetDir, "tiny-password.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlite.Migrate(db.DB, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO users (id, username_norm, username_display, role, status, must_change_password, password_hash, created_at, updated_at)
		 VALUES ('old-user', 'old-user', 'old-user', 'admin', 'active', 0, 'synthetic', ?, ?)`,
		time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		t.Fatal(err)
	}
	db.Close()

	result, err := backup.Restore(context.Background(), f.opts())
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if result.PresnapshotPath == "" {
		t.Fatal("pre-snapshot missing")
	}
	if integrity, err := sqlite.VerifySnapshot(result.PresnapshotPath); err != nil || integrity != "ok" {
		t.Fatalf("pre-snapshot integrity: %q %v", integrity, err)
	}
	f.openRestored(t)
	rowDB, err := sql.Open(sqlite.DriverName, "file:"+filepath.Join(f.targetDir, "tiny-password.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer rowDB.Close()
	var n int
	if err := rowDB.QueryRow("SELECT COUNT(*) FROM users WHERE id = 'old-user'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("target-era user survived the switch: %d", n)
	}
}

// The data-directory lock refuses a restore while a service-shaped lock
// holder is alive.
func TestRestoreRefusesRunningService(t *testing.T) {
	f := newRestoreFixture(t)
	lock, err := backup.AcquireDataDirLock(f.targetDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	_, err = backup.Restore(context.Background(), f.opts())
	if !errors.Is(err, backup.ErrDataDirLocked) {
		t.Fatalf("want ErrDataDirLocked, got %v", err)
	}
	var restoreErr *backup.RestoreError
	if !errors.As(err, &restoreErr) || restoreErr.Code != backup.RestoreCodeDataDirLocked {
		t.Fatalf("code: %v", err)
	}
}

func TestRestoreRejectsWrongPassphraseAndCorruptArchive(t *testing.T) {
	f := newRestoreFixture(t)
	opts := f.opts()
	opts.Passphrase = "wrong-passphrase-9"
	if _, err := backup.Restore(context.Background(), opts); err == nil {
		t.Fatal("wrong passphrase accepted")
	} else if re, ok := err.(*backup.RestoreError); !ok || re.Code != backup.RestoreCodeArchive {
		t.Fatalf("code: %v", err)
	}

	corruptPath := f.opts().ArchivePath + ".corrupt.7z"
	if err := os.WriteFile(corruptPath, []byte("not an archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(corruptPath) })
	corrupt := f.opts()
	corrupt.ArchivePath = corruptPath
	if _, err := backup.Restore(context.Background(), corrupt); err == nil {
		t.Fatal("corrupt archive accepted")
	} else if re, ok := err.(*backup.RestoreError); !ok || re.Code != backup.RestoreCodeArchive {
		t.Fatalf("code: %v", err)
	}
	// The target stayed empty; no candidate or state remnants (the
	// service.lock file from the lock check is legitimate).
	for _, name := range listDirNames(t, f.targetDir) {
		if name != "service.lock" {
			t.Fatalf("target polluted: %v", listDirNames(t, f.targetDir))
		}
	}
}

// archiveDirFromBackup extracts a produced archive for manifest surgery.
func archiveDirFromBackup(t *testing.T, f *restoreFixture) string {
	t.Helper()
	dest, _, err := archive.Extract(context.Background(), f.archive, f.backupPass, f.source.workDir)
	if err != nil {
		t.Fatal(err)
	}
	return dest
}

func repackDir(t *testing.T, workDir, src, passphrase string) string {
	t.Helper()
	path, err := archive.Create(context.Background(), archive.CreateOptions{
		SourceDir: src, WorkDir: workDir, Passphrase: passphrase,
	})
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// A schema version newer than the running binary is refused; an older one
// is migrated forward.
func TestRestoreSchemaCompatRange(t *testing.T) {
	f := newRestoreFixture(t)
	src := archiveDirFromBackup(t, f)
	defer os.RemoveAll(src)
	var manifest backup.Manifest
	raw, err := os.ReadFile(filepath.Join(src, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.SchemaVersion = 999
	updated, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "manifest.json"), updated, 0o600); err != nil {
		t.Fatal(err)
	}
	repacked := repackDir(t, f.source.workDir, src, f.backupPass)

	opts := f.opts()
	opts.ArchivePath = repacked
	if _, err := backup.Restore(context.Background(), opts); err == nil {
		t.Fatal("too-new schema accepted")
	} else if re, ok := err.(*backup.RestoreError); !ok || re.Code != backup.RestoreCodeSchemaTooNew {
		t.Fatalf("code: %v", err)
	}

	// An older-declared schema is fine: the candidate migrates forward.
	f2 := newRestoreFixture(t)
	src2 := archiveDirFromBackup(t, f2)
	defer os.RemoveAll(src2)
	var manifest2 backup.Manifest
	raw2, err := os.ReadFile(filepath.Join(src2, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw2, &manifest2); err != nil {
		t.Fatal(err)
	}
	manifest2.SchemaVersion = 1
	updated2, err := json.Marshal(manifest2)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src2, "manifest.json"), updated2, 0o600); err != nil {
		t.Fatal(err)
	}
	repacked2 := repackDir(t, f2.source.workDir, src2, f2.backupPass)
	opts2 := f2.opts()
	opts2.ArchivePath = repacked2
	if _, err := backup.Restore(context.Background(), opts2); err != nil {
		t.Fatalf("older schema restore: %v", err)
	}
}

// seedOriginalInstance creates a target-era database with its own key, so a
// failed restore can be checked for preserving the original instance.
func seedOriginalInstance(t *testing.T, dataDir string, key []byte) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(dataDir, "tiny-password.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := sqlite.Migrate(db.DB, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO users (id, username_norm, username_display, role, status, must_change_password, password_hash, created_at, updated_at)
		 VALUES ('orig-user', 'orig-user', 'orig-user', 'admin', 'active', 0, 'synthetic', ?, ?)`,
		time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		t.Fatal(err)
	}
}

// Injected failures at every stage leave the original instance startable
// and consistent; a rerun after the failure completes the restore.
func TestRestoreFaultInjectionStages(t *testing.T) {
	stages := []string{"snapshot", "rekey", "migrate", "switch"}
	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			f := newRestoreFixture(t)
			existingKey := make([]byte, 32)
			if _, err := rand.Read(existingKey); err != nil {
				t.Fatal(err)
			}
			seedOriginalInstance(t, f.targetDir, existingKey)

			opts := f.opts()
			hookErr := errors.New("injected stage failure")
			switch stage {
			case "snapshot":
				opts.Hooks.AfterSnapshot = func() error { return hookErr }
			case "rekey":
				opts.Hooks.AfterRekey = func() error { return hookErr }
			case "migrate":
				opts.Hooks.AfterMigrate = func() error { return hookErr }
			case "switch":
				opts.Hooks.AfterSwitch = func() error { return hookErr }
			}
			if _, err := backup.Restore(context.Background(), opts); err == nil {
				t.Fatalf("%s: failure not injected", stage)
			}

			// The original instance still opens with its own data intact.
			db2, err := sqlite.Open(filepath.Join(f.targetDir, "tiny-password.db"))
			if err != nil {
				t.Fatalf("original instance unreadable after %s failure: %v", stage, err)
			}
			integrity, err := db2.IntegrityCheck()
			if err != nil || integrity != "ok" {
				t.Fatalf("integrity after %s failure: %q %v", stage, integrity, err)
			}
			var n int
			if err := db2.QueryRow("SELECT COUNT(*) FROM users WHERE id = 'orig-user'").Scan(&n); err != nil || n != 1 {
				db2.Close()
				t.Fatalf("original data lost after %s failure: %d %v", stage, n, err)
			}
			db2.Close()

			// A rerun (the operator simply retries) completes the restore.
			if _, err := backup.Restore(context.Background(), f.opts()); err != nil {
				t.Fatalf("rerun after %s failure: %v", stage, err)
			}
			f.openRestored(t)
		})
	}
}

// A crash right after the switch (state file present, post-check missing)
// resumes on the next start: the switched database is verified and the
// restore completes.
func TestRestoreCrashAfterSwitchResumes(t *testing.T) {
	f := newRestoreFixture(t)
	opts := f.opts()
	opts.Hooks.AfterSwitch = func() error { return backup.ErrRestoreCrash }
	if _, err := backup.Restore(context.Background(), opts); err == nil {
		t.Fatal("crash not injected")
	}
	state, err := backup.ReadRecoveryState(f.targetDir)
	if err != nil || state == nil || state.Stage != backup.StageSwitched {
		t.Fatalf("state: %+v %v", state, err)
	}
	result, err := backup.Restore(context.Background(), f.opts())
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !result.Resumed {
		t.Fatalf("expected resumed restore: %+v", result)
	}
	if state, _ := backup.ReadRecoveryState(f.targetDir); state != nil {
		t.Fatal("recovery state survived completion")
	}
	f.openRestored(t)
}

func TestRestoreInsufficientSpace(t *testing.T) {
	f := newRestoreFixture(t)
	opts := f.opts()
	opts.Hooks.SpaceCheck = func(string, int64) error { return backup.ErrSpace }
	_, err := backup.Restore(context.Background(), opts)
	var restoreErr *backup.RestoreError
	if !errors.As(err, &restoreErr) || restoreErr.Code != backup.RestoreCodeSpace {
		t.Fatalf("code: %v", err)
	}
}

// The restore only ever cleans its own session below the configured work
// dir: unrelated content there must survive (a shared tmpfs mount is a
// valid target) and no session leftovers stay behind.
func TestRestoreCleansOnlyItsOwnSessionBelowWorkDir(t *testing.T) {
	f := newRestoreFixture(t)
	opts := f.opts()
	if err := os.MkdirAll(opts.WorkDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(opts.WorkDir, "operator-file.txt")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Restore(context.Background(), opts); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("work dir content removed by restore: %v", err)
	}
	entries, err := os.ReadDir(opts.WorkDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "operator-file.txt" {
		t.Fatalf("work dir not cleaned back to the operator's content: %v", entries)
	}
}

// A crash in the rename-pending window (switch-ready state durable, rename
// not yet executed) leaves the old database untouched, and the rerun
// completes the pending switch instead of trusting the live database.
func TestRestoreCrashBeforeSwitchResumes(t *testing.T) {
	t.Run("empty target", func(t *testing.T) {
		f := newRestoreFixture(t)
		opts := f.opts()
		opts.Hooks.BeforeSwitch = func() error { return backup.ErrRestoreCrash }
		if _, err := backup.Restore(context.Background(), opts); err == nil {
			t.Fatal("crash not injected")
		}
		state, err := backup.ReadRecoveryState(f.targetDir)
		if err != nil || state == nil || state.Stage != backup.StageSwitchReady {
			t.Fatalf("state: %+v %v", state, err)
		}
		if _, err := os.Stat(state.CandidatePath); err != nil {
			t.Fatalf("verified candidate missing while switch pending: %v", err)
		}
		// The live database must not exist yet — in particular the restore
		// must not have manufactured an empty one.
		if _, err := os.Stat(filepath.Join(f.targetDir, "tiny-password.db")); !os.IsNotExist(err) {
			t.Fatalf("live database exists before the switch: %v", err)
		}
		result, err := backup.Restore(context.Background(), f.opts())
		if err != nil {
			t.Fatalf("resume: %v", err)
		}
		if !result.Resumed {
			t.Fatalf("expected resumed restore: %+v", result)
		}
		f.openRestored(t)
	})
	t.Run("existing target keeps old database until the switch", func(t *testing.T) {
		f := newRestoreFixture(t)
		seedOriginalInstance(t, f.targetDir, f.targetKey)
		opts := f.opts()
		opts.Hooks.BeforeSwitch = func() error { return backup.ErrRestoreCrash }
		if _, err := backup.Restore(context.Background(), opts); err == nil {
			t.Fatal("crash not injected")
		}
		livePath := filepath.Join(f.targetDir, "tiny-password.db")
		db, err := sqlite.Open(livePath)
		if err != nil {
			t.Fatalf("old database unreadable while switch pending: %v", err)
		}
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM users WHERE id = 'orig-user'").Scan(&n); err != nil || n != 1 {
			db.Close()
			t.Fatalf("old database disturbed while switch pending: %d %v", n, err)
		}
		db.Close()
		result, err := backup.Restore(context.Background(), f.opts())
		if err != nil {
			t.Fatalf("resume: %v", err)
		}
		if result.PresnapshotPath == "" {
			t.Fatalf("pre-snapshot lost across resume: %+v", result)
		}
		f.openRestored(t)
	})
}

// copyDBFile copies a cleanly closed SQLite file (no sidecars) for the
// crafted-state tests below.
func copyDBFile(t *testing.T, src, dst string) {
	t.Helper()
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeCraftedState plants a recovery state file directly, emulating a
// process death at an arbitrary breakpoint (the tests below forge states
// the real code no longer produces, to prove the resume path cannot be
// fooled into reporting success).
func writeCraftedState(t *testing.T, dataDir string, state *backup.RecoveryState) {
	t.Helper()
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "restore-state.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Regression: a state file claiming the switch happened must never let the
// OLD database pass as the restored candidate. A previous instance whose
// payloads decrypt under the same target key passes integrity and decrypt
// verification, so the resume path must additionally confirm the backup
// identity — and roll back when it does not match.
func TestRestoreResumeVerifiesSwitchedIdentity(t *testing.T) {
	t.Run("previous instance under the same target key rolls back", func(t *testing.T) {
		f := newRestoreFixture(t)
		seedOriginalInstance(t, f.targetDir, f.targetKey)
		livePath := filepath.Join(f.targetDir, "tiny-password.db")
		presnapshot := filepath.Join(f.targetDir, "pre-restore-crafted.db")
		copyDBFile(t, livePath, presnapshot)
		writeCraftedState(t, f.targetDir, &backup.RecoveryState{
			Stage:           backup.StageSwitched,
			BackupID:        "00000000-not-the-live-database",
			ArchivePath:     f.archive,
			PresnapshotPath: presnapshot,
			CandidatePath:   filepath.Join(f.targetDir, "restore-candidate.db"),
			StartedAt:       time.Now().UTC().Format(time.RFC3339Nano),
			UpdatedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		})

		_, err := backup.Restore(context.Background(), f.opts())
		var restoreErr *backup.RestoreError
		if !errors.As(err, &restoreErr) || restoreErr.Code != backup.RestoreCodeRolledBack {
			t.Fatalf("foreign live database accepted as restored: %v", err)
		}
		// The original instance is back in place and the state cleared.
		db, err := sqlite.Open(livePath)
		if err != nil {
			t.Fatalf("rolled-back database unreadable: %v", err)
		}
		defer db.Close()
		if integrity, err := db.IntegrityCheck(); err != nil || integrity != "ok" {
			t.Fatalf("integrity after rollback: %q %v", integrity, err)
		}
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM users WHERE id = 'orig-user'").Scan(&n); err != nil || n != 1 {
			t.Fatalf("original data lost after identity rollback: %d %v", n, err)
		}
		if state, _ := backup.ReadRecoveryState(f.targetDir); state != nil {
			t.Fatal("recovery state survived rollback")
		}
	})
	t.Run("switched state without a database never reports success", func(t *testing.T) {
		f := newRestoreFixture(t)
		writeCraftedState(t, f.targetDir, &backup.RecoveryState{
			Stage:       backup.StageSwitched,
			BackupID:    "00000000-no-database",
			EmptyTarget: true,
			StartedAt:   time.Now().UTC().Format(time.RFC3339Nano),
			UpdatedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		})
		if _, err := backup.Restore(context.Background(), f.opts()); err == nil {
			t.Fatal("missing switched database reported as success")
		} else if restoreErr, ok := err.(*backup.RestoreError); !ok || restoreErr.Code != backup.RestoreCodeRolledBack {
			t.Fatalf("code: %v", err)
		}
		// Opening the live path must not have manufactured an empty database.
		if _, err := os.Stat(filepath.Join(f.targetDir, "tiny-password.db")); !os.IsNotExist(err) {
			t.Fatalf("empty database created during resume: %v", err)
		}
	})
}
