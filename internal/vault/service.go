package vault

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/tiny-password/tiny-password/internal/audit"
	"github.com/tiny-password/tiny-password/internal/auth"
	"github.com/tiny-password/tiny-password/internal/idempotency"
	"github.com/tiny-password/tiny-password/internal/platform/archive"
	"github.com/tiny-password/tiny-password/internal/platform/crypto"
	"github.com/tiny-password/tiny-password/internal/platform/ident"
	"github.com/tiny-password/tiny-password/internal/platform/sqlite"
)

// Options configures the vault service.
type Options struct {
	// Now is injectable for tests.
	Now func() time.Time
	// Audit receives same-transaction change events and read events.
	Audit *audit.Service
	// DecryptHook, when set, fires once per payload decryption with the item
	// id. Tests use it to assert that only authorized candidates decrypt;
	// production leaves it nil.
	DecryptHook func(itemID string)
	// SearchProfile receives optional phase timings for search benchmarks.
	// It is never given payload data and is nil in production by default.
	SearchProfile SearchProfileHook
}

// Service orchestrates the item lifecycle: the repository narrows candidates,
// the policy authorizes, and only then is anything decrypted.
type Service struct {
	db            *sql.DB
	key           *crypto.MasterKey
	repo          repository
	now           func() time.Time
	audit         *audit.Service
	decryptHook   func(itemID string)
	searchProfile SearchProfileHook
}

