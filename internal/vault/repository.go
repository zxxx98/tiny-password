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
func (repository) listItems(ctx context.Context, q Queryer, where string, args []any, beforeUpdated, beforeID string, limit int) ([]metaRow, error) {
	query := `SELECT ` + metaColumns + ` FROM vault_items WHERE ` + where
	if beforeUpdated != "" {
		query += ` AND (updated_at < ? OR (updated_at = ? AND id < ?))`
		args = append(args, beforeUpdated, beforeUpdated, beforeID)
	}
	query += ` ORDER BY updated_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []metaRow{}
	for rows.Next() {
		var m metaRow
		if err := rows.Scan(&m.ID, &m.Scope, &m.OwnerID, &m.CreatorID, &m.ItemType, &m.Favorite,
			&m.Revision, &m.CreatedAt, &m.UpdatedAt, &m.DeletedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
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
	return affected == 1, nil
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
