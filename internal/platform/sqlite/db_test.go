package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	_ "embed"
)

//go:embed testdata/0001_basic.sql
var basicMigration string

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func basicFS() fs.FS {
	return fstest.MapFS{
		"0001_basic.sql": &fstest.MapFile{Data: []byte(basicMigration)},
	}
}

func TestMigrateAppliesAndIsIdempotent(t *testing.T) {
	db := openTestDB(t)
	if err := Migrate(db.DB, basicFS()); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if err := Migrate(db.DB, basicFS()); err != nil {
		t.Fatalf("second migrate must be a no-op: %v", err)
	}
	version, err := SchemaVersion(db.DB)
	if err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("schema version = %d, want 1", version)
	}
	if _, err := db.Exec("INSERT INTO parents (id) VALUES (5)"); err != nil {
		t.Fatalf("insert parent: %v", err)
	}
	if _, err := db.Exec("INSERT INTO things (parent_id, name) VALUES (5, 'x')"); err != nil {
		t.Fatalf("insert into migrated table: %v", err)
	}
}

func TestMigrateRollsBackFailedMigration(t *testing.T) {
	db := openTestDB(t)
	if err := Migrate(db.DB, basicFS()); err != nil {
		t.Fatalf("baseline migrate: %v", err)
	}

	broken := fstest.MapFS{
		"0002_broken.sql": &fstest.MapFile{Data: []byte(
			"CREATE TABLE ok_table (id INTEGER);\n" +
				"CREATE TABLE syntax error here;\n",
		)},
	}
	err := Migrate(db.DB, broken)
	if err == nil {
		t.Fatal("expected broken migration to fail")
	}

	version, err := SchemaVersion(db.DB)
	if err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("schema version = %d after failed migration, want 1 (version not recorded)", version)
	}
	// The partial statement inside the failed migration must not persist.
	var name string
	err = db.QueryRow("SELECT name FROM sqlite_master WHERE name = 'ok_table'").Scan(&name)
	if err == nil {
		t.Fatal("table created before the failure survived; rollback incomplete")
	}
}

func TestForeignKeysEnforcedPerConnection(t *testing.T) {
	db := openTestDB(t)
	if err := Migrate(db.DB, basicFS()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO things (id, parent_id, name) VALUES (1, 999, 'orphan')"); err == nil {
		t.Fatal("foreign key violation not enforced")
	}
	if _, err := db.Exec("INSERT INTO parents (id) VALUES (1)"); err != nil {
		t.Fatalf("insert parent: %v", err)
	}
	if _, err := db.Exec("INSERT INTO things (id, parent_id, name) VALUES (1, 1, 'child')"); err != nil {
		t.Fatalf("insert valid child: %v", err)
	}
}

func TestBusyTimeoutSurfacesRetryableBusyError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "busy.db")
	writer, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if err := Migrate(writer.DB, basicFS()); err != nil {
		t.Fatal(err)
	}

	// Hold a write transaction on a second connection.
	holder, err := sql.Open(DriverName, fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)", path))
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	tx, err := holder.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO parents (id) VALUES (7)"); err != nil {
		t.Fatal(err)
	}

	// A short busy timeout makes the competing write fail fast with busy.
	competitor, err := sql.Open(DriverName,
		fmt.Sprintf("file:%s?_pragma=busy_timeout(50)", path))
	if err != nil {
		t.Fatal(err)
	}
	defer competitor.Close()
	_, err = competitor.Exec("INSERT INTO parents (id) VALUES (8)")
	if err == nil {
		t.Skip("write completed without contention; busy path not exercised")
	}
	if !IsBusy(err) {
		t.Fatalf("error %v not classified as busy", err)
	}
	_ = tx.Rollback()
}

func TestSnapshotDuringConcurrentWrites(t *testing.T) {
	db := openTestDB(t)
	if err := Migrate(db.DB, basicFS()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO parents (id) VALUES (1)"); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var writeErrs int
	var mu sync.Mutex
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 150; i++ {
				select {
				case <-stop:
					return
				default:
				}
				err := func() error {
					tx, err := db.Begin()
					if err != nil {
						return err
					}
					defer tx.Rollback() //nolint:errcheck
					if _, err := tx.Exec("INSERT INTO things (parent_id, name) VALUES (1, ?)", randomHex(t)); err != nil {
						return err
					}
					if _, err := tx.Exec("UPDATE counters SET n = n + 1 WHERE id = 1"); err != nil {
						return err
					}
					return tx.Commit()
				}()
				if err != nil {
					mu.Lock()
					writeErrs++
					mu.Unlock()
					return
				}
			}
		}()
	}
	time.Sleep(200 * time.Millisecond)

	snapPath := filepath.Join(t.TempDir(), "snap.db")
	if err := db.Snapshot(context.Background(), snapPath); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	close(stop)
	wg.Wait()
	if writeErrs != 0 {
		t.Fatalf("%d concurrent writes failed during snapshot", writeErrs)
	}

	result, err := VerifySnapshot(snapPath)
	if err != nil {
		t.Fatal(err)
	}
	if result != "ok" {
		t.Fatalf("snapshot integrity = %q, want ok", result)
	}

	snap, err := sql.Open(DriverName, "file:"+snapPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	// The invariant row and rows are updated in the same transaction, so any
	// committed snapshot must satisfy counters.n == COUNT(things).
	var total, count int
	if err := snap.QueryRow("SELECT n FROM counters WHERE id = 1").Scan(&total); err != nil {
		t.Fatal(err)
	}
	if err := snap.QueryRow("SELECT COUNT(*) FROM things").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if total != count {
		t.Fatalf("inconsistent snapshot: counters.n=%d rows=%d", total, count)
	}
}

func randomHex(t *testing.T) string {
	t.Helper()
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b[:])
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