// NewService requires the master key: without it no payload can be sealed
// and the item endpoints must not be wired at all.
func NewService(db *sql.DB, key *crypto.MasterKey, options Options) (*Service, error) {
	if db == nil {
		return nil, errors.New("vault: database is required")
	}
	if key == nil {
		return nil, errors.New("vault: master key is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Audit == nil {
		options.Audit = audit.NewService(audit.Options{})
	}
	return &Service{db: db, key: key, now: options.Now, audit: options.Audit,
		decryptHook: options.DecryptHook, searchProfile: options.SearchProfile}, nil
}

// CreateInput carries the create request. Ownership is derived from the
// acting principal, never from the request.
type CreateInput struct {
	ItemType string
	Scope    string
	Tags     []string
	Favorite bool
	Payload  json.RawMessage
}

// Create stores a new item at revision 1. The idempotency claim, when
// present, is completed inside the insert transaction so a replay can render
// the original resource and a first success is the only possible one.
func (s *Service) Create(ctx context.Context, actor *auth.Principal, input CreateInput, claim *idempotency.Claim) (Detail, error) {
	if actor == nil {
		return Detail{}, ErrForbidden
	}
	if !ItemTypes[input.ItemType] {
		return Detail{}, ErrInvalidItemType
	}
	if !Scopes[input.Scope] {
		return Detail{}, ErrInvalidScope
	}
	payload, err := decodePayload(input.ItemType, input.Payload)
	if err != nil {
		return Detail{}, err
	}
	tags, err := validateTags(input.Tags)
	if err != nil {
		return Detail{}, err
	}

	// The server fixes ownership at creation time; the policy call stays
	// explicit so a future refactor cannot silently move ownership.
	owner, creator := "", ""
	if input.Scope == string(ScopePersonal) {
		owner = actor.UserID
	} else {
		creator = actor.UserID
	}
	view := policyItem(input.Scope, owner, creator)
	if !CanCreate(Role(actor.Role), actor.UserID, view.Scope, owner, creator) {
		return Detail{}, ErrForbidden
	}

	// Address references resolve before anything is written; all rule
	// violations share one error code so probing reveals nothing.
	if ref, ok := referenceTarget(payload); ok {
		if err := s.checkReference(ctx, actor, view, ref); err != nil {
			return Detail{}, err
		}
	}

	envelope, plaintext, err := buildEnvelope(input.ItemType, tags, payload)
	if err != nil {
		return Detail{}, err
	}
	id := ident.NewUUIDv7()
	aad := AADFor(id, input.Scope, owner, creator, crypto.PayloadVersion, 1)
	enc, err := s.key.Encrypt(plaintext, aad)
	if err != nil {
		return Detail{}, err
	}

	at := s.now().UTC().Format(TimestampFormat)
	row := itemRow{
		ID: id, Scope: input.Scope,
		OwnerID:   sql.NullString{String: owner, Valid: owner != ""},
		CreatorID: sql.NullString{String: creator, Valid: creator != ""},
		ItemType:  input.ItemType, Favorite: input.Favorite,
		PayloadVersion: enc.Version, Nonce: enc.Nonce[:], Ciphertext: enc.Ciphertext,
		Revision: 1, CreatedAt: at, UpdatedAt: at,
	}
	if !row.OwnerID.Valid && !row.CreatorID.Valid {
		return Detail{}, ErrInvalidScope
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Detail{}, err
	}
	defer tx.Rollback()
	if err := s.repo.insertItem(ctx, tx, row); err != nil {
		return Detail{}, err
	}
	if claim != nil {
		if err := claim.Complete(ctx, tx, id); err != nil {
			return Detail{}, err
		}
	}
	if err := s.audit.Record(ctx, tx, audit.Event{
		Name: audit.EventVaultItemCreated, ActorID: actor.UserID,
		TargetType: audit.TargetItem, TargetID: id, Result: audit.ResultSuccess,
	}); err != nil {
		return Detail{}, err
	}
	if err := tx.Commit(); err != nil {
		return Detail{}, err
	}
	return s.decorateDetail(ctx, s.db, Detail{Meta: row.toMeta(), Tags: envelope.Tags, Payload: payload}), nil
}

// checkReference validates one address reference: the target must exist, be
// an identity item, be readable by the actor, and respect shared→shared
// visibility (design §6.2, decision D06/T01 contract).
func (s *Service) checkReference(ctx context.Context, actor *auth.Principal, source Item, refID string) error {
	targetRow, err := s.repo.getItem(ctx, s.db, refID)
	if err != nil {
		return ErrReferenceForbidden
	}
	if targetRow.ItemType != TypeIdentity {
		return ErrReferenceForbidden
	}
	if !CanReference(actor.UserID, source, targetRow.policyView()) {
		return ErrReferenceForbidden
	}
	return nil
}

// Get returns the decrypted detail. Authorization strictly precedes
// decryption, and unreadable items are indistinguishable from unknown ones.
func (s *Service) Get(ctx context.Context, actor *auth.Principal, id string) (Detail, error) {
	if actor == nil {
		return Detail{}, ErrForbidden
	}
	row, err := s.repo.getItem(ctx, s.db, id)
	if err != nil {
		return Detail{}, ErrNotFound
	}
	if row.DeletedAt.Valid || !Can(Role(actor.Role), actor.UserID, row.policyView(), ActionRead) {
		return Detail{}, ErrNotFound
	}
	_, typed, envelope, err := s.decryptRow(row)
	if err != nil {
		return Detail{}, err
	}
	if err := s.audit.Record(ctx, s.db, audit.Event{
		Name: audit.EventVaultItemViewed, ActorID: actor.UserID,
		TargetType: audit.TargetItem, TargetID: id, Result: audit.ResultSuccess,
	}); err != nil {
		return Detail{}, err
	}
	detail := Detail{Meta: row.toMeta(), Tags: envelope.Tags, Payload: typed}
	detail.Title = TitleOf(typed)
	metas := []Meta{detail.Meta}
	s.attachCreatorNames(ctx, s.db, metas)
	detail.Meta = metas[0]
	return detail, nil
}

// ListFilter narrows the caller-readable candidate set. Tags live inside
// the ciphertext: a tag filter switches the list to the decrypt-and-filter
// scanner, exactly like search.
type ListFilter struct {
	Scope    string // "", "personal" or "shared"
	ItemType string // "" or one of the five types
	Favorite *bool  // nil = any
	Tag      string // "" = any
}

// List pages item metadata the actor may read, newest first by the
// (updated_at, id) keyset. Tags are inside the ciphertext, so tag filtering
// arrives with the search milestone (T13); payloads are never included.
func (s *Service) List(ctx context.Context, actor *auth.Principal, filter ListFilter, beforeUpdated, beforeID string, limit int) ([]Meta, error) {
	if actor == nil {
		return nil, ErrForbidden
	}
	if filter.Scope != "" && !Scopes[filter.Scope] {
		return nil, ErrInvalidScope
	}
	if filter.ItemType != "" && !ItemTypes[filter.ItemType] {
		return nil, ErrInvalidItemType
	}
	where := `deleted_at IS NULL`
	args := []any{}
	switch filter.Scope {
	case "":
		// The union of own personal items and every shared item; the
		// administrator holds no personal-vault privilege.
		where += ` AND ((vault_scope='personal' AND owner_user_id=?) OR vault_scope='shared')`
		args = append(args, actor.UserID)
	case string(ScopePersonal):
		where += ` AND vault_scope='personal' AND owner_user_id=?`
		args = append(args, actor.UserID)
	case string(ScopeShared):
		where += ` AND vault_scope='shared'`
	}
	if filter.ItemType != "" {
		where += ` AND item_type=?`
		args = append(args, filter.ItemType)
	}
	if filter.Favorite != nil {
		where += ` AND favorite=?`
		args = append(args, *filter.Favorite)
	}
	if filter.Tag != "" {
		matched, err := s.scanMatches(ctx, where, args, matchQuery("", filter.Tag), beforeUpdated, beforeID, limit)
		if err != nil {
			return nil, err
		}
		return matched, nil
	}
	rows, err := s.repo.listRows(ctx, s.db, where, args, beforeUpdated, beforeID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Meta, 0, len(rows))
	for _, row := range rows {
		// Defense in depth: re-check the policy even though SQL narrowed the set.
		owner, creator := "", ""
		if row.OwnerID.Valid {
			owner = row.OwnerID.String
		}
		if row.CreatorID.Valid {
			creator = row.CreatorID.String
		}
		if !CanReadItem(actor.UserID, policyItem(row.Scope, owner, creator)) {
			continue
		}
		// Titles are decrypted per page for the workspace list; no plaintext
		// ever lands in a column or index (design §6.1, §7.1).
		_, typed, _, err := s.decryptRow(row)
		if err != nil {
			return nil, err
		}
		meta := row.toMeta()
		meta.Title = TitleOf(typed)
		out = append(out, meta)
	}
	s.attachCreatorNames(ctx, s.db, out)
	return out, nil
}

// UpdateInput carries the update request. Absent tags keep the stored tags;
// ownership, scope and type are not part of the request at all.
type UpdateInput struct {
	Revision uint64
	Tags     *[]string
	Favorite *bool
	Payload  json.RawMessage
}

// Update applies one effective change under optimistic locking. The previous
// version is archived and the revision advanced inside a single transaction;
// a lost race returns the current revision without overwriting anything.
func (s *Service) Update(ctx context.Context, actor *auth.Principal, id string, input UpdateInput) (Detail, error) {
	// SQLite's deferred transaction can fail while upgrading a read snapshot
	// to a writer when two callers start from the same revision. Roll back that
	// losing snapshot and retry so the second caller observes the committed
	// revision and receives the intended REVISION_CONFLICT instead of a
	// transient DATABASE_BUSY response.
	var detail Detail
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			delay := time.Duration(attempt) * time.Millisecond
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return Detail{}, ctx.Err()
			case <-timer.C:
			}
		}
		detail, err = s.updateOnce(ctx, actor, id, input)
		if !sqlite.IsBusy(err) || attempt == 2 {
			return detail, err
		}
	}
	return detail, err
}

