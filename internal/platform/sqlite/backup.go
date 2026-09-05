package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	sqliteDriver "modernc.org/sqlite"
)

// backupAPI matches (*sqlite.conn).NewBackup. The concrete connection type is
// unexported, but the method name is exported, so an interface assertion from
// driver.Conn reaches it (validated by scripts/probes/sqlite-backup).
type backupAPI interface {
	NewBackup(dstUri string) (*sqliteDriver.Backup, error)
}

const snapshotStepPages = 32

// Snapshot creates a consistent copy of the database at dstPath using the
// SQLite Online Backup API while the source stays live. The snapshot is
// normalized out of WAL mode so the file is standalone (no -wal/-shm sidecar).
func (d *DB) Snapshot(ctx context.Context, dstPath string) error {
	conn, err := d.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection for snapshot: %w", err)
	}
	defer conn.Close()

	err = conn.Raw(func(driverConn any) error {
		backup, ok := driverConn.(backupAPI)
		if !ok {
			return fmt.Errorf("driver connection does not expose the backup API")
		}
		bk, err := backup.NewBackup("file:" + dstPath)
		if err != nil {
			return fmt.Errorf("init backup: %w", err)
		}
		for {
			more, err := bk.Step(snapshotStepPages)
			if err != nil {
				_ = bk.Finish()
				return fmt.Errorf("backup step: %w", err)
			}
			if !more {
				break
			}
		}
		return bk.Finish()
	})
	if err != nil {
		return err
	}
	return normalizeSnapshotFile(dstPath)
}

// normalizeSnapshotFile converts the snapshot out of WAL mode (the backup
// copies the source header verbatim) and verifies no sidecars remain.
func normalizeSnapshotFile(path string) error {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(%d)", path, busyTimeoutMs)
	db, err := sql.Open(DriverName, dsn)
	if err != nil {
		return fmt.Errorf("open snapshot for normalization: %w", err)
	}
	defer db.Close()

	if _, err := db.Exec("PRAGMA journal_mode=DELETE"); err != nil {
		return fmt.Errorf("normalize snapshot journal mode: %w", err)
	}
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		return fmt.Errorf("verify snapshot journal mode: %w", err)
	}
	if mode != "delete" {
		return fmt.Errorf("snapshot journal mode = %s, want delete", mode)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); err == nil {
			return fmt.Errorf("snapshot sidecar %s%s still present", path, suffix)
		}
	}
	return nil
}

// VerifySnapshot opens a snapshot read-only and runs integrity_check.
func VerifySnapshot(path string) (string, error) {
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(%d)", path, busyTimeoutMs)
	db, err := sql.Open(DriverName, dsn)
	if err != nil {
		return "", fmt.Errorf("open snapshot read-only: %w", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return "", fmt.Errorf("ping snapshot: %w", err)
	}
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return "", fmt.Errorf("snapshot integrity_check: %w", err)
	}
	return result, nil
}
