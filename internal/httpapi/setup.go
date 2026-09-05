package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/tiny-password/tiny-password/internal/auth"
	"github.com/tiny-password/tiny-password/internal/bootstrap"
)

// SetupDeps wires the setup endpoints to the bootstrap service.
type SetupDeps struct {
	Service   *bootstrap.Service
	CSRF      *PreAuthCSRF
	Logger    *slog.Logger
	RateLimit int // requests per minute per source
}

const setupMaxBodyBytes = 16 << 10 // 16 KiB (D09)

func registerSetup(api *http.ServeMux, deps SetupDeps) {
	if deps.RateLimit <= 0 {
		deps.RateLimit = 10
	}
	// Separate limiters: CSRF issuance is cheap but public; setup attempts
	// are costlier (Argon2id) and stricter.
	csrfLimiter := NewRateLimiter(30, time.Minute)
	setupLimiter := NewRateLimiter(deps.RateLimit, time.Minute)

	// Pre-auth CSRF context issuance (D07). Unauthenticated by design: it
	// only mints a short-lived context bound to an anonymous cookie.
	api.HandleFunc("POST /api/v1/csrf", func(w http.ResponseWriter, r *http.Request) {
		if !csrfLimiter.Allow(sourceKey(r)) {
			writeJSONError(w, http.StatusTooManyRequests, "RATE_LIMITED", "too many attempts; slow down")
			return
		}
		token := deps.CSRF.Issue(w, r)
		writeJSON(w, http.StatusOK, map[string]any{"csrf_token": token})
	})

	api.HandleFunc("GET /api/v1/setup/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"initialized": deps.Service.Initialized(),
		})
	})

	api.HandleFunc("POST /api/v1/setup/init", func(w http.ResponseWriter, r *http.Request) {
		if !setupLimiter.Allow(sourceKey(r)) {
			writeJSONError(w, http.StatusTooManyRequests, "RATE_LIMITED", "too many attempts; slow down")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, setupMaxBodyBytes)

		var input struct {
			Token    string `json:"token"`
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeJSONError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid request body")
			return
		}

		// Pre-auth CSRF (D07): token bound to the pre-auth cookie plus an
		// Origin check when the browser supplied one.
		if !deps.CSRF.Verify(r) {
			writeJSONError(w, http.StatusForbidden, "FORBIDDEN", "missing or invalid CSRF token")
			return
		}
		if !originAllowed(r) {
			writeJSONError(w, http.StatusForbidden, "FORBIDDEN", "cross-origin requests are not allowed")
			return
		}

		result, err := deps.Service.Initialize(bootstrap.InitializeInput{
			Token:    input.Token,
			Username: input.Username,
			Password: input.Password,
		})
		switch {
		case err == nil:
			deps.CSRF.Consume(r)
			writeJSON(w, http.StatusOK, map[string]any{
				"initialized": true,
				"username":    result.Username,
			})
		case errors.Is(err, bootstrap.ErrAlreadyInitialized):
			writeJSONError(w, http.StatusConflict, "SETUP_ALREADY_DONE", "this instance is already initialized")
		case errors.Is(err, bootstrap.ErrMasterKeyUnavailable):
			writeJSONError(w, http.StatusServiceUnavailable, "MAINTENANCE", "master key unavailable; initialization cannot proceed")
		case errors.Is(err, bootstrap.ErrSetupTokenInvalid):
			writeJSONError(w, http.StatusUnauthorized, "SETUP_TOKEN_INVALID", "the setup token is wrong or was already used")
		case errors.Is(err, bootstrap.ErrUsernameTaken):
			writeJSONError(w, http.StatusConflict, "USERNAME_TAKEN", "that username is already in use")
		case errors.Is(err, bootstrap.ErrInvalidUsername):
			writeJSONError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		case errors.Is(err, auth.ErrPasswordPolicy):
			writeJSONError(w, http.StatusBadRequest, "VALIDATION_ERROR", policyMessage(err))
		default:
			deps.Logger.Error("setup failed", "error", err.Error())
			writeJSONError(w, http.StatusInternalServerError, "INTERNAL", "initialization failed; the instance state is unchanged")
		}
	})
}

func sourceKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func policyMessage(err error) string {
	const prefix = "password does not meet the policy: "
	if strings.HasPrefix(err.Error(), prefix) {
		return err.Error()[len(prefix):]
	}
	return "input does not satisfy the requirements"
}
