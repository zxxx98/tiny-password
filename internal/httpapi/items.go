package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/tiny-password/tiny-password/internal/auth"
	"github.com/tiny-password/tiny-password/internal/idempotency"
	"github.com/tiny-password/tiny-password/internal/platform/sqlite"
	"github.com/tiny-password/tiny-password/internal/requestid"
	"github.com/tiny-password/tiny-password/internal/vault"
)

// ItemsDeps wires the vault item endpoints (T11).
type ItemsDeps struct {
	Service     *vault.Service
	Session     *auth.Service
	Cursor      *CursorCodec
	Idempotency *idempotency.Service
}

const itemsCreateScope = "items.create"

// itemsMaxBodyBytes bounds item JSON bodies: the plaintext envelope is
// capped at 256 KiB (D09) and this adds headroom for tags and structure.
const itemsMaxBodyBytes = 288 << 10

// itemsListFilters is the cursor filter identity; concrete filter values are
// appended so a cursor can never be replayed under different filters.
const itemsListFilters = "v1:items"

const (
	itemsTrashFilters   = "v1:trash"
	itemsHistoryFilters = "v1:history:"
	itemsSearchFilters  = "v1:search"
)

// runeLen counts Unicode code points, the unit the OpenAPI string limits use.
func runeLen(s string) int { return len([]rune(s)) }

type itemCreateRequest struct {
	ItemType   string          `json:"item_type"`
	VaultScope string          `json:"vault_scope"`
	Tags       []string        `json:"tags"`
	Favorite   bool            `json:"favorite"`
	Payload    json.RawMessage `json:"payload"`
}

type itemUpdateRequest struct {
	Revision uint64          `json:"revision"`
	Tags     *[]string       `json:"tags"`
	Favorite *bool           `json:"favorite"`
	Payload  json.RawMessage `json:"payload"`
}

