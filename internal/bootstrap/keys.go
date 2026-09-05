package bootstrap

import (
	"database/sql"
	"errors"

	"github.com/tiny-password/tiny-password/internal/platform/crypto"
)

// systemStateMasterKeyMarker is the system_state row holding the master key
// verification marker, written during one-time initialization (T05).
const systemStateMasterKeyMarker = "master_key_marker"

// ErrMasterKeyMismatch reports that the mounted master key does not match the
// marker recorded in the database.
var ErrMasterKeyMismatch = errors.New("mounted master key does not match this instance")

// ErrMasterKeyUnavailable prevents initialization without a loaded secret.
var ErrMasterKeyUnavailable = errors.New("master key unavailable")

// MasterKeyCheck returns a /readyz validator for the master key. A key
// loading failure (missing secret, wrong size, permissions) fails readiness;
// once a marker exists in system_state, a key change also fails readiness.
// No replacement key is ever generated here (decision D03/design §10.3).
func MasterKeyCheck(db *sql.DB, key *crypto.MasterKey, loadErr error) func() error {
	return func() error {
		if loadErr != nil {
			return loadErr
		}
		if key == nil {
			return ErrMasterKeyUnavailable
		}
		var marker sql.NullString
		err := db.QueryRow(
			"SELECT value FROM system_state WHERE key = ?", systemStateMasterKeyMarker,
		).Scan(&marker)
		if errors.Is(err, sql.ErrNoRows) {
			initialized, err := isInitialized(db)
			if err != nil {
				return err
			}
			if initialized {
				return ErrMasterKeyMismatch
			}
			// Only an empty instance may lack the marker.
			return nil
		}
		if err != nil {
			return err
		}
		if !key.VerifyMarker(marker.String) {
			return ErrMasterKeyMismatch
		}
		return nil
	}
}

// MasterKeyMarkerName exposes the system_state key for the bootstrap service.
func MasterKeyMarkerName() string { return systemStateMasterKeyMarker }
