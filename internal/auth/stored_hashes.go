package auth

import (
	"context"
	"database/sql"
	"fmt"
)

// StoredHashStats summarizes password hashes without running Argon2. Startup
// uses it to fail fast when an operator lowers the runtime budget below what
// existing accounts still need for their one-time migration login.
type StoredHashStats struct {
	Count        int
	NeedsRehash  int
	MaxMemoryKiB uint32
}

// InspectStoredHashes parses every stored PHC string and verifies that the
// current runtime policy can still authenticate it. No password work runs.
func InspectStoredHashes(ctx context.Context, db *sql.DB, hasher *PasswordHasher) (StoredHashStats, error) {
	var stats StoredHashStats
	if db == nil || hasher == nil {
		return stats, fmt.Errorf("argon2 stored-hash inspection requires database and hasher")
	}
	rows, err := db.QueryContext(ctx, "SELECT password_hash FROM users")
	if err != nil {
		return stats, err
	}
	defer rows.Close()

	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			return stats, err
		}
		params, err := HashParamsOf(encoded)
		if err != nil {
			return stats, fmt.Errorf("stored password hash is malformed: %w", err)
		}
		if err := hasher.validateVerifyParams(params); err != nil {
			p := hasher.Policy()
			return stats, fmt.Errorf(
				"stored password hash requires m=%d KiB,t=%d,p=%d but runtime limits are memory=%d KiB, iterations=%d, parallelism=%d, budget=%d KiB",
				params.MemoryKiB, params.Iterations, params.Parallelism,
				p.VerifyMaxMemoryKiB, p.VerifyMaxIterations, p.VerifyMaxParallelism, p.MemoryBudgetKiB,
			)
		}
		stats.Count++
		if params.MemoryKiB > stats.MaxMemoryKiB {
			stats.MaxMemoryKiB = params.MemoryKiB
		}
		if hasher.NeedsRehash(encoded) {
			stats.NeedsRehash++
		}
	}
	if err := rows.Err(); err != nil {
		return stats, err
	}
	return stats, nil
}
