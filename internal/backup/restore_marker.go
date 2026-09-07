package backup

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// restoreMarkerKey tags a restored candidate database in system_state with
// the backup it was built from. The post-switch check refuses to report
// success unless the live database carries exactly the recorded backup id:
// verification under the target key alone cannot distinguish the restored
// candidate from the previous instance when both decrypt under the same
// key, and an accidentally created empty database carries no marker at all.
const restoreMarkerKey = "restore_backup_id"

// stampRestoreMarker records backupID in the candidate's system_state. The
// candidate is offline (service stopped, data-dir lock held), so a plain
// upsert cannot race anything.
func stampRestoreMarker(db *sql.DB, backupID string) error {
	_, err := db.Exec(
		`INSERT INTO system_state (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		restoreMarkerKey, backupID, time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("stamp restore marker: %w", err)
	}
	return nil
}

// readRestoreMarker returns the recorded backup id; an empty string means
// the database carries no restore marker.
func readRestoreMarker(db *sql.DB) (string, error) {
	var value string
	err := db.QueryRow(`SELECT value FROM system_state WHERE key = ?`, restoreMarkerKey).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read restore marker: %w", err)
	}
	return value, nil
}
