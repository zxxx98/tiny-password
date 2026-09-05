// Package fixtures builds synthetic vault data for performance baselines and
// load tests (plan T13). Everything here is deterministic and clearly
// synthetic; nothing is production data.
package fixtures

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/tiny-password/tiny-password/internal/platform/crypto"
	"github.com/tiny-password/tiny-password/internal/vault"
)

// SeedLoginItems inserts count synthetic login items into ownerID's personal
// vault, bypassing policy and audit on purpose (raw seed path). The item at
// needleIndex carries needle in its title so a search has exactly one
// expected hit. Returns the needle item's id.
func SeedLoginItems(ctx context.Context, db *sql.DB, key *crypto.MasterKey, ownerID string, count, needleIndex int, needle string) (string, error) {
	if needleIndex < 0 || needleIndex >= count {
		return "", fmt.Errorf("needleIndex %d out of range [0,%d)", needleIndex, count)
	}
	const batch = 1000
	needleID := ""
	for start := 0; start < count; start += batch {
		end := start + batch
		if end > count {
			end = count
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return "", err
		}
		for i := start; i < end; i++ {
			name := fmt.Sprintf("Item %05d", i)
			if i == needleIndex {
				name = needle
			}
			payload := &vault.LoginPayload{
				Name:     name,
				Username: fmt.Sprintf("user%05d", i),
				Password: fmt.Sprintf("pw-%05d-abcdefghijkl", i),
				URLs:     []string{fmt.Sprintf("https://site%05d.example", i)},
				Notes:    "synthetic load-test record",
			}
			id, err := vault.InsertSynthetic(ctx, tx, key, vault.SyntheticItem{
				ItemType: vault.TypeLogin,
				Scope:    string(vault.ScopePersonal),
				OwnerID:  ownerID,
				Tags:     []string{[]string{"tag-a", "tag-b", "tag-c"}[i%3]},
				Favorite: i%7 == 0,
				Payload:  payload,
			})
			if err != nil {
				_ = tx.Rollback()
				return "", fmt.Errorf("seed item %d: %w", i, err)
			}
			if i == needleIndex {
				needleID = id
			}
		}
		if err := tx.Commit(); err != nil {
			return "", err
		}
	}
	return needleID, nil
}