func (s *Service) updateOnce(ctx context.Context, actor *auth.Principal, id string, input UpdateInput) (Detail, error) {
	if actor == nil {
		return Detail{}, ErrForbidden
	}
	if input.Revision < 1 {
		return Detail{}, fmt.Errorf("%w: revision must be at least 1", ErrPayloadInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Detail{}, err
	}
	defer tx.Rollback()

	row, err := s.repo.getItem(ctx, tx, id)
	if err != nil {
		return Detail{}, ErrNotFound
	}
	view := row.policyView()
	if row.DeletedAt.Valid || !Can(Role(actor.Role), actor.UserID, view, ActionRead) {
		return Detail{}, ErrNotFound
	}
	if !Can(Role(actor.Role), actor.UserID, view, ActionUpdate) {
		return Detail{}, ErrForbidden
	}
	// The client's revision is the optimistic lock: a stale request never
	// reaches the write path, and the CAS below guards the remaining race.
	if row.Revision != input.Revision {
		return Detail{}, &RevisionConflictError{CurrentRevision: row.Revision}
	}

	// The type is fixed by the stored row; the payload must match it.
	payload, err := decodePayload(row.ItemType, input.Payload)
	if err != nil {
		return Detail{}, err
	}
	// Address references are part of the encrypted payload, so updates must
	// enforce the same visibility and target-type rules as creates.
	if ref, ok := referenceTarget(payload); ok {
		if err := s.checkReference(ctx, actor, view, ref); err != nil {
			return Detail{}, err
		}
	}

	// Decrypt the current envelope: it supplies the tags when the request
	// omits them and the canonical bytes for no-change detection.
	oldRaw, _, oldEnvelope, err := s.decryptRow(row)
	if err != nil {
		return Detail{}, err
	}
	tags := oldEnvelope.Tags
	if input.Tags != nil {
		tags, err = validateTags(*input.Tags)
		if err != nil {
			return Detail{}, err
		}
	}
	favorite := row.Favorite
	if input.Favorite != nil {
		favorite = *input.Favorite
	}

	envelope, plaintext, err := buildEnvelope(row.ItemType, tags, payload)
	if err != nil {
		return Detail{}, err
	}
	if bytes.Equal(plaintext, oldRaw) && favorite == row.Favorite {
		// No effective change: not an update, no history, no revision bump.
		return Detail{Meta: row.toMeta(), Tags: oldEnvelope.Tags, Payload: typedPayloadOf(row.ItemType, oldEnvelope)}, nil
	}

	enc, err := s.key.Encrypt(plaintext, AADFor(id, row.Scope, row.OwnerID.String, row.CreatorID.String, crypto.PayloadVersion, row.Revision+1))
	if err != nil {
		return Detail{}, err
	}
	updatedAt := s.now().UTC().Format(TimestampFormat)
	applied, err := s.repo.updateItem(ctx, tx, row, enc.Version, enc.Nonce[:], enc.Ciphertext, favorite, updatedAt)
	if err != nil {
		return Detail{}, err
	}
	if !applied {
		current, err := s.repo.currentRevision(ctx, tx, id)
		if err != nil {
			return Detail{}, ErrNotFound
		}
		return Detail{}, &RevisionConflictError{CurrentRevision: current}
	}
	if err := s.audit.Record(ctx, tx, audit.Event{
		Name: audit.EventVaultItemUpdated, ActorID: actor.UserID,
		TargetType: audit.TargetItem, TargetID: id, Result: audit.ResultSuccess,
	}); err != nil {
		return Detail{}, err
	}
	if err := tx.Commit(); err != nil {
		return Detail{}, err
	}
	meta := row.toMeta()
	meta.Revision = row.Revision + 1
	meta.Favorite = favorite
	meta.UpdatedAt = updatedAt
	return Detail{Meta: meta, Tags: envelope.Tags, Payload: payload}, nil
}

// decryptRow opens the current version of a row and returns the canonical
// plaintext bytes, the typed payload and the envelope. Every failure here
// indicates corruption or a bug and maps to a bare internal error — never
// to details that could leak stored material.
func (s *Service) decryptRow(row itemRow) ([]byte, any, *storedPayload, error) {
	return s.decryptRowWithProfile(row, nil)
}

func (s *Service) decryptRowWithProfile(row itemRow, profile SearchProfileHook) ([]byte, any, *storedPayload, error) {
	if s.decryptHook != nil {
		s.decryptHook(row.ID)
	}
	aad := AADFor(row.ID, row.Scope, row.OwnerID.String, row.CreatorID.String, row.PayloadVersion, row.Revision)
	started := time.Now()
	raw, err := s.key.DecryptColumns(row.PayloadVersion, row.Nonce, row.Ciphertext, aad)
	if profile != nil {
		profile(SearchPhaseDecrypt, time.Since(started))
	}
	if err != nil {
		return nil, nil, nil, errors.New("vault: stored payload failed authentication")
	}
	var envelope storedPayload
	started = time.Now()
	if err := json.Unmarshal(raw, &envelope); err != nil {
		if profile != nil {
			profile(SearchPhaseJSON, time.Since(started))
		}
		return nil, nil, nil, errors.New("vault: stored payload is unreadable")
	}
	if profile != nil {
		profile(SearchPhaseJSON, time.Since(started))
	}
	if envelope.Version != PayloadSchemaVersion {
		return nil, nil, nil, errors.New("vault: stored payload version is unsupported")
	}
	if envelope.Tags == nil {
		// omitempty drops empty tag lists on storage; normalize so responses
		// always carry a JSON array, never null.
		envelope.Tags = []string{}
	}
	typed := typedPayloadOf(row.ItemType, &envelope)
	if typed == nil {
		return nil, nil, nil, errors.New("vault: stored payload does not match the item type")
	}
	return raw, typed, &envelope, nil
}

// typedPayloadOf picks the stored envelope branch matching the item type.
func typedPayloadOf(itemType string, envelope *storedPayload) any {
	switch itemType {
	case TypeLogin:
		if envelope.Login == nil {
			return nil
		}
		return envelope.Login
	case TypeSSHKey:
		if envelope.SSHKey == nil {
			return nil
		}
		return envelope.SSHKey
	case TypeCreditCard:
		if envelope.CreditCard == nil {
			return nil
		}
		return envelope.CreditCard
	case TypeIdentity:
		if envelope.Identity == nil {
			return nil
		}
		return envelope.Identity
	case TypeSecureNote:
		if envelope.SecureNote == nil {
			return nil
		}
		return envelope.SecureNote
	default:
		return nil
	}
}

// SetFavorite flips the favorite flag as an ordinary effective update: the
// previous version is archived and the revision advances (design D06: the
// favorite flag is part of the item).
func (s *Service) SetFavorite(ctx context.Context, actor *auth.Principal, itemID string, favorite bool) error {
	_, err := s.applyMetadataChange(ctx, actor, itemID, &favorite, nil, ActionFavorite)
	return err
}

// SetTags replaces the tag list (dedup applies) as an ordinary effective
// update. Absent-changes keep the stored envelope untouched.
func (s *Service) SetTags(ctx context.Context, actor *auth.Principal, itemID string, tags []string) error {
	_, err := s.applyMetadataChange(ctx, actor, itemID, nil, &tags, ActionTag)
	return err
}

// applyMetadataChange updates the favorite flag and/or tags through the same
// optimistic-locking path as a payload update.
func (s *Service) applyMetadataChange(ctx context.Context, actor *auth.Principal, itemID string, favorite *bool, tags *[]string, action Action) (Detail, error) {
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
	if row.DeletedAt.Valid || !Can(Role(actor.Role), actor.UserID, view, ActionRead) {
		return Detail{}, ErrNotFound
	}
	if !Can(Role(actor.Role), actor.UserID, view, action) {
		return Detail{}, ErrForbidden
	}
	oldRaw, typed, oldEnvelope, err := s.decryptRow(row)
	if err != nil {
		return Detail{}, err
	}
	newTags := oldEnvelope.Tags
	if tags != nil {
		newTags, err = validateTags(*tags)
		if err != nil {
			return Detail{}, err
		}
	}
	newFavorite := row.Favorite
	if favorite != nil {
		newFavorite = *favorite
	}
	envelope, plaintext, err := buildEnvelope(row.ItemType, newTags, typed)
	if err != nil {
		return Detail{}, err
	}
	if bytes.Equal(plaintext, oldRaw) && newFavorite == row.Favorite {
		return Detail{Meta: row.toMeta(), Tags: oldEnvelope.Tags, Payload: typed}, nil
	}
	enc, err := s.key.Encrypt(plaintext, AADFor(itemID, row.Scope, row.OwnerID.String, row.CreatorID.String, crypto.PayloadVersion, row.Revision+1))
	if err != nil {
		return Detail{}, err
	}
	updatedAt := s.now().UTC().Format(TimestampFormat)
	applied, err := s.repo.updateItem(ctx, tx, row, enc.Version, enc.Nonce[:], enc.Ciphertext, newFavorite, updatedAt)
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
		Name: audit.EventVaultItemUpdated, ActorID: actor.UserID,
		TargetType: audit.TargetItem, TargetID: itemID, Result: audit.ResultSuccess,
	}); err != nil {
		return Detail{}, err
	}
	if err := tx.Commit(); err != nil {
		return Detail{}, err
	}
	meta := row.toMeta()
	meta.Revision = row.Revision + 1
	meta.Favorite = newFavorite
	meta.UpdatedAt = updatedAt
	return Detail{Meta: meta, Tags: envelope.Tags, Payload: typed}, nil
}

