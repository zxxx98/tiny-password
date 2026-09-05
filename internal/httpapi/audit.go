package httpapi

import (
	"net/http"

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

const (
	activityFilters = "v1:activity"
	systemFilters   = "v1:system"
)

func registerAudit(api *http.ServeMux, deps AuditDeps) {
	// Personal activity: the caller's own redacted events only (D08).
	api.Handle("GET /api/v1/auth/activity", RequireSession(deps.Session, false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := CurrentPrincipal(r.Context())
		beforeCreated, beforeID, limit, ok := DecodeCursorParams(w, r, deps.Cursor, p.UserID, activityFilters)
		if !ok {
			return
		}
		page, err := deps.Service.Activity(r.Context(), deps.DB.DB, p.UserID, beforeCreated, beforeID, limit)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL", "the activity query failed")
			return
		}
		writeJSON(w, http.StatusOK, CursorPageResponse(deps.Cursor, p.UserID, activityFilters, page.Entries, page.LastCreated, page.LastID))
	})))

	// System audit: admin only (design §5.2).
	api.Handle("GET /api/v1/admin/audit", RequireAdmin(deps.Session, false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		event := r.URL.Query().Get("event")
		if event != "" && !audit.ValidEventName(event) {
			writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "unknown event filter")
			return
		}
		p := CurrentPrincipal(r.Context())
		beforeCreated, beforeID, limit, ok := DecodeCursorParams(w, r, deps.Cursor, p.UserID, systemFilters)
		if !ok {
			return
		}
		page, err := deps.Service.System(r.Context(), deps.DB.DB, event, beforeCreated, beforeID, limit)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL", "the audit query failed")
			return
		}
		writeJSON(w, http.StatusOK, CursorPageResponse(deps.Cursor, p.UserID, systemFilters, page.Entries, page.LastCreated, page.LastID))
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
