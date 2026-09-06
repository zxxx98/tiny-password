package backup

import (
	"database/sql"
	"fmt"

	"github.com/tiny-password/tiny-password/internal/platform/crypto"
	"github.com/tiny-password/tiny-password/internal/vault"
)

// RekeyReport summarizes one re-encryption pass.
type RekeyReport struct {
	Items       int
	Versions    int
	AuthCleared int64
}

// RekeyCandidate decrypts every vault_items and item_versions payload with
// the archive's source key and re-encrypts it under the target key inside
// one transaction (design §11.5 step 6). IDs, scopes, owners, and revisions
// are preserved; only nonce, ciphertext and nothing else change — the AAD
// binds the same row identity on both sides. The short-lived authentication
// state of the restored instance (sessions, rate-limit aggregation, pending
// idempotency claims) is cleared.
//
// The candidate database must be closed (or at least quiescent) by the
// caller afterwards; this function only opens the single connection it
// needs and commits before returning.
func RekeyCandidate(dbPath string, sourceKey, targetKey *crypto.MasterKey) (RekeyReport, error) {
	var report RekeyReport
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		return report, fmt.Errorf("open candidate: %w", err)
	}
	defer db.Close()

	// Row identity for the AAD: item id, scope, subject, revision.
	type identity struct {
		scope   string
		subject string
	}
	items := map[string]identity{}
	rows, err := db.Query(
		`SELECT id, vault_scope, COALESCE(owner_user_id, ''), COALESCE(created_by_user_id, '')
		 FROM vault_items`,
	)
	if err != nil {
		return report, fmt.Errorf("list items: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, scope, owner, creator string
		if err := rows.Scan(&id, &scope, &owner, &creator); err != nil {
			return report, err
		}
		subject := owner
		if subject == "" {
			subject = creator
		}
		items[id] = identity{scope: scope, subject: subject}
	}
	if err := rows.Err(); err != nil {
		return report, err
	}

	tx, err := db.Begin()
	if err != nil {
		return report, fmt.Errorf("begin rekey: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // read-only rollback use

	itemRows, err := tx.Query(
		`SELECT id, payload_version, nonce, ciphertext, revision FROM vault_items`)
	if err != nil {
		return report, fmt.Errorf("read items: %w", err)
	}
	type update struct {
		version uint16
		nonce   []byte
		ct      []byte
		rev     uint64
	}
	updates := map[string]update{}
	for itemRows.Next() {
		var id string
		var version uint16
		var nonce, ct []byte
		var revision uint64
		if err := itemRows.Scan(&id, &version, &nonce, &ct, &revision); err != nil {
			itemRows.Close()
			return report, fmt.Errorf("scan item: %w", err)
		}
		updates[id] = update{version: version, nonce: nonce, ct: ct, rev: revision}
	}
	itemRows.Close()
	if err := itemRows.Err(); err != nil {
		return report, err
	}
	for id, u := range updates {
		it, ok := items[id]
		if !ok {
			return report, fmt.Errorf("rekey: missing identity for item %s", id)
		}
		aad := vault.AADFor(id, it.scope, it.subject, "", u.version, u.rev)
		plain, err := sourceKey.DecryptColumns(u.version, u.nonce, u.ct, aad)
		if err != nil {
			return report, fmt.Errorf("rekey: decrypt item: %w", err)
		}
		enc, err := targetKey.EncryptEncoded(plain, aad)
		if err != nil {
			return report, fmt.Errorf("rekey: encrypt item: %w", err)
		}
		if err := rekeyItem(tx, id, u.version, enc); err != nil {
			return report, err
		}
		report.Items++
	}

	// History versions keep their own revision and identity.
	versionRows, err := tx.Query(
		`SELECT v.item_id, v.payload_version, v.nonce, v.ciphertext, v.revision
		 FROM item_versions v`)
	if err != nil {
		return report, fmt.Errorf("read versions: %w", err)
	}
	type versionUpdate struct {
		itemID  string
		rev     uint64
		version uint16
		enc     []byte
	}
	var versionUpdates []versionUpdate
	for versionRows.Next() {
		var itemID string
		var version uint16
		var nonce, ct []byte
		var revision uint64
		if err := versionRows.Scan(&itemID, &version, &nonce, &ct, &revision); err != nil {
			versionRows.Close()
			return report, fmt.Errorf("scan version: %w", err)
		}
		it, ok := items[itemID]
		if !ok {
			versionRows.Close()
			return report, fmt.Errorf("rekey: missing identity for item %s", itemID)
		}
		aad := vault.AADFor(itemID, it.scope, it.subject, "", version, revision)
		plain, err := sourceKey.DecryptColumns(version, nonce, ct, aad)
		if err != nil {
			versionRows.Close()
			return report, fmt.Errorf("rekey: decrypt version: %w", err)
		}
		enc, err := targetKey.EncryptEncoded(plain, aad)
		if err != nil {
			versionRows.Close()
			return report, fmt.Errorf("rekey: encrypt version: %w", err)
		}
		versionUpdates = append(versionUpdates, versionUpdate{itemID: itemID, rev: revision, version: version, enc: enc})
	}
	versionRows.Close()
	if err := versionRows.Err(); err != nil {
		return report, err
	}
	for _, u := range versionUpdates {
		if _, err := tx.Exec(
			`UPDATE item_versions SET payload_version=?, nonce=?, ciphertext=? WHERE item_id=? AND revision=?`,
			u.version, u.enc[2:2+24], u.enc[2+24:], u.itemID, u.rev,
		); err != nil {
			return report, fmt.Errorf("rekey: update version: %w", err)
		}
		report.Versions++
	}

	// Short-lived authentication state never survives a restore.
	res, err := tx.Exec(`DELETE FROM sessions`)
	if err != nil {
		return report, fmt.Errorf("rekey: clear sessions: %w", err)
	}
	n, _ := res.RowsAffected()
	report.AuthCleared += n
	if _, err := tx.Exec(`DELETE FROM login_attempts`); err != nil {
		return report, fmt.Errorf("rekey: clear login attempts: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM idempotency_keys`); err != nil {
		return report, fmt.Errorf("rekey: clear idempotency: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return report, fmt.Errorf("rekey: commit: %w", err)
	}
	return report, nil
}

// rekeyItem splits the encoded envelope into the storage columns.
func rekeyItem(tx *sql.Tx, id string, version uint16, enc []byte) error {
	if len(enc) < 2+24+16 {
		return fmt.Errorf("rekey: encoded payload too short")
	}
	_, err := tx.Exec(
		`UPDATE vault_items SET payload_version=?, nonce=?, ciphertext=? WHERE id=?`,
		version, enc[2:2+24], enc[2+24:], id,
	)
	if err != nil {
		return fmt.Errorf("rekey: update item: %w", err)
	}
	return nil
}
