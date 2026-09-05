package sqlite

import (
	"database/sql"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

const migrationsTable = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    applied_at TEXT NOT NULL
);`

// Migrate applies all pending *.sql files from fsys in lexical order inside
// individual transactions. Each migration is recorded in schema_migrations;
// a failing migration is rolled back completely and aborts the run.
func Migrate(db *sql.DB, fsys fs.FS) error {
	if _, err := db.Exec(migrationsTable); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	names, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(names)

	applied, err := appliedVersions(db)
	if err != nil {
		return err
	}

	for _, name := range names {
		version, err := versionFromName(name)
		if err != nil {
			return err
		}
		if applied[version] {
			continue
		}
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if err := applyMigration(db, version, name, string(body)); err != nil {
			return fmt.Errorf("migration %s failed: %w", name, err)
		}
	}
	return nil
}

func applyMigration(db *sql.DB, version int64, name, body string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // read-only use

	// SQLite's Exec runs multiple statements; a failure leaves the whole
	// transaction to be rolled back by the deferred Rollback.
	if _, err := tx.Exec(body); err != nil {
		return err
	}
	_, err = tx.Exec(
		"INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, strftime('%Y-%m-%dT%H:%M:%fZ','now'))",
		version, name,
	)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func appliedVersions(db *sql.DB) (map[int64]bool, error) {
	rows, err := db.Query("SELECT version FROM schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := map[int64]bool{}
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

func versionFromName(name string) (int64, error) {
	base := strings.TrimSuffix(name, ".sql")
	var version int64
	if _, err := fmt.Sscanf(base, "%d", &version); err != nil {
		return 0, fmt.Errorf("migration file %q must start with a numeric version", name)
	}
	return version, nil
}

// SchemaVersion returns the highest applied migration version, or 0.
func SchemaVersion(db *sql.DB) (int64, error) {
	var v sql.NullInt64
	if err := db.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&v); err != nil {
		return 0, err
	}
	return v.Int64, nil
}
