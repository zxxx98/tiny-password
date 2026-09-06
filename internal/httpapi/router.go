// Package httpapi owns routing and HTTP concerns. Handlers must not operate
// SQL, crypto, or archive processes directly; they call service interfaces.
package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/tiny-password/tiny-password/internal/requestid"
)

// Options wires the API router to its collaborators. Dependencies grow per
// milestone; anything nil is treated as "not available in this milestone".
type Options struct {
	// SPA serves the built frontend for non-API routes.
	SPA http.Handler
	// Ready powers /readyz; nil means the endpoint reports 503.
	Ready *ReadyChecker
	// Setup wires the one-time initialization endpoints; nil leaves the
	// instance without a setup entry point.
	Setup *SetupDeps
	Auth  *AuthDeps
	// Audit wires the personal-activity and admin system-audit queries.
	Audit *AuditDeps
	// Users wires the admin member-management endpoints.
	Users *UsersDeps
	// Items wires the encrypted vault item endpoints.
	Items *ItemsDeps
	// Generators wires the credential generator endpoints.
	Generators *GeneratorsDeps
	// Transfer wires the personal import/export endpoints.
	Transfer *TransferDeps
	// Backups wires the admin backup endpoints (jobs, runs, manual run).
	Backups *BackupsDeps
	// Settings wires the admin system-settings endpoints.
	Settings *SettingsDeps
	// Logger receives one structured access-log record per request; nil
	// disables access logging (tests).
	Logger *slog.Logger
	// Proxy describes the trusted proxy networks used to resolve the client
	// address and protocol. An empty config trusts nothing (default).
	Proxy ProxyConfig
}

// New returns the top-level HTTP handler wrapped in the security middleware
// chain: request ID → access log → security headers → panic recovery.
func New(opts Options) http.Handler {
	api := http.NewServeMux()
	registerAPIRoutes(api, opts)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	if opts.Ready != nil {
		mux.Handle("GET /readyz", handleReady(opts.Ready))
	} else {
		mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
			writeError(w, r, http.StatusServiceUnavailable, "MAINTENANCE", "readiness not configured")
		})
	}
	mux.Handle("/api/", api)

	spa := opts.SPA
	if spa == nil {
		spa = http.NotFoundHandler()
	}

	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/healthz", r.URL.Path == "/readyz", hasPrefixPath(r.URL.Path, "/api/"):
			mux.ServeHTTP(w, r)
		default:
			spa.ServeHTTP(w, r)
		}
	})

	return chain(root,
		withRequestID,
		func(next http.Handler) http.Handler { return withAccessLog(opts.Logger, opts.Proxy, next) },
		func(next http.Handler) http.Handler { return withSecurityHeaders(opts.Proxy, next) },
		withRecovery,
	)
}

// registerAPIRoutes mounts versioned endpoints as milestones deliver them.
// Unknown API paths hit the catch-all and return the stable JSON 404.
func registerAPIRoutes(api *http.ServeMux, opts Options) {
	if opts.Setup != nil {
		registerSetup(api, *opts.Setup)
	}
	if opts.Auth != nil {
		registerAuth(api, *opts.Auth)
	}
	if opts.Audit != nil {
		registerAudit(api, *opts.Audit)
	}
	if opts.Users != nil {
		registerUsers(api, *opts.Users)
	}
	if opts.Items != nil {
		registerItems(api, *opts.Items)
	}
	if opts.Generators != nil {
		registerGenerators(api, *opts.Generators)
	}
	if opts.Transfer != nil {
		registerTransfer(api, *opts.Transfer)
	}
	if opts.Backups != nil {
		registerBackups(api, *opts.Backups)
	}
	if opts.Settings != nil {
		registerSettings(api, *opts.Settings)
	}
	api.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "resource not found")
	})
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// writeJSONError is retained for call sites outside a request scope (health
// fallback); request-scoped handlers must use writeError.
func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"code":       code,
		"message":    message,
		"request_id": requestid.New(),
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func hasPrefixPath(path, prefix string) bool {
	return len(path) >= len(prefix) && path[:len(prefix)] == prefix
}
