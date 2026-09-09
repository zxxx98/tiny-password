package vault

import (
	"context"
	"database/sql"
	"errors"
)

// Queryer abstracts *sql.DB and *sql.Tx so every statement can run inside
// the caller's transaction.
type Queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// itemRow is the full stored row, including the encrypted columns. Business
// fields never appear here — only policy metadata and ciphertext.
type itemRow struct {
	ID             string
	Scope          string
	OwnerID        sql.NullString
	CreatorID      sql.NullString
	ItemType       string
	Favorite       bool
	PayloadVersion uint16
	Nonce          []byte
	Ciphertext     []byte
	Revision       uint64
	CreatedAt      string
	UpdatedAt      string
	DeletedAt      sql.NullString
}

// itemColumns covers detail reads; list reads use metaColumns only.
const itemColumns = `id, vault_scope, owner_user_id, created_by_user_id, item_type, favorite,
	payload_version, nonce, ciphertext, revision, created_at, updated_at, deleted_at`

const metaColumns = `id, vault_scope, owner_user_id, created_by_user_id, item_type, favorite,
	revision, created_at, updated_at, deleted_at`

func scanItem(scanner interface{ Scan(...any) error }) (itemRow, error) {
	var r itemRow
	err := scanner.Scan(&r.ID, &r.Scope, &r.OwnerID, &r.CreatorID, &r.ItemType, &r.Favorite,
		&r.PayloadVersion, &r.Nonce, &r.Ciphertext, &r.Revision, &r.CreatedAt, &r.UpdatedAt, &r.DeletedAt)
	return r, err
}

// metaRow is the plaintext projection for list responses.
type metaRow struct {
	ID        string
	Scope     string
	OwnerID   sql.NullString
	CreatorID sql.NullString
	ItemType  string
	Favorite  bool
	Revision  uint64
	CreatedAt string
	UpdatedAt string
	DeletedAt sql.NullString
}

func (m metaRow) toMeta() Meta {
	meta := Meta{
		ID:        m.ID,
		ItemType:  m.ItemType,
		Scope:     m.Scope,
		Favorite:  m.Favorite,
		Revision:  m.Revision,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
	if m.OwnerID.Valid {
		owner := m.OwnerID.String
		meta.OwnerID = &owner
	}
	if m.CreatorID.Valid {
		creator := m.CreatorID.String
		meta.CreatorID = &creator
	}
	if m.DeletedAt.Valid {
		deleted := m.DeletedAt.String
		meta.DeletedAt = &deleted
	}
	return meta
}

func (r itemRow) toMeta() Meta {
	return metaRow{
		ID: r.ID, Scope: r.Scope, OwnerID: r.OwnerID, CreatorID: r.CreatorID,
		ItemType: r.ItemType, Favorite: r.Favorite, Revision: r.Revision,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, DeletedAt: r.DeletedAt,
	}.toMeta()
}

// policyView renders the ownership fields the policy decides on.
func (r itemRow) policyView() Item {
	owner, creator := r.OwnerID.String, r.CreatorID.String
	if !r.OwnerID.Valid {
		owner = ""
	}
	if !r.CreatorID.Valid {
		creator = ""
	}
	return policyItem(r.Scope, owner, creator)
}

type repository struct{}

// insertItem stores one new item at revision 1.
func (repository) insertItem(ctx context.Context, q Queryer, r itemRow) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO vault_items
			(id, vault_scope, owner_user_id, created_by_user_id, item_type, favorite,
			 payload_version, nonce, ciphertext, revision, created_at, updated_at, deleted_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
		r.ID, r.Scope, r.OwnerID, r.CreatorID, r.ItemType, r.Favorite,
		r.PayloadVersion, r.Nonce, r.Ciphertext, r.Revision, r.CreatedAt, r.UpdatedAt)
	return err
}

var errItemMissing = errors.New("vault: row missing")