// attachCreatorNames resolves display usernames for shared items in one
// batched query so list and detail views can sign entries (design §12).
func (s *Service) attachCreatorNames(ctx context.Context, q Queryer, metas []Meta) {
	ids := make([]string, 0, len(metas))
	need := map[string]bool{}
	for _, m := range metas {
		if m.Scope == string(ScopeShared) && m.CreatorID != nil && !need[*m.CreatorID] {
			need[*m.CreatorID] = true
			ids = append(ids, *m.CreatorID)
		}
	}
	if len(ids) == 0 {
		return
	}
	query := `SELECT id, username_display FROM users WHERE id IN (`
	args := make([]any, 0, len(ids))
	for i, id := range ids {
		if i > 0 {
			query += ","
		}
		query += "?"
		args = append(args, id)
	}
	query += `)`
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return
	}
	defer rows.Close()
	names := map[string]string{}
	for rows.Next() {
		var id, display string
		if err := rows.Scan(&id, &display); err != nil {
			return
		}
		names[id] = display
	}
	for i := range metas {
		if metas[i].Scope == string(ScopeShared) && metas[i].CreatorID != nil {
			metas[i].CreatorName = names[*metas[i].CreatorID]
		}
	}
}

// decorateDetail fills the response-side derived fields (title from the
// typed payload, creator display name) on one detail response.
func (s *Service) decorateDetail(ctx context.Context, q Queryer, detail Detail) Detail {
	detail.Title = TitleOf(detail.Payload)
	metas := []Meta{detail.Meta}
	s.attachCreatorNames(ctx, q, metas)
	detail.Meta = metas[0]
	return detail
}

