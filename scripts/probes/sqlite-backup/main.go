// T01 probe: verify that a consistent online snapshot of a live SQLite
// database can be produced while concurrent transactions are committing.
//
// Two snapshot methods are exercised:
//  1. the SQLite Online Backup API exposed by modernc.org/sqlite v1.58.0
//     through the unexported *conn type, reached via an interface assertion;
//  2. the VACUUM INTO statement.
//
// Consistency proof: every writer transaction inserts a row and updates an
// invariant row (total/checksum) in the same transaction. A snapshot taken at
// any committed point must therefore satisfy:
//
//	total    == COUNT(entries)
//	checksum == SUM(seq)
//
// The probe passes only when both snapshots pass integrity_check, satisfy the
// invariant, writers report no hard failures, and the snapshots are usable as
// standalone files (no WAL sidecar required).
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sqlite "modernc.org/sqlite"
)

const (
	writers        = 8
	txPerWriter    = 250
	busyTimeoutMs  = 5000
	backupStepPage = 32
)

// backuper matches the signature of (*sqlite.conn).NewBackup; the concrete
// type is unexported but the method name is exported, so an interface
// assertion from driver.Conn works.
type backuper interface {
	NewBackup(dstUri string) (*sqlite.Backup, error)
}

var writeErrors atomic.Int64
var busyErrors atomic.Int64

func fail(format string, args ...any) {
	fmt.Printf("FAIL  "+format+"\n", args...)
	os.Exit(1)
}

func pass(format string, args ...any) {
	fmt.Printf("PASS  "+format+"\n", args...)
}

func openDB(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)", path, busyTimeoutMs)
	return sql.Open("sqlite", dsn)
}

func schema(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS entries (seq INTEGER PRIMARY KEY AUTOINCREMENT, payload TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS invariant (id INTEGER PRIMARY KEY CHECK (id = 1), total INTEGER NOT NULL, checksum INTEGER NOT NULL)`,
		`INSERT OR IGNORE INTO invariant (id, total, checksum) VALUES (1, 0, 0)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

func writer(id int, db *sql.DB, stop <-chan struct{}, done func()) {
	defer done()
	payload := strings.Repeat("v", 200)
	for i := 0; i < txPerWriter; i++ {
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
			defer tx.Rollback()
			if _, err := tx.Exec(`INSERT INTO entries (payload) VALUES (?)`, payload+fmt.Sprintf(":%d:%d", id, i)); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE invariant SET total = total + 1, checksum = checksum + (SELECT last_insert_rowid()) WHERE id = 1`); err != nil {
				return err
			}
			return tx.Commit()
		}()
		if err != nil {
			if strings.Contains(err.Error(), "SQLITE_BUSY") {
				busyErrors.Add(1)
				i-- // retry this transaction
				time.Sleep(5 * time.Millisecond)
				continue
			}
			writeErrors.Add(1)
			return
		}
	}
}

func snapshotViaBackupAPI(db *sql.DB, dst string) (time.Duration, error) {
	conn, err := db.Conn(context.Background())
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	start := time.Now()
	err = conn.Raw(func(driverConn any) error {
		b, ok := driverConn.(backuper)
		if !ok {
			return fmt.Errorf("driver connection does not expose NewBackup")
		}
		bk, err := b.NewBackup("file:" + dst)
		if err != nil {
			return err
		}
		for {
			more, err := bk.Step(backupStepPage)
			if err != nil {
				bk.Finish()
				return fmt.Errorf("backup step: %w", err)
			}
			if !more {
				break
			}
		}
		return bk.Finish()
	})
	return time.Since(start), err
}

func snapshotViaVacuum(db *sql.DB, dst string) (time.Duration, error) {
	start := time.Now()
	_, err := db.Exec(`VACUUM INTO ?`, dst)
	return time.Since(start), err
}

// normalizeSnapshot opens a snapshot read-write and converts it out of WAL
// mode, guaranteeing the archived file never needs a -wal/-shm sidecar.
func normalizeSnapshot(path string) error {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=busy_timeout(%d)", path, busyTimeoutMs))
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA journal_mode=DELETE`); err != nil {
		return err
	}
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		return err
	}
	if mode != "delete" {
		return fmt.Errorf("journal mode is %q, want delete", mode)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); err == nil {
			return fmt.Errorf("sidecar %s%s still present", path, suffix)
		}
	}
	return nil
}