// getItem loads the full row (with ciphertext) by id.
func (repository) getItem(ctx context.Context, q Queryer, id string) (itemRow, error) {
	row, err := scanItem(q.QueryRowContext(ctx, `SELECT `+itemColumns+` FROM vault_items WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return itemRow{}, errItemMissing
	}
	return row, err
}

// listItems pages the candidate set newest-first by the (updated_at, id)
// keyset. The caller supplies the authorization predicate; this function
// never decides who may see what.
func (repo repository) listItems(ctx context.Context, q Queryer, where string, args []any, beforeUpdated, beforeID string, limit int) ([]metaRow, error) {
	rows, err := repo.listRows(ctx, q, where, args, beforeUpdated, beforeID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]metaRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, metaRow{
			ID: r.ID, Scope: r.Scope, OwnerID: r.OwnerID, CreatorID: r.CreatorID,
			ItemType: r.ItemType, Favorite: r.Favorite, Revision: r.Revision,
			CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, DeletedAt: r.DeletedAt,
		})
	}
	return out, nil
}

// listRows is the full-row variant of listItems: it carries the encrypted
// columns so the decrypt-and-filter scanner can process candidates in
// batches. limit<=0 means "all remaining rows" (health scans).
func (repo repository) listRows(ctx context.Context, q Queryer, where string, args []any, beforeUpdated, beforeID string, limit int) ([]itemRow, error) {
	query := `SELECT ` + itemColumns + ` FROM vault_items WHERE ` + where
	if beforeUpdated != "" {
		query += ` AND (updated_at < ? OR (updated_at = ? AND id < ?))`
		args = append(args, beforeUpdated, beforeUpdated, beforeID)
	}
	query += ` ORDER BY updated_at DESC, id DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []itemRow{}
	for rows.Next() {
		r, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// insertVersion archives one superseded version inside the update
// transaction. created_at records when that version was current.
func (repository) insertVersion(ctx context.Context, q Queryer, itemID string, r itemRow) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO item_versions (item_id, revision, payload_version, nonce, ciphertext, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		itemID, r.Revision, r.PayloadVersion, r.Nonce, r.Ciphertext, r.UpdatedAt)
	return err
}

// updateItem performs the optimistic-locked write: it archives the previous
// version and flips the current row to the new ciphertext only when the
// caller's revision still matches. RowsAffected==0 means the item moved on.
func (repo repository) updateItem(ctx context.Context, q Queryer, current itemRow, newPayloadVersion uint16, nonce, ciphertext []byte, favorite bool, updatedAt string) (bool, error) {
	if err := repo.insertVersion(ctx, q, current.ID, current); err != nil {
		return false, err
	}
	res, err := q.ExecContext(ctx,
		`UPDATE vault_items
		 SET payload_version=?, nonce=?, ciphertext=?, revision=revision+1, favorite=?, updated_at=?
		 WHERE id=? AND revision=? AND deleted_at IS NULL`,
		newPayloadVersion, nonce, ciphertext, favorite, updatedAt, current.ID, current.Revision)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected != 1 {
		return false, nil
	}
	// Retain only the most recent MaxHistoryVersions archived versions;
	// with ten or fewer rows this deletes nothing.
	_, err = q.ExecContext(ctx,
		`DELETE FROM item_versions WHERE item_id=? AND revision NOT IN
		   (SELECT revision FROM (SELECT revision FROM item_versions WHERE item_id=? ORDER BY revision DESC LIMIT ?))`,
		current.ID, current.ID, MaxHistoryVersions)
	if err != nil {
		return false, err
	}
	return true, nil
}

// listHistory pages archived versions newest-first by revision. before==0
// starts from the newest version.
func (repo repository) listHistory(ctx context.Context, q Queryer, itemID string, before uint64, limit int) ([]HistoryEntry, error) {
	query := `SELECT revision, created_at FROM item_versions WHERE item_id=?`
	args := []any{itemID}
	if before > 0 {
		query += ` AND revision < ?`
		args = append(args, before)
	}
	query += ` ORDER BY revision DESC LIMIT ?`
	args = append(args, limit)
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HistoryEntry{}
	for rows.Next() {
		var e HistoryEntry
		if err := rows.Scan(&e.Revision, &e.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// getHistoryVersion loads one archived version. The projection reuses
// itemRow so decryption shares the row-based path.
func (repo repository) getHistoryVersion(ctx context.Context, q Queryer, itemID string, revision uint64) (itemRow, error) {
	row := itemRow{ID: itemID, Revision: revision}
	err := q.QueryRowContext(ctx,
		`SELECT payload_version, nonce, ciphertext, created_at FROM item_versions WHERE item_id=? AND revision=?`,
		itemID, revision).Scan(&row.PayloadVersion, &row.Nonce, &row.Ciphertext, &row.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return itemRow{}, errItemMissing
	}
	return row, err
}

// setDeletedAt moves an item into (at != "") or out of (at == "") the trash.
// The conditional forms make concurrent double-trash / double-restore lose
// loudly instead of double-auditing.
func (repo repository) setDeletedAt(ctx context.Context, q Queryer, itemID, at string) (bool, error) {
	var (
		res sql.Result
		err error
	)
	if at == "" {
		res, err = q.ExecContext(ctx,
			`UPDATE vault_items SET deleted_at=NULL WHERE id=? AND deleted_at IS NOT NULL`, itemID)
	} else {
		res, err = q.ExecContext(ctx,
			`UPDATE vault_items SET deleted_at=? WHERE id=? AND deleted_at IS NULL`, at, itemID)
	}
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// deleteItemCompletely removes the item row; item_versions cascade via the
// foreign key. Runs inside the caller's transaction.
func (repo repository) deleteItemCompletely(ctx context.Context, q Queryer, itemID string) error {
	_, err := q.ExecContext(ctx, `DELETE FROM vault_items WHERE id=?`, itemID)
	return err
}

// deleteItemAtRevision permanently removes an active item only when its
// optimistic-lock revision is still the expected one. Item history cascades
// through the vault_items foreign key.
func (repository) deleteItemAtRevision(ctx context.Context, q Queryer, itemID string, revision uint64) (bool, error) {
	res, err := q.ExecContext(ctx,
		`DELETE FROM vault_items WHERE id=? AND revision=? AND deleted_at IS NULL`,
		itemID, revision)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// expiredTrashIDs lists trashed items whose retention deadline has passed.
func (repo repository) expiredTrashIDs(ctx context.Context, q Queryer, cutoff string) ([]string, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id FROM vault_items WHERE deleted_at IS NOT NULL AND deleted_at <= ? ORDER BY id`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// currentRevision re-reads just the revision after a CAS miss so the conflict
// response can tell the client where the head actually is.
func (repository) currentRevision(ctx context.Context, q Queryer, id string) (uint64, error) {
	var revision uint64
	var deleted sql.NullString
	err := q.QueryRowContext(ctx, `SELECT revision, deleted_at FROM vault_items WHERE id=?`, id).Scan(&revision, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, errItemMissing
	}
	if err != nil {
		return 0, err
	}
	if deleted.Valid {
		return 0, errItemMissing
	}
	return revision, nil
}
