package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/tiny-password/tiny-password/internal/auth"
	"github.com/tiny-password/tiny-password/internal/platform/sqlite"
)

const sessionCookieName = "tiny_password_session"

type AuthDeps struct {
	Service              *auth.Service
	CSRF                 *PreAuthCSRF
	AllowInsecureCookies bool
}
type principalContextKey struct{}

func CurrentPrincipal(ctx context.Context) *auth.Principal {
	p, _ := ctx.Value(principalContextKey{}).(*auth.Principal)
	return p
}
func sessionToken(r *http.Request) string {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}
	return cookie.Value
}

// RequireSession checks live database state on every request. Polling does not
// renew sessions. Only explicit allowlisted handlers permit first-login users.
func RequireSession(service *auth.Service, allowPasswordChange bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		token := sessionToken(r)
		p, err := service.Authenticate(r.Context(), token)
		if err != nil {
			writeAuthError(w, err)
			return
		}
		if p.MustChangePassword && !allowPasswordChange {
			writeAuthError(w, auth.ErrPasswordChangeRequired)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			if !originAllowed(r) || !auth.VerifyCSRF(token, r.Header.Get("X-CSRF-Token")) {
				writeJSONError(w, 403, "FORBIDDEN", "missing or invalid CSRF context")
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalContextKey{}, p)))
	})
}
func registerAuth(api *http.ServeMux, deps AuthDeps) {
	guarded := func(pattern string, allow bool, handler http.HandlerFunc) {
		api.Handle(pattern, RequireSession(deps.Service, allow, handler))
	}
	api.HandleFunc("POST /api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		if !deps.CSRF.Verify(r) || !originAllowed(r) {
			writeJSONError(w, 403, "FORBIDDEN", "missing or invalid CSRF context")
			return
		}
		var input struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if !decodeAuthBody(w, r, &input) {
			return
		}
		result, err := deps.Service.Login(r.Context(), input.Username, input.Password, sourceKey(r))
		if err != nil {
			writeAuthError(w, err)
			return
		}
		deps.CSRF.Consume(r)
		// A pre-auth context never becomes the authenticated CSRF context.
		http.SetCookie(w, &http.Cookie{Name: preAuthCookieName, Value: "", Path: "/", HttpOnly: true, Secure: !deps.AllowInsecureCookies, SameSite: http.SameSiteLaxMode, MaxAge: -1})
		deps.setSession(w, result.Token)
		writeJSON(w, 200, map[string]any{"must_change_password": result.Principal.MustChangePassword, "csrf_token": auth.CSRFToken(result.Token), "user": result.Principal})
	})
	guarded("GET /api/v1/auth/session", true, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"user": CurrentPrincipal(r.Context()), "csrf_token": auth.CSRFToken(sessionToken(r))})
	})
	guarded("POST /api/v1/auth/logout", true, func(w http.ResponseWriter, r *http.Request) {
		if err := deps.Service.Logout(r.Context(), sessionToken(r)); err != nil {
			writeAuthError(w, err)
			return
		}
		deps.clearSession(w)
		w.WriteHeader(204)
	})
	guarded("POST /api/v1/auth/password", true, func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Current string `json:"current_password"`
			Next    string `json:"new_password"`
		}
		if !decodeAuthBody(w, r, &input) {
			return
		}
		result, err := deps.Service.ChangePassword(r.Context(), sessionToken(r), input.Current, input.Next, sourceKey(r))
		if err != nil {
			writeAuthError(w, err)
			return
		}
		deps.setSession(w, result.Token)
		w.Header().Set("X-CSRF-Token", auth.CSRFToken(result.Token))
		w.WriteHeader(204)
	})
	guarded("GET /api/v1/auth/sessions", false, func(w http.ResponseWriter, r *http.Request) {
		items, err := deps.Service.ListSessions(r.Context(), sessionToken(r))
		if err != nil {
			writeAuthError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"items": items, "next_cursor": nil})
	})
	guarded("DELETE /api/v1/auth/sessions/{sessionId}", false, func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("sessionId")
		if err := deps.Service.RevokeSession(r.Context(), sessionToken(r), id); err != nil {
			writeAuthError(w, err)
			return
		}
		if id == CurrentPrincipal(r.Context()).Session.ID {
			deps.clearSession(w)
		}
		w.WriteHeader(204)
	})
	guarded("POST /api/v1/auth/session/activity", false, func(w http.ResponseWriter, r *http.Request) {
		if err := deps.Service.Touch(r.Context(), sessionToken(r)); err != nil {
			writeAuthError(w, err)
			return
		}
		w.WriteHeader(204)
	})
	guarded("PATCH /api/v1/auth/preferences", false, func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			IdleTimeoutMinutes int `json:"idle_timeout_minutes"`
		}
		if !decodeAuthBody(w, r, &input) {
			return
		}
		if err := deps.Service.SetIdleTimeout(r.Context(), sessionToken(r), input.IdleTimeoutMinutes); err != nil {
			writeAuthError(w, err)
			return
		}
		w.WriteHeader(204)
	})
}
func (d AuthDeps) setSession(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: token, Path: "/", HttpOnly: true, Secure: !d.AllowInsecureCookies, SameSite: http.SameSiteLaxMode, MaxAge: int(auth.AbsoluteLifetime.Seconds())})
}
func (d AuthDeps) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", HttpOnly: true, Secure: !d.AllowInsecureCookies, SameSite: http.SameSiteLaxMode, MaxAge: -1})
}
func decodeAuthBody(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeJSONError(w, 400, "VALIDATION_ERROR", "invalid request body")
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeJSONError(w, 400, "VALIDATION_ERROR", "invalid request body")
		return false
	}
	return true
}
func writeAuthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrUnauthorized):
		writeJSONError(w, 401, "UNAUTHORIZED", "invalid credentials or session")
	case errors.Is(err, auth.ErrAccountDisabled):
		writeJSONError(w, 403, "ACCOUNT_DISABLED", "account disabled")
	case errors.Is(err, auth.ErrPasswordChangeRequired):
		writeJSONError(w, 403, "PASSWORD_CHANGE_REQUIRED", "change your password before continuing")
	case errors.Is(err, auth.ErrRateLimited):
		w.Header().Set("Retry-After", "60")
		writeJSONError(w, 429, "RATE_LIMITED", "too many attempts; slow down")
	case errors.Is(err, auth.ErrNotFound):
		writeJSONError(w, 404, "NOT_FOUND", "session not found")
	case errors.Is(err, auth.ErrPasswordPolicy):
		writeJSONError(w, 400, "VALIDATION_ERROR", policyMessage(err))
	case errors.Is(err, auth.ErrInvalidIdleTimeout):
		writeJSONError(w, 400, "VALIDATION_ERROR", err.Error())
	case sqlite.IsBusy(err):
		writeJSONError(w, 503, "DATABASE_BUSY", "database temporarily busy; retry later")
	default:
		writeJSONError(w, 500, "INTERNAL", "authentication request failed")
	}
}