func verifySnapshot(path, label string) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(%d)", path, busyTimeoutMs))
	if err != nil {
		fail("%s: open snapshot: %v", label, err)
	}
	defer db.Close()
	var check string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&check); err != nil {
		fail("%s: integrity_check: %v", label, err)
	}
	if check != "ok" {
		fail("%s: integrity_check returned %q", label, check)
	}
	pass("%s: integrity_check = ok", label)

	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		fail("%s: journal_mode: %v", label, err)
	}
	if mode == "wal" {
		fail("%s: snapshot depends on WAL sidecar (journal_mode=wal)", label)
	} else {
		pass("%s: standalone file (journal_mode=%s)", label, mode)
	}

	var invariantTotal, invariantChecksum int
	if err := db.QueryRow(`SELECT total, checksum FROM invariant WHERE id = 1`).Scan(&invariantTotal, &invariantChecksum); err != nil {
		fail("%s: read invariant: %v", label, err)
	}
	var count int
	var checksum any
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(seq), 0) FROM entries`).Scan(&count, &checksum); err != nil {
		fail("%s: aggregate entries: %v", label, err)
	}
	if count != invariantTotal || int(checksum.(int64)) != invariantChecksum {
		fail("%s: inconsistent snapshot: invariant(total=%d checksum=%d) vs actual(rows=%d checksum=%d)", label, invariantTotal, invariantChecksum, count, checksum)
	}
	pass("%s: transactional invariant holds (rows=%d)", label, count)
}

func main() {
	dir, err := os.MkdirTemp("", "tp-sqlite-probe-")
	if err != nil {
		fail("temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	db, err := openDB(filepath.Join(dir, "src.db"))
	if err != nil {
		fail("open source db: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(writers + 2)

	if err := schema(db); err != nil {
		fail("schema: %v", err)
	}

	var sqliteVersion string
	if err := db.QueryRow(`SELECT sqlite_version()`).Scan(&sqliteVersion); err != nil {
		fail("sqlite_version: %v", err)
	}
	fmt.Printf("  driver=modernc.org/sqlite v1.58.0 sqlite_version=%s\n", sqliteVersion)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go writer(w, db, stop, wg.Done)
	}
	// Give writers time to build up contention before snapshotting.
	time.Sleep(300 * time.Millisecond)

	bkTime, err := snapshotViaBackupAPI(db, filepath.Join(dir, "snap_backup.db"))
	if err != nil {
		fail("backup API snapshot: %v", err)
	}
	pass("online backup API snapshot created in %s while writers were active", bkTime)

	// The backup API copies the source header verbatim, so the snapshot
	// inherits the WAL flag. Production normalizes the snapshot to a
	// standalone rollback-journal file before archiving; exercise that step.
	if err := normalizeSnapshot(filepath.Join(dir, "snap_backup.db")); err != nil {
		fail("normalize backup snapshot: %v", err)
	}
	pass("backup API snapshot normalized to journal_mode=DELETE")

	vacTime, err := snapshotViaVacuum(db, filepath.Join(dir, "snap_vacuum.db"))
	if err != nil {
		fail("VACUUM INTO snapshot: %v", err)
	}
	pass("VACUUM INTO snapshot created in %s while writers were active", vacTime)

	close(stop)
	wg.Wait()

	if writeErrors.Load() > 0 {
		fail("writers hit %d hard errors", writeErrors.Load())
	}
	pass("writers completed with 0 hard errors (SQLITE_BUSY retries: %d)", busyErrors.Load())

	verifySnapshot(filepath.Join(dir, "snap_backup.db"), "backup-api")
	verifySnapshot(filepath.Join(dir, "snap_vacuum.db"), "vacuum-into")

	var srcCheck string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&srcCheck); err != nil || srcCheck != "ok" {
		fail("source integrity_check: %q (%v)", srcCheck, err)
	}
	pass("source db integrity_check = ok after run")

	var finalRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM entries`).Scan(&finalRows); err != nil {
		fail("final count: %v", err)
	}
	fmt.Printf("\nprobe complete: %d committed rows at end\n", finalRows)
}