func registerItems(api *http.ServeMux, deps ItemsDeps) {
	guarded := func(handler http.HandlerFunc) http.Handler {
		return RequireSession(deps.Session, false, handler)
	}

	api.Handle("POST /api/v1/items", guarded(func(w http.ResponseWriter, r *http.Request) {
		deps.create(w, r)
	}))

	api.Handle("GET /api/v1/items", guarded(func(w http.ResponseWriter, r *http.Request) {
		deps.list(w, r)
	}))

	api.Handle("GET /api/v1/items/{itemId}", guarded(func(w http.ResponseWriter, r *http.Request) {
		detail, err := deps.Service.Get(r.Context(), CurrentPrincipal(r.Context()), r.PathValue("itemId"))
		if err != nil {
			writeItemsError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, detail)
	}))

	api.Handle("PUT /api/v1/items/{itemId}", guarded(func(w http.ResponseWriter, r *http.Request) {
		principal := CurrentPrincipal(r.Context())
		var input itemUpdateRequest
		if !decodeJSONBody(w, r, &input, itemsMaxBodyBytes) {
			return
		}
		if len(input.Payload) == 0 {
			writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "payload is required")
			return
		}
		update := vault.UpdateInput{Revision: input.Revision, Tags: input.Tags, Favorite: input.Favorite, Payload: input.Payload}
		detail, err := deps.Service.Update(r.Context(), principal, r.PathValue("itemId"), update)
		if err != nil {
			writeItemsError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, detail)
	}))

	// Trash lifecycle (T12): delete marks deleted_at only; restore brings
	// the item back unchanged; purge removes it permanently.
	api.Handle("DELETE /api/v1/items/{itemId}", guarded(func(w http.ResponseWriter, r *http.Request) {
		if err := deps.Service.Trash(r.Context(), CurrentPrincipal(r.Context()), r.PathValue("itemId")); err != nil {
			writeItemsError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	api.Handle("POST /api/v1/items/{itemId}/restore", guarded(func(w http.ResponseWriter, r *http.Request) {
		detail, err := deps.Service.RestoreTrashed(r.Context(), CurrentPrincipal(r.Context()), r.PathValue("itemId"))
		if err != nil {
			writeItemsError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, detail)
	}))

	api.Handle("DELETE /api/v1/items/{itemId}/purge", guarded(func(w http.ResponseWriter, r *http.Request) {
		if err := deps.Service.Purge(r.Context(), CurrentPrincipal(r.Context()), r.PathValue("itemId")); err != nil {
			writeItemsError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	// Search (T13): POST keeps the query out of URLs; CSRF and no-store come
	// from the session guard and error writer. Cursor and limit arrive in the
	// JSON body, so this handler decodes them itself.
	api.Handle("POST /api/v1/items/search", guarded(func(w http.ResponseWriter, r *http.Request) {
		deps.search(w, r)
	}))

	// Password health over the caller's readable login items (T13).
	api.Handle("GET /api/v1/items/health", guarded(func(w http.ResponseWriter, r *http.Request) {
		report, err := deps.Service.Health(r.Context(), CurrentPrincipal(r.Context()))
		if err != nil {
			writeItemsError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, report)
	}))

	// Favorite and tags are item writes (D06): personal owner or shared
	// creator only, revision-locked like any other update.
	api.Handle("PUT /api/v1/items/{itemId}/favorite", guarded(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Favorite *bool `json:"favorite"`
		}
		if !decodeJSONBody(w, r, &input, authMaxBodyBytes) {
			return
		}
		if input.Favorite == nil {
			writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "favorite is required")
			return
		}
		if err := deps.Service.SetFavorite(r.Context(), CurrentPrincipal(r.Context()), r.PathValue("itemId"), *input.Favorite); err != nil {
			writeItemsError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	api.Handle("PUT /api/v1/items/{itemId}/tags", guarded(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Tags []string `json:"tags"`
		}
		if !decodeJSONBody(w, r, &input, authMaxBodyBytes) {
			return
		}
		if err := deps.Service.SetTags(r.Context(), CurrentPrincipal(r.Context()), r.PathValue("itemId"), input.Tags); err != nil {
			writeItemsError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	// The trash listing shows the caller's readable trashed items.
	api.Handle("GET /api/v1/items/trash", guarded(func(w http.ResponseWriter, r *http.Request) {
		principal := CurrentPrincipal(r.Context())
		beforeCreated, beforeID, limit, ok := DecodeCursorParams(w, r, deps.Cursor, principal.UserID, itemsTrashFilters)
		if !ok {
			return
		}
		metas, err := deps.Service.ListTrash(r.Context(), principal, beforeCreated, beforeID, limit+1)
		if err != nil {
			writeItemsError(w, r, err)
			return
		}
		var lastCreated, lastID string
		if len(metas) > limit {
			metas = metas[:limit]
			lastCreated, lastID = metas[len(metas)-1].UpdatedAt, metas[len(metas)-1].ID
		}
		writeJSON(w, http.StatusOK, CursorPageResponse(deps.Cursor, principal.UserID, itemsTrashFilters, metas, lastCreated, lastID))
	}))

	// Encrypted history: readable by whoever can read the item.
	api.Handle("GET /api/v1/items/{itemId}/history", guarded(func(w http.ResponseWriter, r *http.Request) {
		principal := CurrentPrincipal(r.Context())
		itemID := r.PathValue("itemId")
		filters := itemsHistoryFilters + itemID
		beforeRevision, _, limit, ok := DecodeCursorParams(w, r, deps.Cursor, principal.UserID, filters)
		if !ok {
			return
		}
		entries, err := deps.Service.ListHistory(r.Context(), principal, itemID, beforeRevision, limit+1)
		if err != nil {
			writeItemsError(w, r, err)
			return
		}
		var lastRevision string
		if len(entries) > limit {
			entries = entries[:limit]
			lastRevision = strconv.FormatUint(entries[len(entries)-1].Revision, 10)
		}
		writeJSON(w, http.StatusOK, CursorPageResponse(deps.Cursor, principal.UserID, filters, entries, lastRevision, ""))
	}))

	// Restoring a history version re-encrypts it as a new current revision.
	api.Handle("POST /api/v1/items/{itemId}/history/{revision}/restore", guarded(func(w http.ResponseWriter, r *http.Request) {
		revision, err := strconv.ParseUint(r.PathValue("revision"), 10, 64)
		if err != nil || revision == 0 {
			writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "invalid revision")
			return
		}
		detail, err := deps.Service.RestoreHistory(r.Context(), CurrentPrincipal(r.Context()), r.PathValue("itemId"), revision)
		if err != nil {
			writeItemsError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, detail)
	}))
}

// create implements POST /items with optional create idempotency. A replay
// re-renders the originally created item after the session guard and the
// service re-verified the caller's read access.
func (d ItemsDeps) create(w http.ResponseWriter, r *http.Request) {
	principal := CurrentPrincipal(r.Context())
	var input itemCreateRequest
	if !decodeJSONBody(w, r, &input, itemsMaxBodyBytes) {
		return
	}
	if len(input.Payload) == 0 {
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "payload is required")
		return
	}

	var claim *idempotency.Claim
	if d.Idempotency != nil {
		key, present, invalid := IdempotencyKey(r)
		if invalid {
			writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "invalid idempotency key")
			return
		}
		if present {
			scope := ScopeFor(itemsCreateScope, principal.UserID)
			fp := d.createFingerprint(input)
			c, outcome, err := d.Idempotency.Claim(r.Context(), scope, key, fp)
			if err != nil {
				writeError(w, r, http.StatusInternalServerError, "INTERNAL", "the request could not be processed")
				return
			}
			switch outcome {
			case idempotency.OutcomeFresh:
				claim = c
			case idempotency.OutcomeInFlight:
				writeError(w, r, http.StatusConflict, "CONFLICT", "an identical request is still in progress")
				return
			case idempotency.OutcomeConflict:
				writeError(w, r, http.StatusConflict, "IDEMPOTENCY_KEY_CONFLICT", "this idempotency key was used with different content")
				return
			default: // Replay
				resourceID, err := d.Idempotency.ReplayResourceID(r.Context(), scope, key, fp)
				if err != nil {
					writeError(w, r, http.StatusConflict, "CONFLICT", "idempotency record is unavailable")
					return
				}
				detail, err := d.Service.Get(r.Context(), principal, resourceID)
				if err != nil {
					writeError(w, r, http.StatusConflict, "CONFLICT", "the original resource no longer exists")
					return
				}
				writeJSON(w, http.StatusCreated, detail)
				return
			}
		}
	}

	detail, err := d.Service.Create(r.Context(), principal, vault.CreateInput{
		ItemType: input.ItemType,
		Scope:    input.VaultScope,
		Tags:     input.Tags,
		Favorite: input.Favorite,
		Payload:  input.Payload,
	}, claim)
	if err != nil {
		claim.Release(r.Context())
		writeItemsError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, detail)
}

// createFingerprint derives the keyed fingerprint from the request's
// semantic fields. json.Marshal compacts the raw payload so whitespace-only
// differences still replay; any real content change (including reordered
// object keys) counts as different content and conflicts.
func (d ItemsDeps) createFingerprint(input itemCreateRequest) string {
	canonical, err := json.Marshal(input.Payload)
	if err != nil {
		canonical = input.Payload
	}
	tags := strings.Join(input.Tags, "\x00")
	return d.Idempotency.Fingerprint(itemsCreateScope, input.ItemType, input.VaultScope,
		strconv.FormatBool(input.Favorite), tags, string(canonical))
}

// search implements POST /items/search with a body-carried cursor and limit.
func (d ItemsDeps) search(w http.ResponseWriter, r *http.Request) {
	principal := CurrentPrincipal(r.Context())
	var body struct {
		Query  string `json:"query"`
		Scope  string `json:"scope"`
		Type   string `json:"type"`
		Tag    string `json:"tag"`
		Cursor string `json:"cursor"`
		Limit  *int   `json:"limit"`
	}
	if !decodeJSONBody(w, r, &body, authMaxBodyBytes) {
		return
	}
	if strings.TrimSpace(body.Query) == "" {
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "query is required")
		return
	}
	if runeLen(body.Query) > 256 {
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "query must be at most 256 characters")
		return
	}
	if runeLen(body.Tag) > 64 {
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "tag must be at most 64 characters")
		return
	}
	// The query itself is part of the cursor binding: a cursor never leaks
	// results across different searches.
	filters := itemsSearchFilters + ":q=" + body.Query + ":scope=" + body.Scope + ":type=" + body.Type + ":tag=" + body.Tag
	limit := defaultPageSize
	if body.Limit != nil {
		if *body.Limit < 1 || *body.Limit > maxPageSize {
			writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", pageSizeTooLarge)
			return
		}
		limit = *body.Limit
	}
	var beforeUpdated, beforeID string
	if body.Cursor != "" {
		sort, err := d.Cursor.Decode(body.Cursor, principal.UserID, filters)
		if err != nil || len(sort) != 2 {
			writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "invalid or expired cursor")
			return
		}
		beforeUpdated, beforeID = sort[0], sort[1]
	}
	metas, err := d.Service.Search(r.Context(), principal, vault.SearchInput{
		Query: body.Query, Scope: body.Scope, Type: body.Type, Tag: body.Tag,
	}, beforeUpdated, beforeID, limit+1)
	if err != nil {
		writeItemsError(w, r, err)
		return
	}
	var lastCreated, lastID string
	if len(metas) > limit {
		metas = metas[:limit]
		lastCreated, lastID = metas[len(metas)-1].UpdatedAt, metas[len(metas)-1].ID
	}
	writeJSON(w, http.StatusOK, CursorPageResponse(d.Cursor, principal.UserID, filters, metas, lastCreated, lastID))
}

func (d ItemsDeps) list(w http.ResponseWriter, r *http.Request) {
	principal := CurrentPrincipal(r.Context())
	query := r.URL.Query()

	filter := vault.ListFilter{
		Scope:    query.Get("scope"),
		ItemType: query.Get("type"),
	}
	if raw := query.Get("favorite"); raw != "" {
		favorite, err := strconv.ParseBool(raw)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "favorite must be true or false")
			return
		}
		filter.Favorite = &favorite
	}
	// Tags live inside the ciphertext; a tag filter switches the list to the
	// decrypt-and-filter scanner (T13). The tag participates in the cursor
	// filter identity below.
	filter.Tag = query.Get("tag")
	if runeLen(filter.Tag) > 64 {
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "tag must be at most 64 characters")
		return
	}
	// The filter identity binds cursors to this exact filter combination.
	filters := itemsListFilters + ":scope=" + filter.Scope + ":type=" + filter.ItemType
	if filter.Favorite != nil {
		filters += ":favorite=" + strconv.FormatBool(*filter.Favorite)
	}
	filters += ":tag=" + filter.Tag

	beforeCreated, beforeID, limit, ok := DecodeCursorParams(w, r, d.Cursor, principal.UserID, filters)
	if !ok {
		return
	}
	metas, err := d.Service.List(r.Context(), principal, filter, beforeCreated, beforeID, limit+1)
	if err != nil {
		writeItemsError(w, r, err)
		return
	}
	var lastCreated, lastID string
	if len(metas) > limit {
		metas = metas[:limit]
		lastCreated, lastID = metas[len(metas)-1].UpdatedAt, metas[len(metas)-1].ID
	}
	writeJSON(w, http.StatusOK, CursorPageResponse(d.Cursor, principal.UserID, filters, metas, lastCreated, lastID))
}

