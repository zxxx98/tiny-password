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
	// Tags live inside the ciphertext: filtering by tag requires the
	// decrypt-and-filter pipeline of the search milestone (T13). Fail closed
	// instead of silently returning unfiltered results.
	if query.Get("tag") != "" {
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "tag filtering is not available yet")
		return
	}
	// The filter identity binds cursors to this exact filter combination.
	filters := itemsListFilters + ":scope=" + filter.Scope + ":type=" + filter.ItemType
	if filter.Favorite != nil {
		filters += ":favorite=" + strconv.FormatBool(*filter.Favorite)
	}

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