// AADFor assembles the row-bound additional authenticated data. Exactly one
// of ownerID/creatorID is set, mirroring the database CHECK constraint.
func AADFor(itemID, scope, ownerID, creatorID string, payloadVersion uint16, revision uint64) crypto.AAD {
	subject := ownerID
	if subject == "" {
		subject = creatorID
	}
	return crypto.AAD{
		ItemID:         itemID,
		Scope:          scope,
		SubjectID:      subject,
		PayloadVersion: payloadVersion,
		Revision:       revision,
	}
}

// secretFields is the closed set of sensitive field categories whose reveal
// and copy interactions are audited (design §6.4). Values are never recorded.
var secretFields = map[string]bool{
	"password":       true,
	"key_passphrase": true,
	"private_key":    true,
	"cvv":            true,
	"pin":            true,
	"number":         true,
}

// RecordSecretEvent writes one sensitive-field interaction to the audit
// trail. The event fires after the caller could see the value — the UI
// calls it when a field is revealed or copied — and carries only the field
// category, never its content.
func (s *Service) RecordSecretEvent(ctx context.Context, actor *auth.Principal, itemID, kind, field string) error {
	if actor == nil {
		return ErrForbidden
	}
	if !secretFields[field] {
		return fmt.Errorf("%w: unknown sensitive field", ErrPayloadInvalid)
	}
	row, err := s.repo.getItem(ctx, s.db, itemID)
	if err != nil {
		return ErrNotFound
	}
	if row.DeletedAt.Valid || !Can(Role(actor.Role), actor.UserID, row.policyView(), ActionRead) {
		return ErrNotFound
	}
	name := audit.EventVaultSecretRevealed
	if kind == "copy" {
		name = audit.EventVaultSecretCopied
	} else if kind != "reveal" {
		return fmt.Errorf("%w: unknown secret event kind", ErrPayloadInvalid)
	}
	if err := s.audit.Record(ctx, s.db, audit.Event{
		Name: name, ActorID: actor.UserID,
		TargetType: audit.TargetItem, TargetID: itemID, Result: audit.ResultSuccess,
	}); err != nil {
		return err
	}
	return nil
}

