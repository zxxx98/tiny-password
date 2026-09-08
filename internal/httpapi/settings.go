package httpapi

import (
	"net/http"

	"github.com/tiny-password/tiny-password/internal/auth"
	"github.com/tiny-password/tiny-password/internal/scheduler"
	"github.com/tiny-password/tiny-password/internal/settings"
)

// SettingsDeps wires the admin system-settings endpoints (T27): version,
// readiness, scheduler outcomes, and the non-sensitive settings whitelist.
// Secrets are injected via environment variables or Secret files and are
// never included, echoed, or downloadable here.
type SettingsDeps struct {
	Settings  *settings.Service
	Session   *auth.Service
	Scheduler *scheduler.Scheduler
	Version   string
	// Ready reports the readiness checks (same set as /readyz).
	Ready func() (map[string]bool, bool)
	// R2CredentialsPresent reports whether both R2 credentials are configured
	// (presence only — never the values). Nil reports false.
	R2CredentialsPresent func() bool
}

func registerSettings(api *http.ServeMux, deps SettingsDeps) {
	admin := func(handler func(w http.ResponseWriter, r *http.Request)) http.Handler {
		return RequireAdmin(deps.Session, false, http.HandlerFunc(handler))
	}

	api.Handle("GET /api/v1/admin/settings", admin(func(w http.ResponseWriter, r *http.Request) {
		st, err := deps.Settings.Get(r.Context())
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL", "the settings are unavailable")
			return
		}
		var checks map[string]bool
		ready := false
		if deps.Ready != nil {
			checks, ready = deps.Ready()
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"version":  deps.Version,
			"ready":    ready,
			"checks":   checks,
			"settings": st,
			// Presence flag only — never the credential values.
			"r2_credentials_configured": deps.R2CredentialsPresent != nil && deps.R2CredentialsPresent(),
			"scheduler":                 deps.Scheduler.Results(),
		})
	}))

	// Update from an explicit non-sensitive whitelist; unknown or sensitive
	// fields cannot be expressed through the settings struct.
	api.Handle("PUT /api/v1/admin/settings", admin(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			R2Endpoint string `json:"r2_endpoint"`
			R2Bucket   string `json:"r2_bucket"`
			R2Prefix   string `json:"r2_prefix"`
		}
		if !decodeJSONBody(w, r, &input, authMaxBodyBytes) {
			return
		}
		st := settings.Settings{
			R2Endpoint: input.R2Endpoint,
			R2Bucket:   input.R2Bucket,
			R2Prefix:   input.R2Prefix,
		}
		if err := deps.Settings.Update(r.Context(), st); err != nil {
			writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "the settings are invalid")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"updated": true})
	}))
}
