// Package httpapi owns routing and HTTP concerns. Handlers must not operate
// SQL, crypto, or archive processes directly; they call service interfaces.
package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
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
}

// New returns the top-level HTTP handler: /healthz plus the /api/v1 tree are
// owned here; everything else falls through to the SPA handler.
func New(opts Options) http.Handler {
	api := http.NewServeMux()
	registerAPIRoutes(api, opts)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	if opts.Ready != nil {
		mux.Handle("GET /readyz", handleReady(opts.Ready))
	} else {
		mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
			writeJSONError(w, http.StatusServiceUnavailable, "MAINTENANCE", "readiness not configured")
		})
	}
	mux.Handle("/api/", api)

	spa := opts.SPA
	if spa == nil {
		spa = http.NotFoundHandler()
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/healthz", r.URL.Path == "/readyz":
			mux.ServeHTTP(w, r)
		case hasPrefixPath(r.URL.Path, "/api/"):
			mux.ServeHTTP(w, r)
		default:
			spa.ServeHTTP(w, r)
		}
	})
}

// registerAPIRoutes mounts versioned endpoints as milestones deliver them.
// Unknown API paths hit the catch-all and return the stable JSON 404.
func registerAPIRoutes(api *http.ServeMux, opts Options) {
	if opts.Setup != nil {
		registerSetup(api, *opts.Setup)
	}
	api.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSONError(w, http.StatusNotFound, "NOT_FOUND", "resource not found")
	})
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// writeJSONError emits the stable error envelope shared by all API handlers.
func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"code":       code,
		"message":    message,
		"request_id": NewRequestID(),
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

// NewRequestID returns an opaque correlation id. It never encodes secrets.
func NewRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "req-unknown"
	}
	return hex.EncodeToString(b[:])
}
