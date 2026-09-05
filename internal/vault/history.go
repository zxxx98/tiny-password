package vault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/tiny-password/tiny-password/internal/audit"
	"github.com/tiny-password/tiny-password/internal/auth"
	"github.com/tiny-password/tiny-password/internal/platform/crypto"
)

// MaxHistoryVersions bounds the archived versions per item (design §6.3:
// the most recent 10 history versions are retained).
const MaxHistoryVersions = 10

// HistoryEntry is one archived version's metadata. Payloads stay sealed:
// the history list never decrypts anything.
type HistoryEntry struct {
	Revision  uint64 `json:"revision"`
	UpdatedAt string `json:"updated_at"`
}

// historyColumns for the keyset over (revision DESC).
const historyColumns = `revision, created_at`

// ListHistory pages the archived versions of one item, newest first. Every
// member who can read the item may read its history (design §5.2).
func (s *Service) ListHistory(ctx context.Context, actor *auth.Principal, itemID, beforeRevision string, limit int) ([]HistoryEntry, error) {
	if actor == nil {
		return nil, ErrForbidden
	}
	row, err := s.repo.getItem(ctx, s.db, itemID)
	if err != nil {
		return nil, ErrNotFound
	}
	if row.DeletedAt.Valid || !Can(Role(actor.Role), actor.UserID, row.policyView(), ActionHistory) {
		return nil, ErrNotFound
	}
	var before uint64
	if beforeRevision != "" {
		if _, err := fmt.Sscanf(beforeRevision, "%d", &before); err != nil || before == 0 {
			return nil, ErrNotFound
		}
	}
	return s.repo.listHistory(ctx, s.db, itemID, before, limit)
}

// RestoreHistory re-seals an archived version as a new current revision. The
// archived ciphertext is decrypted, then re-encrypted under a fresh nonce and
// the new revision's AAD — old ciphertext is never copied forward. The
// previous current version is archived in the same transaction.
func (s *Service) RestoreHistory(ctx context.Context, actor *auth.Principal, itemID string, revision uint64) (Detail, error) {
	if actor == nil {
		return Detail{}, ErrForbidden
	}
	if revision == 0 {
		return Detail{}, ErrNotFound
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
	if row.DeletedAt.Valid || !Can(Role(actor.Role), actor.UserID, view, ActionHistory) {
		return Detail{}, ErrNotFound
	}
	if !Can(Role(actor.Role), actor.UserID, view, ActionHistoryRestore) {
		return Detail{}, ErrForbidden
	}

	archived, err := s.repo.getHistoryVersion(ctx, tx, itemID, revision)
	if err != nil {
		return Detail{}, ErrNotFound
	}
	aad := AADFor(itemID, row.Scope, row.OwnerID.String, row.CreatorID.String, archived.PayloadVersion, archived.Revision)
	plaintext, err := s.key.DecryptColumns(archived.PayloadVersion, archived.Nonce, archived.Ciphertext, aad)
	if err != nil {
		return Detail{}, errors.New("vault: archived payload failed authentication")
	}
	var envelope storedPayload
	if err := json.Unmarshal(plaintext, &envelope); err != nil || envelope.Version != PayloadSchemaVersion {
		return Detail{}, errors.New("vault: archived payload is unreadable")
	}
	typed := typedPayloadOf(row.ItemType, &envelope)
	if typed == nil {
		return Detail{}, errors.New("vault: archived payload does not match the item type")
	}

	// Re-encrypt the restored content under the next revision's AAD.
	enc, err := s.key.Encrypt(plaintext, AADFor(itemID, row.Scope, row.OwnerID.String, row.CreatorID.String, crypto.PayloadVersion, row.Revision+1))
	if err != nil {
		return Detail{}, err
	}
	updatedAt := s.now().UTC().Format(TimestampFormat)
	applied, err := s.repo.updateItem(ctx, tx, row, enc.Version, enc.Nonce[:], enc.Ciphertext, row.Favorite, updatedAt)
	if err != nil {
		return Detail{}, err
	}
	if !applied {
		current, err := s.repo.currentRevision(ctx, tx, itemID)
		if err != nil {
			return Detail{}, ErrNotFound
		}
		return Detail{}, &RevisionConflictError{CurrentRevision: current}
	}
	if err := s.audit.Record(ctx, tx, audit.Event{
		Name: audit.EventVaultItemHistoryRestored, ActorID: actor.UserID,
		TargetType: audit.TargetItem, TargetID: itemID, Result: audit.ResultSuccess,
	}); err != nil {
		return Detail{}, err
	}
	if err := tx.Commit(); err != nil {
		return Detail{}, err
	}
	meta := row.toMeta()
	meta.Revision = row.Revision + 1
	meta.UpdatedAt = updatedAt
	out := Detail{Meta: meta, Tags: envelope.Tags, Payload: typed}
	out.Title = TitleOf(typed)
	return out, nil
}
