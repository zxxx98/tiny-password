package auth

import (
	"context"
	"database/sql"
	"time"
)

// CleanupExpiredSessions deletes sessions that can never authenticate
// again: revoked, absolute-expired, or idle-expired rows. It is the
// maintenance entry the scheduler calls (T23); running it any number of
// times is safe. The count of removed rows is returned.
func CleanupExpiredSessions(ctx context.Context, db *sql.DB, now time.Time) (int64, error) {
	at := timestamp(now)
	res, err := db.ExecContext(ctx,
		`DELETE FROM sessions WHERE revoked_at IS NOT NULL OR absolute_expires_at <= ? OR idle_expires_at <= ?`,
		at, at,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CleanupLoginAttempts deletes rate-limit aggregation rows older than the
// given cutoff. Rows are digests only; the rolling window never spans more
// than minutes, so a periodic sweep keeps the table bounded.
func CleanupLoginAttempts(ctx context.Context, db *sql.DB, cutoff time.Time) (int64, error) {
	res, err := db.ExecContext(ctx,
		`DELETE FROM login_attempts WHERE created_at <= ?`, timestamp(cutoff),
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