// ValidatePayload re-uses the package's payload grammar for import paths
// that receive decrypted payloads from outside this package.
func ValidatePayload(itemType string, raw json.RawMessage) error {
	_, err := decodePayload(itemType, raw)
	return err
}

// ValidatePayloadForImport applies the normal payload grammar while allowing
// a format converter to preserve a genuinely empty secure-note body.
func ValidatePayloadForImport(itemType string, raw json.RawMessage, allowEmptySecureNoteBody bool) error {
	_, err := decodePayloadWithOptions(itemType, raw, allowEmptySecureNoteBody)
	return err
}

// ExportItem is one decrypted item prepared for the personal archive.
type ExportItem struct {
	Meta    Meta
	Tags    []string
	Payload any
}

// An export always contains manifest.json and the items directory in addition
// to one file per item. Keep the item capacity below the archive entry limit
// so the output is guaranteed to pass the same limits on re-import.
const exportArchiveOverhead = 2
const exportMaxItems = archive.MaxFiles - exportArchiveOverhead

// ExportAll decrypts every item the actor may export: their personal items
// plus shared items they created — current versions only (decision D10).
// The administrator holds no personal-vault privilege, so an admin exporting
// sees only their own data.
func (s *Service) ExportAll(ctx context.Context, actor *auth.Principal) ([]ExportItem, error) {
	if actor == nil {
		return nil, ErrForbidden
	}
	// List intentionally includes every shared item readable by the caller.
	// Export is narrower: only personal items owned by the caller and shared
	// items created by the caller are candidates. Apply the limit only after
	// this ownership predicate, and fetch one extra row to detect overflow.
	where := `deleted_at IS NULL AND
		((vault_scope='personal' AND owner_user_id=?) OR
		 (vault_scope='shared' AND created_by_user_id=?))`
	rows, err := s.repo.listRows(ctx, s.db, where, []any{actor.UserID, actor.UserID}, "", "", exportMaxItems+1)
	if err != nil {
		return nil, err
	}
	if len(rows) > exportMaxItems {
		return nil, ErrExportTooLarge
	}
	out := make([]ExportItem, 0, len(rows))
	for _, row := range rows {
		// The SQL predicate is the candidate narrowing step; keep the policy
		// check explicit so a future query change cannot broaden exports.
		if !Can(Role(actor.Role), actor.UserID, row.policyView(), ActionExport) {
			continue
		}
		_, typed, envelope, err := s.decryptRow(row)
		if err != nil {
			return nil, err
		}
		meta := row.toMeta()
		meta.Title = TitleOf(typed)
		out = append(out, ExportItem{Meta: meta, Tags: envelope.Tags, Payload: typed})
	}
	return out, nil
}