// writeRevisionConflict renders the REVISION_CONFLICT envelope including the
// current revision so clients can reload and re-apply.
func writeRevisionConflict(w http.ResponseWriter, r *http.Request, currentRevision uint64) {
	id := requestid.FromContext(r.Context())
	if id == "" {
		id = requestid.New()
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":             "REVISION_CONFLICT",
		"message":          "the item was updated by someone else; reload and reapply",
		"request_id":       id,
		"current_revision": currentRevision,
	})
}

func writeItemsError(w http.ResponseWriter, r *http.Request, err error) {
	var conflict *vault.RevisionConflictError
	switch {
	case errors.Is(err, vault.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "item not found")
	case errors.Is(err, vault.ErrForbidden):
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "the item is read-only for this account")
	case errors.As(err, &conflict):
		writeRevisionConflict(w, r, conflict.CurrentRevision)
	case errors.Is(err, vault.ErrPayloadTooLarge):
		writeError(w, r, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "the item payload exceeds the allowed size")
	case errors.Is(err, vault.ErrReferenceForbidden):
		writeError(w, r, http.StatusConflict, "REFERENCE_FORBIDDEN", "the referenced item is not available for this entry")
	case errors.Is(err, vault.ErrInvalidItemType):
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "unknown item type")
	case errors.Is(err, vault.ErrInvalidScope):
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "unknown vault scope")
	case errors.Is(err, vault.ErrPayloadInvalid):
		msg := err.Error()
		if unwrapped := errors.Unwrap(err); unwrapped != nil {
			msg = unwrapped.Error()
		}
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", msg)
	case sqlite.IsBusy(err):
		writeError(w, r, http.StatusServiceUnavailable, "DATABASE_BUSY", "database temporarily busy; retry later")
	default:
		writeError(w, r, http.StatusInternalServerError, "INTERNAL", "the item request could not be completed")
	}
}
