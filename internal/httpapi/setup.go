package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/tiny-password/tiny-password/internal/auth"
	"github.com/tiny-password/tiny-password/internal/bootstrap"
	"github.com/tiny-password/tiny-password/internal/idempotency"
)

// SetupDeps wires the setup endpoints to the bootstrap service.
type SetupDeps struct {
	Service   *bootstrap.Service
	CSRF      *PreAuthCSRF
	Logger    *slog.Logger
	RateLimit int // requests per minute per source
	// Proxy resolves the client source for rate limiting.
	Proxy ProxyConfig
	// Idempotency makes POST /setup/init replayable (contract Idempotency-Key).
	Idempotency *idempotency.Service
}

const setupMaxBodyBytes = 16 << 10 // 16 KiB (D09)

const setupIdempotencyScope = "setup.init"

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
		if !csrfLimiter.Allow(sourceKey(r, deps.Proxy)) {
			writeError(w, r, http.StatusTooManyRequests, "RATE_LIMITED", "too many attempts; slow down")
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
		if !setupLimiter.Allow(sourceKey(r, deps.Proxy)) {
			writeError(w, r, http.StatusTooManyRequests, "RATE_LIMITED", "too many attempts; slow down")
			return
		}

		var input struct {
			Token    string `json:"token"`
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if !decodeJSONBody(w, r, &input, setupMaxBodyBytes) {
			return
		}

		// Pre-auth CSRF (D07): token bound to the pre-auth cookie plus an
		// Origin check when the browser supplied one. Replay of an
		// idempotent setup passes through these same checks first, so the
		// replay revalidates the caller's authorization context.
		if !deps.CSRF.Verify(r) {
			writeError(w, r, http.StatusForbidden, "FORBIDDEN", "missing or invalid CSRF token")
			return
		}
		if !originAllowed(r) || !secFetchSiteAllowed(r) {
			writeError(w, r, http.StatusForbidden, "FORBIDDEN", "cross-origin requests are not allowed")
			return
		}

		claim, release, proceed := claimSetupIdempotency(w, r, deps, input)
		if !proceed {
			return
		}

		result, err := deps.Service.Initialize(bootstrap.InitializeInput{
			Token:    input.Token,
			Username: input.Username,
			Password: input.Password,
			Claim:    claim,
		})
		if err != nil {
			release(r)
			writeSetupError(w, r, deps, err)
			return
		}
		deps.CSRF.Consume(r)
		writeJSON(w, http.StatusOK, map[string]any{
			"initialized": true,
			"username":    result.Username,
		})
	})
}

// claimSetupIdempotency claims the idempotency key when one is presented.
// A replay renders the administrator created by the original request; the
// setup entry point itself stays closed.
func claimSetupIdempotency(w http.ResponseWriter, r *http.Request, deps SetupDeps, input struct {
	Token    string `json:"token"`
	Username string `json:"username"`
	Password string `json:"password"`
}) (claim *idempotency.Claim, release func(*http.Request), proceed bool) {
	release = func(*http.Request) {}
	if deps.Idempotency == nil {
		return nil, release, true
	}
	key, present, invalid := IdempotencyKey(r)
	if invalid {
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "invalid idempotency key")
		return nil, release, false
	}
	if !present {
		return nil, release, true
	}
	fp := deps.Idempotency.Fingerprint(setupIdempotencyScope, input.Token, input.Username, input.Password)
	claim, outcome, err := deps.Idempotency.Claim(r.Context(), setupIdempotencyScope, key, fp)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL", "initialization failed; the instance state is unchanged")
		return nil, release, false
	}
	switch outcome {
	case idempotency.OutcomeFresh:
		return claim, func(*http.Request) { claim.Release(r.Context()) }, true
	case idempotency.OutcomeInFlight:
		writeError(w, r, http.StatusConflict, "CONFLICT", "an identical request is still in progress")
		return nil, release, false
	case idempotency.OutcomeConflict:
		writeError(w, r, http.StatusConflict, "IDEMPOTENCY_KEY_CONFLICT", "this idempotency key was used with different content")
		return nil, release, false
	default: // Replay
		resourceID, err := deps.Idempotency.ReplayResourceID(r.Context(), setupIdempotencyScope, key, fp)
		if err != nil {
			writeError(w, r, http.StatusConflict, "CONFLICT", "idempotency record is unavailable")
			return nil, release, false
		}
		renderSetupReplay(w, r, deps, resourceID)
		return nil, release, false
	}
}

func renderSetupReplay(w http.ResponseWriter, r *http.Request, deps SetupDeps, resourceID string) {
	username, err := deps.Service.UsernameByID(resourceID)
	if err != nil {
		writeError(w, r, http.StatusConflict, "CONFLICT", "the original resource no longer exists")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"initialized": true, "username": username})
}

func writeSetupError(w http.ResponseWriter, r *http.Request, deps SetupDeps, err error) {
	switch {
	case errors.Is(err, bootstrap.ErrAlreadyInitialized):
		writeError(w, r, http.StatusConflict, "SETUP_ALREADY_DONE", "this instance is already initialized")
	case errors.Is(err, bootstrap.ErrMasterKeyUnavailable):
		writeError(w, r, http.StatusServiceUnavailable, "MAINTENANCE", "master key unavailable; initialization cannot proceed")
	case errors.Is(err, bootstrap.ErrSetupTokenInvalid):
		writeError(w, r, http.StatusUnauthorized, "SETUP_TOKEN_INVALID", "the setup token is wrong or was already used")
	case errors.Is(err, bootstrap.ErrUsernameTaken):
		writeError(w, r, http.StatusConflict, "USERNAME_TAKEN", "that username is already in use")
	case errors.Is(err, bootstrap.ErrInvalidUsername):
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
	case errors.Is(err, auth.ErrPasswordPolicy):
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", policyMessage(err))
	default:
		deps.Logger.Error("setup failed", "error", err.Error())
		writeError(w, r, http.StatusInternalServerError, "INTERNAL", "initialization failed; the instance state is unchanged")
	}
}

// sourceKey returns the rate-limiting source: the resolved client address
// when the peer is a trusted proxy, otherwise the direct peer. Untrusted
// forwarded headers never change it.
func sourceKey(r *http.Request, proxy ProxyConfig) string {
	client, _ := proxy.Resolve(r)
	return client
}

func policyMessage(err error) string {
	const prefix = "password does not meet the policy: "
	if strings.HasPrefix(err.Error(), prefix) {
		return err.Error()[len(prefix):]
	}
	return "input does not satisfy the requirements"
}