// ImportItem is one decrypted item to store on import. Scope decides the
// ownership mapping: personal items belong to the importer; shared items
// keep the importer as their creator (decision D10).
type ImportItem struct {
	OriginalID               string
	ItemType                 string
	Scope                    string
	Tags                     []string
	Favorite                 bool
	AllowEmptySecureNoteBody bool
	Payload                  json.RawMessage
}

// ImportTxHook runs after validation and conflict mapping, inside the same
// transaction immediately before imported rows are inserted. Transfer uses
// this narrow callback to validate the originating session without making the
// vault package depend on authentication policy.
type ImportTxHook func(context.Context, *sql.Tx) error

// billingReference extracts the only cross-item reference currently carried
// by a vault payload. decodePayload has already checked the type grammar; this
// helper is deliberately small so import validation and rewriting use the
// same field without accepting arbitrary account material.
func billingReference(payload json.RawMessage) (string, bool) {
	var obj struct {
		BillingAddressItemID *string `json:"billing_address_item_id"`
	}
	if err := json.Unmarshal(payload, &obj); err != nil || obj.BillingAddressItemID == nil || *obj.BillingAddressItemID == "" {
		return "", false
	}
	return *obj.BillingAddressItemID, true
}

// ImportAll stores a set of imported items in ONE transaction. IDs that
// already exist are re-issued as fresh UUIDv7 items; address references
// pointing into the import set are remapped to the new IDs, references
// outside it are cleared (the preview listed them for the user to fill in).
// Every payload is re-validated and re-encrypted under the importer's
// ownership.
func (s *Service) ImportAll(ctx context.Context, actor *auth.Principal, items []ImportItem) (imported int, remapped int, err error) {
	return s.ImportAllWithHook(ctx, actor, items, nil)
}

