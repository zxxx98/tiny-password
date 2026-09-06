package integration

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tiny-password/tiny-password/internal/backup"
	"github.com/tiny-password/tiny-password/internal/platform/archive"
	"github.com/tiny-password/tiny-password/internal/platform/sqlite"
)

// backupHarness is a service-level harness: migrated DB, restricted work
// dir, synthetic master key, and a local publish dir. No HTTP surface —
// backups never run inside web requests (design §11.5).
type backupHarness struct {
	db       *sqlite.DB
	workDir  string
	localDir string
	keyRaw   []byte
	runner   *backup.Runner
}

func newBackupHarness(t *testing.T) *backupHarness {
	t.Helper()
	archiveBin(t)
	db := openMigratedDB(t)
	keyRaw := make([]byte, 32)
	if _, err := rand.Read(keyRaw); err != nil {
		t.Fatal(err)
	}
	h := &backupHarness{
		db:       db,
		workDir:  filepath.Join(t.TempDir(), "backup-tmp"),
		localDir: filepath.Join(t.TempDir(), "backups"),
		keyRaw:   keyRaw,
	}
	h.runner = h.newRunner(t, backup.Hooks{})
	return h
}

func (h *backupHarness) newRunner(t *testing.T, hooks backup.Hooks) *backup.Runner {
	t.Helper()
	runner, err := backup.NewRunner(backup.Options{
		DB:           h.db,
		WorkDir:      h.workDir,
		AppVersion:   "test",
		MasterKeyRaw: h.keyRaw,
		Now:          func() time.Time { return time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC) },
		Hooks:        hooks,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func (h *backupHarness) run(t *testing.T, runner *backup.Runner) (*backup.RunResult, error) {
	t.Helper()
	return runner.Run(context.Background(), backup.RunInput{
		Target:     backup.TargetLocal,
		Trigger:    backup.TriggerManual,
		Passphrase: "backup-passphrase-1",
		LocalDir:   h.localDir,
	})
}

func (h *backupHarness) lastRun(t *testing.T) (status, errorCode string) {
	t.Helper()
	var statusNull, codeNull sql.NullString
	err := h.db.QueryRow(
		"SELECT status, error_code FROM backup_runs ORDER BY rowid DESC LIMIT 1",
	).Scan(&statusNull, &codeNull)
	if err != nil {
		t.Fatal(err)
	}
	return statusNull.String, codeNull.String
}

// seedWriterUser inserts one synthetic user row so the concurrent writer can
// satisfy the vault_items foreign key.
func (h *backupHarness) seedWriterUser(t *testing.T) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := h.db.Exec(
		`INSERT INTO users (id, username_norm, username_display, role, status, must_change_password, password_hash, created_at, updated_at)
		 VALUES ('writer-user', 'writer-user', 'writer-user', 'admin', 'active', 0, 'synthetic-test-hash', ?, ?)`,
		now, now,
	)
	if err != nil {
		t.Fatal(err)
	}
}

// seedBatchWriter inserts batchSize rows per transaction until stop is
// closed, emulating continuous concurrent writes during the snapshot.
func (h *backupHarness) seedBatchWriter(t *testing.T, batchSize int, stop <-chan struct{}) {
	t.Helper()
	h.seedWriterUser(t)
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			tx, err := h.db.Begin()
			if err != nil {
				return
			}
			now := time.Now().UTC().Format(time.RFC3339Nano)
			blob := randomBlob(32)
			for i := 0; i < batchSize; i++ {
				if _, err = tx.Exec(
					`INSERT INTO vault_items (id, vault_scope, owner_user_id, item_type, favorite,
					   payload_version, nonce, ciphertext, revision, created_at, updated_at)
					 VALUES (?, 'personal', 'writer-user', 'secure_note', 0, 99, ?, ?, 1, ?, ?)`,
					randomHex(t), blob, blob, now, now,
				); err != nil {
					_ = tx.Rollback()
					return
				}
			}
			_ = tx.Commit()
		}
	}()
}

func randomHex(t *testing.T) string {
	t.Helper()
	b := randomBlob(16)
	return hex.EncodeToString(b)
}

