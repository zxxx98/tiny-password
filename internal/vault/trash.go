package vault

import (
	"context"
	"time"

	"github.com/tiny-password/tiny-password/internal/audit"
	"github.com/tiny-password/tiny-password/internal/auth"
)

// TrashRetentionDays bounds how long a trashed item survives before the
// background purge removes it permanently (design §6.3).
const TrashRetentionDays = 30

// Trash moves an item to the recycle bin: only deleted_at is set, every
// payload byte stays exactly as it is. Personal owner or shared creator
// only; trashed items disappear from detail and list views immediately.
func (s *Service) Trash(ctx context.Context, actor *auth.Principal, itemID string) error {
	if actor == nil {
		return ErrForbidden
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	row, err := s.repo.getItem(ctx, tx, itemID)
	if err != nil {
		return ErrNotFound
	}
	view := row.policyView()
	if row.DeletedAt.Valid || !Can(Role(actor.Role), actor.UserID, view, ActionRead) {
		return ErrNotFound
	}
	if !Can(Role(actor.Role), actor.UserID, view, ActionDelete) {
		return ErrForbidden
	}
	at := s.now().UTC().Format(TimestampFormat)
	applied, err := s.repo.setDeletedAt(ctx, tx, itemID, at)
	if err != nil {
		return err
	}
	if !applied {
		return ErrNotFound
	}
	if err := s.audit.Record(ctx, tx, audit.Event{
		Name: audit.EventVaultItemTrashed, ActorID: actor.UserID,
		TargetType: audit.TargetItem, TargetID: itemID, Result: audit.ResultSuccess,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// RestoreTrashed brings a trashed item back unchanged: no revision bump, no
// re-encryption, no history entry. Personal owner or shared creator only.
func (s *Service) RestoreTrashed(ctx context.Context, actor *auth.Principal, itemID string) (Detail, error) {
	if actor == nil {
		return Detail{}, ErrForbidden
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Detail{}, err
	}
	defer tx.Rollback()
	row, err := s.repo.getItem(ctx, tx, itemID)
	if err != nil {
		return Detail{}, ErrNotFound
	}
	view := row.policyView()
	if !Can(Role(actor.Role), actor.UserID, view, ActionRead) {
		return Detail{}, ErrNotFound
	}
	if !Can(Role(actor.Role), actor.UserID, view, ActionRestore) {
		return Detail{}, ErrForbidden
	}
	if !row.DeletedAt.Valid {
		return Detail{}, ErrNotFound // nothing in the trash to restore
	}
	applied, err := s.repo.setDeletedAt(ctx, tx, itemID, "")
	if err != nil {
		return Detail{}, err
	}
	if !applied {
		return Detail{}, ErrNotFound
	}
	if err := s.audit.Record(ctx, tx, audit.Event{
		Name: audit.EventVaultItemRestored, ActorID: actor.UserID,
		TargetType: audit.TargetItem, TargetID: itemID, Result: audit.ResultSuccess,
	}); err != nil {
		return Detail{}, err
	}
	if err := tx.Commit(); err != nil {
		return Detail{}, err
	}
	_, typed, envelope, err := s.decryptRow(row)
	if err != nil {
		return Detail{}, err
	}
	return Detail{Meta: row.toMeta(), Tags: envelope.Tags, Payload: typed}, nil
}

// Purge permanently removes a trashed item and its archived versions. This
// is the early-exit from the recycle bin; the item must currently be
// trashed, and only the personal owner or shared creator may call it.
func (s *Service) Purge(ctx context.Context, actor *auth.Principal, itemID string) error {
	if actor == nil {
		return ErrForbidden
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	row, err := s.repo.getItem(ctx, tx, itemID)
	if err != nil {
		return ErrNotFound
	}
	view := row.policyView()
	if !Can(Role(actor.Role), actor.UserID, view, ActionRead) {
		return ErrNotFound
	}
	if !Can(Role(actor.Role), actor.UserID, view, ActionPurge) {
		return ErrForbidden
	}
	if !row.DeletedAt.Valid {
		return ErrNotFound // only trashed items can be purged
	}
	if err := s.repo.deleteItemCompletely(ctx, tx, itemID); err != nil {
		return err
	}
	if err := s.audit.Record(ctx, tx, audit.Event{
		Name: audit.EventVaultItemPurged, ActorID: actor.UserID,
		TargetType: audit.TargetItem, TargetID: itemID, Result: audit.ResultSuccess,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// ListTrash pages the caller's readable trashed items (own personal items
// plus shared items), metadata only, newest first by the (updated_at, id)
// keyset. Restoration rights remain governed by the write policy.
func (s *Service) ListTrash(ctx context.Context, actor *auth.Principal, beforeUpdated, beforeID string, limit int) ([]Meta, error) {
	if actor == nil {
		return nil, ErrForbidden
	}
	where := `deleted_at IS NOT NULL AND ((vault_scope='personal' AND owner_user_id=?) OR vault_scope='shared')`
	args := []any{actor.UserID}
	rows, err := s.repo.listItems(ctx, s.db, where, args, beforeUpdated, beforeID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Meta, 0, len(rows))
	for _, m := range rows {
		owner, creator := "", ""
		if m.OwnerID.Valid {
			owner = m.OwnerID.String
		}
		if m.CreatorID.Valid {
			creator = m.CreatorID.String
		}
		if !CanReadItem(actor.UserID, policyItem(m.Scope, owner, creator)) {
			continue
		}
		out = append(out, m.toMeta())
	}
	return out, nil
}

// PurgeExpiredTrash is the maintenance entry the scheduler calls (T23): it
// permanently removes items whose trash retention has elapsed. Each item is
// purged with its archived versions inside one transaction and audited
// under the anonymous actor; re-running the sweep is safe.
func (s *Service) PurgeExpiredTrash(ctx context.Context) (int64, error) {
	cutoff := s.now().UTC().Add(-TrashRetentionDays * 24 * time.Hour).Format(TimestampFormat)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	ids, err := s.repo.expiredTrashIDs(ctx, tx, cutoff)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := s.repo.deleteItemCompletely(ctx, tx, id); err != nil {
			return 0, err
		}
		if err := s.audit.Record(ctx, tx, audit.Event{
			Name:       audit.EventVaultItemPurged,
			ActorID:    audit.Anonymous,
			TargetType: audit.TargetItem, TargetID: id, Result: audit.ResultSuccess,
		}); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int64(len(ids)), nil
}
