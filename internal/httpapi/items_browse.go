package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/tiny-password/tiny-password/internal/vault"
)

func (d ItemsDeps) browse(w http.ResponseWriter, r *http.Request) {
	var body struct {
		vault.BrowseInput
		Cursor string `json:"cursor"`
		Limit  *int   `json:"limit"`
	}
	if !decodeJSONBody(w, r, &body, authMaxBodyBytes) {
		return
	}
	if runeLen(body.Query) > 256 || runeLen(body.Tag) > 64 {
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "query or tag exceeds the allowed length")
		return
	}
	limit := defaultPageSize
	if body.Limit != nil {
		if *body.Limit < 1 || *body.Limit > maxPageSize {
			writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", pageSizeTooLarge)
			return
		}
		limit = *body.Limit
	}
	principal := CurrentPrincipal(r.Context())
	// JSON keeps arbitrary query/tag punctuation from colliding in bindings.
	canonical, _ := json.Marshal(body.BrowseInput)
	digest := sha256.Sum256(canonical)
	filters := "v1:browse:" + hex.EncodeToString(digest[:])
	var afterKey, afterID string
	if body.Cursor != "" {
		position, err := d.Cursor.Decode(body.Cursor, principal.UserID, filters)
		if err != nil || len(position) != 2 {
			writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "invalid or expired cursor")
			return
		}
		afterKey, afterID = position[0], position[1]
	}
	items, err := d.Service.Browse(r.Context(), principal, body.BrowseInput, afterKey, afterID, limit+1)
	if err != nil {
		writeItemsError(w, r, err)
		return
	}
	var next any
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		next = d.Cursor.Encode(principal.UserID, filters, []string{vault.BrowseKey(last, body.Sort), last.ID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

// Tag options are loaded separately so ordinary pagination never scans the vault.
func (d ItemsDeps) browseTags(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Scope    string `json:"scope"`
		Type     string `json:"type"`
		Favorite *bool  `json:"favorite,omitempty"`
	}
	if !decodeJSONBody(w, r, &body, authMaxBodyBytes) {
		return
	}
	tags, err := d.Service.BrowseTags(r.Context(), CurrentPrincipal(r.Context()), vault.BrowseInput{Scope: body.Scope, Type: body.Type, Favorite: body.Favorite})
	if err != nil {
		writeItemsError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": tags})
}
