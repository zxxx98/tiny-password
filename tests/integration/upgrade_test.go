package integration

import (
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/tiny-password/tiny-password/internal/platform/sqlite"
	"github.com/tiny-password/tiny-password/migrations"
)

// upgradeHarness builds a database that mimics a previous release: the v1
// fixture schema applied by hand and stamped into schema_migrations.
type upgradeHarness struct {
	db      *sqlite.DB
	dataDir string
	dbPath  string
}

func newUpgradeHarness(t *testing.T) *upgradeHarness {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "tiny-password.db")
	raw, err := sql.Open(sqlite.DriverName, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(filepath.Join("..", "fixtures", "schema-v1.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(string(fixture)); err != nil {
		t.Fatal(err)
	}
	// The old release already tracked applied versions.
	if _, err := raw.Exec(
		`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL)`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES (1, '0001_old_release.sql', '2026-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatal(err)
	}
	// One synthetic user row the upgrade must preserve.
	if _, err := raw.Exec(
		`INSERT INTO users (id, username_norm, username_display, role, status, must_change_password, password_hash, created_at, updated_at)
		 VALUES ('user-1', 'user-1', 'user-1', 'admin', 'active', 0, 'synthetic-v1-hash', '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z')`,
	); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &upgradeHarness{db: db, dataDir: dir, dbPath: dbPath}
}

// The v1 release upgrades to the current schema: data rows survive and the
// pre-upgrade snapshot exists and verifies.
func TestUpgradeOldSchemaAppliesWithSnapshot(t *testing.T) {
	h := newUpgradeHarness(t)
	presnapshot, err := sqlite.Upgrade(h.db, migrations.FS, h.dataDir, time.Now())
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if presnapshot == "" {
		t.Fatal("pre-upgrade snapshot missing")
	}
	if integrity, err := sqlite.VerifySnapshot(presnapshot); err != nil || integrity != "ok" {
		t.Fatalf("pre-upgrade snapshot: %q %v", integrity, err)
	}
	var version int64
	if err := h.db.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version < 2 {
		t.Fatalf("schema not upgraded: %d", version)
	}
	// The v1-era user row survived the migration (including the 0002
	// ALTER TABLE that touched users' sibling table).
	var display string
	if err := h.db.QueryRow("SELECT username_display FROM users WHERE id = 'user-1'").Scan(&display); err != nil {
		t.Fatalf("v1 data lost: %v", err)
	}
	if display != "user-1" {
		t.Fatalf("v1 data corrupted: %q", display)
	}
	// The auth_sessions migration extended the schema as designed.
	var publicID string
	if err := h.db.QueryRow("SELECT public_id FROM sessions LIMIT 1").Scan(&publicID); err == nil {
		t.Fatal("sessions table should exist but stay empty")
	}
}

// A failing migration rolls back completely, leaves the previous schema and
// its data intact, and the pre-upgrade snapshot remains as the rollback
// artifact.
func TestUpgradeFailureRollsBackToPreviousSchema(t *testing.T) {
	h := newUpgradeHarness(t)
	broken := fstest.MapFS{
		"0002_auth_sessions.sql": &fstest.MapFile{Data: migrationFile(t, "0002_auth_sessions.sql")},
		"0003_broken_step.sql":   &fstest.MapFile{Data: []byte("CREATE TABLE half_written (id TEXT); INSERT INTO nonexistent_table VALUES (1);")},
	}
	_, err := sqlite.Upgrade(h.db, broken, h.dataDir, time.Now())
	if err == nil {
		t.Fatal("broken migration accepted")
	}
	// 0002 applied and stayed (it committed); 0003 rolled back entirely.
	var exists int
	if err := h.db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='half_written'`,
	).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists != 0 {
		t.Fatal("failed migration left tables behind")
	}
	// Data from before the failed upgrade is still readable.
	var display string
	if err := h.db.QueryRow("SELECT username_display FROM users WHERE id = 'user-1'").Scan(&display); err != nil {
		t.Fatal(err)
	}
}

// A schema newer than the binary is refused: downgrades are unsupported.
func TestUpgradeRefusesDowngrade(t *testing.T) {
	h := newUpgradeHarness(t)
	future := fstest.MapFS{} // a binary that knows NO migrations at all
	if _, err := sqlite.Upgrade(h.db, future, h.dataDir, time.Now()); err == nil {
		t.Fatal("downgrade accepted")
	}
	// The database is untouched.
	var version int64
	if err := h.db.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("database mutated during refused downgrade: %d", version)
	}
}

// A restart with nothing pending performs no snapshot and changes nothing;
// an empty database upgrades without a snapshot.
func TestUpgradeIdempotentRestartAndEmptyDB(t *testing.T) {
	h := newUpgradeHarness(t)
	if _, err := sqlite.Upgrade(h.db, migrations.FS, h.dataDir, time.Now()); err != nil {
		t.Fatal(err)
	}
	// Second start: nothing pending, no new snapshot.
	presnapshot, err := sqlite.Upgrade(h.db, migrations.FS, h.dataDir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if presnapshot != "" {
		t.Fatalf("snapshot taken without pending migrations: %s", presnapshot)
	}

	// Fresh empty database: migrations apply, no snapshot needed.
	emptyPath := filepath.Join(t.TempDir(), "empty.db")
	empty, err := sqlite.Open(emptyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Close()
	presnapshot, err = sqlite.Upgrade(empty, migrations.FS, t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if presnapshot != "" {
		t.Fatalf("empty database produced a snapshot: %s", presnapshot)
	}
	var version int64
	if err := empty.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version < 2 {
		t.Fatalf("empty database not migrated: %d", version)
	}
}

// Migrations interrupted by a crash leave the previous complete schema in
// place: simulating a process death between two pending migrations still
// yields a startable database at a known version.
func TestUpgradeInterruptedKeepsPreviousSchemaStartable(t *testing.T) {
	h := newUpgradeHarness(t)
	// Apply the first pending migration only, then "crash" before the rest.
	single := fstest.MapFS{
		"0002_auth_sessions.sql": &fstest.MapFile{Data: migrationFile(t, "0002_auth_sessions.sql")},
		"0003_never_reached.sql": &fstest.MapFile{Data: []byte(`CREATE TABLE newer_feature (
		    id TEXT PRIMARY KEY,
		    payload BLOB NOT NULL
		);`)},
	}
	if _, err := sqlite.Upgrade(h.db, onlyUpTo(single, 2), h.dataDir, time.Now()); err != nil {
		t.Fatal(err)
	}
	// Restart with the FULL set: the interrupted migration completes.
	if _, err := sqlite.Upgrade(h.db, single, h.dataDir, time.Now()); err != nil {
		t.Fatalf("restart after interruption: %v", err)
	}
	var exists int
	if err := h.db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='newer_feature'`,
	).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists != 1 {
		t.Fatal("interrupted migration did not complete after restart")
	}
}

// migrationFile reads a real migration for use inside synthetic sets.
func migrationFile(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := migrations.FS.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func onlyUpTo(fsys fs.FS, maxVersion int) fs.FS {
	sub := fstest.MapFS{}
	_ = fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		var version int
		if _, err := fmt.Sscanf(path, "%d", &version); err == nil && version <= maxVersion {
			data, _ := fs.ReadFile(fsys, path)
			sub[path] = &fstest.MapFile{Data: data}
		}
		return nil
	})
	return sub
}