func randomBlob(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func TestBackupLocalSuccessDuringConcurrentWrites(t *testing.T) {
	h := newBackupHarness(t)
	stop := make(chan struct{})
	h.seedBatchWriter(t, 10, stop)
	defer close(stop)
	time.Sleep(50 * time.Millisecond) // let the writer land some batches

	result, err := h.run(t, h.runner)
	if err != nil {
		t.Fatalf("backup run failed: %v", err)
	}
	if result.SizeBytes <= 0 || result.SHA256 == "" {
		t.Fatalf("run result incomplete: %+v", result)
	}

	// Published atomically under the final name; no staging leftovers.
	if names := listDirNames(t, h.localDir); len(names) != 1 || filepath.Ext(names[0]) != ".7z" {
		t.Fatalf("backup dir contents: %v", names)
	}
	published := filepath.Join(h.localDir, listDirNames(t, h.localDir)[0])
	raw, err := os.ReadFile(published)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != result.SHA256 {
		t.Fatal("published archive digest mismatch")
	}
	if int64(len(raw)) != result.SizeBytes {
		t.Fatalf("size mismatch: %d vs %d", len(raw), result.SizeBytes)
	}

	// Re-open the archive under the instance limits and verify everything.
	dest, entries, err := archive.ExtractLimited(context.Background(), published, "backup-passphrase-1", t.TempDir(), backup.InstanceLimits(1<<30))
	if err != nil {
		t.Fatalf("verify extract: %v", err)
	}
	defer os.RemoveAll(dest)
	manifestRaw, err := os.ReadFile(filepath.Join(dest, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest backup.Manifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.FormatVersion != backup.FormatVersion {
		t.Fatalf("format version %d", manifest.FormatVersion)
	}
	var liveSchema int64
	if err := h.db.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&liveSchema); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != liveSchema {
		t.Fatalf("schema version %d, want %d", manifest.SchemaVersion, liveSchema)
	}
	if manifest.InstanceID == "" || manifest.BackupID == "" {
		t.Fatal("manifest missing ids")
	}
	if _, err := time.Parse(time.RFC3339Nano, manifest.CreatedAt); err != nil {
		t.Fatalf("created_at: %v", err)
	}
	// Exactly the whitelisted file set: snapshot + master key + manifest.
	var files []string
	for _, e := range entries {
		if !e.IsDir {
			files = append(files, e.Path)
		}
	}
	if len(files) != 3 {
		t.Fatalf("archive files: %v", files)
	}
	if err := manifest.Check(); err != nil {
		t.Fatalf("manifest check: %v", err)
	}
	for _, f := range manifest.Files {
		raw, err := os.ReadFile(filepath.Join(dest, f.Path))
		if err != nil {
			t.Fatalf("archived file %s: %v", f.Path, err)
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != f.SHA256 || int64(len(raw)) != f.Size {
			t.Fatalf("digest/size mismatch for %s", f.Path)
		}
	}
	keyOut, err := os.ReadFile(filepath.Join(dest, "secrets/master_key"))
	if err != nil {
		t.Fatal(err)
	}
	if string(keyOut) != string(h.keyRaw) {
		t.Fatal("master key copy mismatch")
	}
	if result, err := sqlite.VerifySnapshot(filepath.Join(dest, "db/tiny-password.db")); err != nil || result != "ok" {
		t.Fatalf("snapshot integrity: %q err=%v", result, err)
	}

	// Transactional consistency: every 10-row batch is fully in or out of
	// the SNAPSHOT (the live DB keeps changing; the snapshot must not).
	snapDB, err := sql.Open(sqlite.DriverName, "file:"+filepath.Join(dest, "db/tiny-password.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer snapDB.Close()
	var writerCount int
	if err := snapDB.QueryRow("SELECT COUNT(*) FROM vault_items WHERE payload_version = 99").Scan(&writerCount); err != nil {
		t.Fatal(err)
	}
	if writerCount == 0 || writerCount%10 != 0 {
		t.Fatalf("snapshot not transactionally consistent: %d rows", writerCount)
	}

	// The run record is a succeeded row with size and digest.
	if status, code := h.lastRun(t); status != "succeeded" || code != "" {
		t.Fatalf("last run: %s/%s", status, code)
	}
	// The restricted work dir is empty after the run.
	if empty, err := dirEmpty(h.workDir); err != nil || !empty {
		t.Fatalf("work dir not empty: %v %v", empty, err)
	}
}

func TestBackupLocalWrongPassphraseRejected(t *testing.T) {
	h := newBackupHarness(t)
	_, err := h.runner.Run(context.Background(), backup.RunInput{
		Target: backup.TargetLocal, Trigger: backup.TriggerManual,
		Passphrase: "", LocalDir: h.localDir,
	})
	if !errors.Is(err, backup.ErrPassphraseInvalid) {
		t.Fatalf("want ErrPassphraseInvalid, got %v", err)
	}
	if status, code := h.lastRun(t); status != "failed" || code != "BACKUP_PASSPHRASE_INVALID" {
		t.Fatalf("run row: %s/%s", status, code)
	}
	assertNoBackupsAndCleanWorkdir(t, h)
}

func TestBackupLocalCorruptArchiveRejected(t *testing.T) {
	h := newBackupHarness(t)
	// First run succeeds and publishes a good backup.
	if _, err := h.run(t, h.runner); err != nil {
		t.Fatal(err)
	}
	before := listDirNames(t, h.localDir)

	// Second run corrupts the staged archive before verification.
	corrupt := func(_ context.Context, path string) error {
		return os.WriteFile(path, []byte("not an archive at all"), 0o600)
	}
	broken := h.newRunner(t, backup.Hooks{AfterArchiveCreated: corrupt})
	if _, err := h.run(t, broken); err == nil {
		t.Fatal("corrupt archive accepted")
	}
	if status, code := h.lastRun(t); status != "failed" || code != "BACKUP_VERIFY_FAILED" {
		t.Fatalf("run row: %s/%s", status, code)
	}
	// The previous good backup is still in place; nothing new published.
	if got := listDirNames(t, h.localDir); len(got) != len(before) {
		t.Fatalf("backup dir changed: %v", got)
	}
	if empty, err := dirEmpty(h.workDir); err != nil || !empty {
		t.Fatalf("work dir not empty: %v %v", empty, err)
	}
}

func TestBackupLocalCancelCleansUp(t *testing.T) {
	h := newBackupHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	blocked := make(chan chan struct{})
	hookEntered := make(chan struct{})
	hooks := backup.Hooks{
		AfterArchiveCreated: func(hctx context.Context, _ string) error {
			close(hookEntered)
			ack := make(chan struct{})
			blocked <- ack
			<-ack
			<-hctx.Done() // release only on cancellation
			return hctx.Err()
		},
	}
	runner := h.newRunner(t, hooks)
	errCh := make(chan error, 1)
	go func() {
		_, err := runner.Run(ctx, backup.RunInput{
			Target: backup.TargetLocal, Trigger: backup.TriggerManual,
			Passphrase: "backup-passphrase-1", LocalDir: h.localDir,
		})
		errCh <- err
	}()
	select {
	case <-hookEntered:
	case err := <-errCh:
		t.Fatalf("run failed before the hook: %v", err)
	case <-time.After(60 * time.Second):
		t.Fatal("hook never entered")
	}
	cancel()
	ack := <-blocked
	close(ack)
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if status, code := h.lastRun(t); status != "failed" || code != "BACKUP_CANCELED" {
		t.Fatalf("run row: %s/%s", status, code)
	}
	assertNoBackupsAndCleanWorkdir(t, h)
}

func TestBackupLocalMutualExclusion(t *testing.T) {
	h := newBackupHarness(t)
	release := make(chan chan struct{})
	entered := make(chan chan struct{})
	hooks := backup.Hooks{
		AfterArchiveCreated: func(_ context.Context, _ string) error {
			ack := make(chan struct{})
			entered <- ack
			<-ack
			<-release
			return nil
		},
	}
	first := h.newRunner(t, hooks)
	done := make(chan error, 1)
	go func() {
		_, err := first.Run(context.Background(), backup.RunInput{
			Target: backup.TargetLocal, Trigger: backup.TriggerManual,
			Passphrase: "backup-passphrase-1", LocalDir: h.localDir,
		})
		done <- err
	}()
	ack := <-entered

	// A second run (same runner or a sibling instance) must see the busy mutex.
	if _, err := h.runner.Run(context.Background(), backup.RunInput{
		Target: backup.TargetLocal, Trigger: backup.TriggerManual,
		Passphrase: "backup-passphrase-1", LocalDir: h.localDir,
	}); !errors.Is(err, backup.ErrBusy) {
		t.Fatalf("want ErrBusy, got %v", err)
	}

	close(ack)
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first run: %v", err)
	}
	if status, _ := h.lastRun(t); status != "succeeded" {
		t.Fatalf("first run recorded as %s", status)
	}
}

func TestBackupLocalInsufficientSpace(t *testing.T) {
	h := newBackupHarness(t)
	hooks := backup.Hooks{
		SpaceCheck: func(string, int64) error {
			return backup.ErrSpace
		},
	}
	runner := h.newRunner(t, hooks)
	if _, err := h.run(t, runner); !errors.Is(err, backup.ErrSpace) {
		t.Fatalf("want ErrSpace, got %v", err)
	}
	if status, code := h.lastRun(t); status != "failed" || code != "BACKUP_INSUFFICIENT_SPACE" {
		t.Fatalf("run row: %s/%s", status, code)
	}
	assertNoBackupsAndCleanWorkdir(t, h)
}

func assertNoBackupsAndCleanWorkdir(t *testing.T, h *backupHarness) {
	t.Helper()
	if names := listDirNames(t, h.localDir); len(names) != 0 {
		t.Fatalf("unexpected backups: %v", names)
	}
	if empty, err := dirEmpty(h.workDir); err != nil || !empty {
		t.Fatalf("work dir not empty: %v %v", empty, err)
	}
}

func listDirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func dirEmpty(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, err
	}
	return len(entries) == 0, nil
}

// The runner must refuse obvious misconfiguration before doing any work.
func TestBackupRunnerOptionsValidation(t *testing.T) {
	db := openMigratedDB(t)
	if _, err := backup.NewRunner(backup.Options{DB: db, WorkDir: t.TempDir(), MasterKeyRaw: make([]byte, 16)}); err == nil {
		t.Fatal("short master key accepted")
	}
	if _, err := backup.NewRunner(backup.Options{DB: db, WorkDir: "", MasterKeyRaw: make([]byte, 32)}); err == nil {
		t.Fatal("missing work dir accepted")
	}
}
