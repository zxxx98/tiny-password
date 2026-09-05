package httpapi

import (
	"net/http"
	"strconv"

	"github.com/tiny-password/tiny-password/internal/audit"
	"github.com/tiny-password/tiny-password/internal/auth"
	tpsqlite "github.com/tiny-password/tiny-password/internal/platform/sqlite"
)

// AuditDeps wires the read-side audit endpoints: personal activity (D08) and
// the admin-only system query. There are deliberately no write or delete
// endpoints for audit data.
type AuditDeps struct {
	Service *audit.Service
	DB      *tpsqlite.DB
	Cursor  *CursorCodec
	// Session guards the endpoints; first-login users cannot read activity.
	Session *auth.Service
}

const defaultPageSize = 50

// cursorParams extracts the decoded keyset position and effective page size.
// Cursor decoding is bound to the actor and the endpoint's filter identity.
func (d AuditDeps) cursorParams(w http.ResponseWriter, r *http.Request, actor, filters string) (beforeCreated, beforeID string, limit int, ok bool) {
	limit = defaultPageSize
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "limit must be between 1 and 100")
			return "", "", 0, false
		}
		limit = n
	}
	if token := r.URL.Query().Get("cursor"); token != "" {
		sort, err := d.Cursor.Decode(token, actor, filters)
		if err != nil || len(sort) != 2 {
			writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "invalid or expired cursor")
			return "", "", 0, false
		}
		beforeCreated, beforeID = sort[0], sort[1]
	}
	return beforeCreated, beforeID, limit, true
}

func pageResponse(codec *CursorCodec, actor, filters string, page audit.Page) map[string]any {
	var next any
	if page.LastCreated != "" {
		next = codec.Encode(actor, filters, []string{page.LastCreated, page.LastID})
	}
	return map[string]any{"items": page.Entries, "next_cursor": next}
}

func registerAudit(api *http.ServeMux, deps AuditDeps) {
	// Personal activity: the caller's own redacted events only (D08).
	api.Handle("GET /api/v1/auth/activity", RequireSession(deps.Session, false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := CurrentPrincipal(r.Context())
		const filters = "v1:activity"
		beforeCreated, beforeID, limit, ok := deps.cursorParams(w, r, p.UserID, filters)
		if !ok {
			return
		}
		page, err := deps.Service.Activity(r.Context(), deps.DB.DB, p.UserID, beforeCreated, beforeID, limit)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL", "the activity query failed")
			return
		}
		writeJSON(w, http.StatusOK, pageResponse(deps.Cursor, p.UserID, filters, page))
	})))

	// System audit: admin only (design §5.2).
	api.Handle("GET /api/v1/admin/audit", RequireAdmin(deps.Session, false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const filters = "v1:system"
		event := r.URL.Query().Get("event")
		if event != "" && !audit.ValidEventName(event) {
			writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "unknown event filter")
			return
		}
		p := CurrentPrincipal(r.Context())
		beforeCreated, beforeID, limit, ok := deps.cursorParams(w, r, p.UserID, filters)
		if !ok {
			return
		}
		page, err := deps.Service.System(r.Context(), deps.DB.DB, event, beforeCreated, beforeID, limit)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL", "the audit query failed")
			return
		}
		writeJSON(w, http.StatusOK, pageResponse(deps.Cursor, p.UserID, filters, page))
	})))
}

// RequireAdmin gates a handler behind an authenticated, first-login-completed
// admin session. Members receive the stable FORBIDDEN envelope.
func RequireAdmin(service *auth.Service, allowPasswordChange bool, next http.Handler) http.Handler {
	return RequireSession(service, allowPasswordChange, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := CurrentPrincipal(r.Context())
		if p == nil || p.Role != "admin" {
			writeError(w, r, http.StatusForbidden, "FORBIDDEN", "administrator role required")
			return
		}
		next.ServeHTTP(w, r)
	}))
}
