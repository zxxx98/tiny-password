// Package sqlite owns the database connection pool, migrations, consistent
// snapshots, and integrity checks. It is the only place that talks to SQLite.
package sqlite

import (
	"database/sql"
	"fmt"
	"strings"
)

// DriverName is the database/sql driver registered by modernc.org/sqlite.
const DriverName = "sqlite"

const busyTimeoutMs = 5000

// DB wraps the connection pool with Tiny Password runtime guarantees:
// WAL journaling, per-connection foreign keys, and a 5 s busy timeout.
type DB struct {
	*sql.DB
	path string
}

// Open opens (creating if needed) the database at path with the runtime
// pragmas required by design §9.3.
func Open(path string) (*DB, error) {
	dsn := fmt.Sprintf(
		"file:%s?_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)",
		path, busyTimeoutMs,
	)
	pool, err := sql.Open(DriverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	pool.SetMaxOpenConns(10)
	db := &DB{DB: pool, path: path}
	if err := db.verifyPragmas(); err != nil {
		_ = pool.Close()
		return nil, err
	}
	return db, nil
}

func (d *DB) verifyPragmas() error {
	var journalMode, foreignKeys string
	if err := d.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		return fmt.Errorf("read journal_mode: %w", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		return fmt.Errorf("journal_mode = %s, want wal", journalMode)
	}
	if err := d.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("read foreign_keys: %w", err)
	}
	if foreignKeys != "1" {
		return fmt.Errorf("foreign_keys = %s, want 1", foreignKeys)
	}
	return nil
}

// Path returns the database file path.
func (d *DB) Path() string { return d.path }

// IsBusy reports whether err is a SQLITE_BUSY/SQLITE_LOCKED condition, which
// callers may retry after the busy timeout.
func IsBusy(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "SQLITE_BUSY") ||
		strings.Contains(msg, "SQLITE_LOCKED") ||
		strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "database table is locked")
}

// Checkpoint runs a passive WAL checkpoint. Intended for the low-peak
// maintenance scheduler, never inside request handling.
func (d *DB) Checkpoint() error {
	var busy, log, checkpointed int
	if err := d.QueryRow("PRAGMA wal_checkpoint(PASSIVE)").Scan(&busy, &log, &checkpointed); err != nil {
		return fmt.Errorf("wal checkpoint: %w", err)
	}
	return nil
}

// IntegrityCheck runs PRAGMA integrity_check and returns its result.
func (d *DB) IntegrityCheck() (string, error) {
	var result string
	if err := d.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return "", fmt.Errorf("integrity_check: %w", err)
	}
	return result, nil
}