// ImportAllWithHook is ImportAll with an optional transaction-local check.
// The hook executes before the first insert and any hook error rolls back the
// transaction, preserving retryability when commit definitely did not begin.
func (s *Service) ImportAllWithHook(ctx context.Context, actor *auth.Principal, items []ImportItem, hook ImportTxHook) (imported int, remapped int, err error) {
	if actor == nil {
		return 0, 0, ErrForbidden
	}
	// Validate every item and the complete reference graph before opening the
	// write transaction. References are checked against archive metadata, not
	// just their string presence: an internal target must be an identity, and
	// a shared source cannot point at a personal target. This makes malformed
	// archives fail as a unit before any row can be inserted.
	archiveItems := make(map[string]ImportItem, len(items))
	for i := range items {
		if !ItemTypes[items[i].ItemType] {
			return 0, 0, fmt.Errorf("%w: unknown item type at position %d", ErrPayloadInvalid, i)
		}
		if !Scopes[items[i].Scope] {
			return 0, 0, fmt.Errorf("%w: unknown scope at position %d", ErrPayloadInvalid, i)
		}
		if _, err := decodePayloadWithOptions(items[i].ItemType, items[i].Payload, items[i].AllowEmptySecureNoteBody); err != nil {
			return 0, 0, fmt.Errorf("payload %d: %w", i, err)
		}
		if _, err := validateTags(items[i].Tags); err != nil {
			return 0, 0, fmt.Errorf("tags %d: %w", i, err)
		}
		if items[i].OriginalID != "" {
			if _, duplicate := archiveItems[items[i].OriginalID]; duplicate {
				return 0, 0, fmt.Errorf("%w: duplicate imported item id", ErrPayloadInvalid)
			}
			archiveItems[items[i].OriginalID] = items[i]
		}
	}
	for i := range items {
		ref, ok := billingReference(items[i].Payload)
		if !ok {
			continue
		}
		target, inArchive := archiveItems[ref]
		if !inArchive {
			continue // external references are cleared below by design.
		}
		if target.ItemType != TypeIdentity ||
			(items[i].Scope == string(ScopeShared) && target.Scope != string(ScopeShared)) {
			return 0, 0, fmt.Errorf("%w: imported billing reference is not allowed", ErrReferenceForbidden)
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	// Pass 1: final IDs — the original ID when free, a fresh UUIDv7 on a
	// conflict (design D10: conflicting IDs are re-issued, never overwritten).
	idMap := make(map[string]string, len(items))
	finalIDs := make([]string, len(items))
	at := s.now().UTC().Format(TimestampFormat)
	for i := range items {
		original := items[i].OriginalID
		final := original
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM vault_items WHERE id=?`, original).Scan(&exists); err != nil {
			return 0, 0, err
		}
		if exists > 0 || original == "" {
			final = ident.NewUUIDv7()
		}
		finalIDs[i] = final
		if original != "" {
			idMap[original] = final
		}
	}

	// Pass 2: rewrite billing references into the import set; clear ones
	// pointing outside it.
	for i := range items {
		if items[i].ItemType != TypeCreditCard {
			continue
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(items[i].Payload, &obj); err != nil {
			return 0, 0, fmt.Errorf("payload %d: %w", i, err)
		}
		refRaw, ok := obj["billing_address_item_id"]
		if !ok || string(refRaw) == "null" {
			continue
		}
		var ref string
		if err := json.Unmarshal(refRaw, &ref); err != nil || ref == "" {
			continue
		}
		if newID, inSet := idMap[ref]; inSet && newID != ref {
			obj["billing_address_item_id"], _ = json.Marshal(newID)
			remapped++
		} else if _, known := idMap[ref]; !known {
			// Outside the archive: the preview flagged it; clear on import.
			obj["billing_address_item_id"] = json.RawMessage("null")
		}
		out, err := json.Marshal(obj)
		if err != nil {
			return 0, 0, err
		}
		items[i].Payload = out
	}
	if hook != nil {
		if err := hook(ctx, tx); err != nil {
			return 0, 0, err
		}
	}

	// Pass 3: encrypt and insert.
	for i := range items {
		payload, err := decodePayloadWithOptions(items[i].ItemType, items[i].Payload, items[i].AllowEmptySecureNoteBody)
		if err != nil {
			return 0, 0, err
		}
		tags, err := validateTags(items[i].Tags)
		if err != nil {
			return 0, 0, err
		}
		scope := items[i].Scope
		ownerID, creatorID := "", ""
		if scope == string(ScopePersonal) {
			ownerID = actor.UserID
		} else {
			creatorID = actor.UserID
		}
		_, plaintext, err := buildEnvelope(items[i].ItemType, tags, payload)
		if err != nil {
			return 0, 0, err
		}
		id := finalIDs[i]
		enc, err := s.key.Encrypt(plaintext, AADFor(id, scope, ownerID, creatorID, crypto.PayloadVersion, 1))
		if err != nil {
			return 0, 0, err
		}
		row := itemRow{
			ID: id, Scope: scope,
			OwnerID:   sql.NullString{String: ownerID, Valid: ownerID != ""},
			CreatorID: sql.NullString{String: creatorID, Valid: creatorID != ""},
			ItemType:  items[i].ItemType, Favorite: items[i].Favorite,
			PayloadVersion: enc.Version, Nonce: enc.Nonce[:], Ciphertext: enc.Ciphertext,
			Revision: 1, CreatedAt: at, UpdatedAt: at,
		}
		if err := s.repo.insertItem(ctx, tx, row); err != nil {
			return 0, 0, err
		}
		imported++
	}
	if err := s.audit.Record(ctx, tx, audit.Event{
		Name: audit.EventTransferImported, ActorID: actor.UserID,
		TargetType: audit.TargetUser, TargetID: actor.UserID, Result: audit.ResultSuccess,
	}); err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("%w: %v", ErrImportCommitUnknown, err)
	}
	return imported, remapped, nil
}

// IDExists reports whether an item row with this ID exists (import conflict
// detection). It leaks nothing else.
func (s *Service) IDExists(ctx context.Context, id string) bool {
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM vault_items WHERE id=?`, id).Scan(&exists); err != nil {
		return false
	}
	return exists > 0
}
