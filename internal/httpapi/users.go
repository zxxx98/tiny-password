package httpapi

import (
	"errors"
	"net/http"

	"github.com/tiny-password/tiny-password/internal/auth"
	"github.com/tiny-password/tiny-password/internal/idempotency"
	"github.com/tiny-password/tiny-password/internal/platform/sqlite"
	"github.com/tiny-password/tiny-password/internal/users"
)

// UsersDeps wires the admin member-management endpoints (T09).
type UsersDeps struct {
	Service        *users.Service
	Session        *auth.Service
	Cursor         *CursorCodec
	Idempotency    *idempotency.Service
	PreviewCleanup PreviewCleanup
}

const usersCreateScope = "users.create"
const usersListFilters = "v1:users"

func registerUsers(api *http.ServeMux, deps UsersDeps) {
	admin := func(handler func(w http.ResponseWriter, r *http.Request)) http.Handler {
		return RequireAdmin(deps.Session, false, http.HandlerFunc(handler))
	}
	api.Handle("GET /api/v1/users", admin(func(w http.ResponseWriter, r *http.Request) {
		admin := CurrentPrincipal(r.Context())
		beforeCreated, beforeID, limit, ok := DecodeCursorParams(w, r, deps.Cursor, admin.UserID, usersListFilters)
		if !ok {
			return
		}
		page, err := deps.Service.List(r.Context(), admin, beforeCreated, beforeID, limit+1)
		if err != nil {
			writeUsersError(w, r, err)
			return
		}
		var lastCreated, lastID string
		if len(page) > limit {
			page = page[:limit]
			lastCreated, lastID = page[len(page)-1].CreatedAt, page[len(page)-1].ID
		}
		writeJSON(w, http.StatusOK, CursorPageResponse(deps.Cursor, admin.UserID, usersListFilters, page, lastCreated, lastID))
	}))

	api.Handle("POST /api/v1/users", admin(func(w http.ResponseWriter, r *http.Request) {
		deps.create(w, r)
	}))

	target := func(handler func(w http.ResponseWriter, r *http.Request, targetID string)) http.Handler {
		return admin(func(w http.ResponseWriter, r *http.Request) {
			handler(w, r, r.PathValue("userId"))
		})
	}

	api.Handle("POST /api/v1/users/{userId}/disable", target(func(w http.ResponseWriter, r *http.Request, targetID string) {
		u, err := deps.Service.Disable(r.Context(), CurrentPrincipal(r.Context()), targetID)
		if err != nil {
			writeUsersError(w, r, err)
			return
		}
		if deps.PreviewCleanup != nil {
			deps.PreviewCleanup.InvalidateUser(r.Context(), targetID)
		}
		writeJSON(w, http.StatusOK, u)
	}))

	api.Handle("POST /api/v1/users/{userId}/enable", target(func(w http.ResponseWriter, r *http.Request, targetID string) {
		u, err := deps.Service.Enable(r.Context(), CurrentPrincipal(r.Context()), targetID)
		if err != nil {
			writeUsersError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, u)
	}))

	api.Handle("POST /api/v1/users/{userId}/revoke-sessions", target(func(w http.ResponseWriter, r *http.Request, targetID string) {
		if _, err := deps.Service.RevokeSessions(r.Context(), CurrentPrincipal(r.Context()), targetID); err != nil {
			writeUsersError(w, r, err)
			return
		}
		if deps.PreviewCleanup != nil {
			deps.PreviewCleanup.InvalidateUser(r.Context(), targetID)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	api.Handle("DELETE /api/v1/users/{userId}", target(func(w http.ResponseWriter, r *http.Request, targetID string) {
		var input struct {
			ConfirmUsername string `json:"confirm_username"`
		}
		if !decodeJSONBody(w, r, &input, authMaxBodyBytes) {
			return
		}
		if err := deps.Service.Delete(r.Context(), CurrentPrincipal(r.Context()), targetID, users.DeleteInput{ConfirmUsername: input.ConfirmUsername}); err != nil {
			writeUsersError(w, r, err)
			return
		}
		if deps.PreviewCleanup != nil {
			deps.PreviewCleanup.InvalidateUser(r.Context(), targetID)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
}

// create implements POST /users with optional idempotency. A replay
// re-renders the originally created member after RequireAdmin re-verified the
// caller; a claim completed inside the creation transaction makes the
// first successful response the only possible one.
func (d UsersDeps) create(w http.ResponseWriter, r *http.Request) {
	admin := CurrentPrincipal(r.Context())
	var input struct {
		Username        string `json:"username"`
		InitialPassword string `json:"initial_password"`
	}
	if !decodeJSONBody(w, r, &input, authMaxBodyBytes) {
		return
	}

	if d.Idempotency == nil && r.Header.Get("Idempotency-Key") != "" {
		writeError(w, r, http.StatusServiceUnavailable, "MAINTENANCE", "idempotency is unavailable")
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
			scope := ScopeFor(usersCreateScope, admin.UserID)
			fp := d.Idempotency.Fingerprint(scope, input.Username, input.InitialPassword)
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
				u, err := d.Service.ReplayCreated(r.Context(), admin, resourceID)
				if err != nil {
					if !errors.Is(err, users.ErrNotFound) {
						writeUsersError(w, r, err)
						return
					}
					writeError(w, r, http.StatusConflict, "CONFLICT", "the original resource no longer exists")
					return
				}
				writeJSON(w, http.StatusCreated, u)
				return
			}
		}
	}

	u, err := d.Service.Create(r.Context(), admin, users.CreateInput{
		Username:        input.Username,
		InitialPassword: input.InitialPassword,
	}, claim)
	if err != nil {
		claim.Release(r.Context())
		writeUsersError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

func writeUsersError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrUnauthorized):
		writeAuthError(w, r, err)
	case errors.Is(err, users.ErrForbidden):
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "administrator role required")
	case errors.Is(err, users.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "member not found")
	case errors.Is(err, users.ErrUsernameTaken):
		writeError(w, r, http.StatusConflict, "USERNAME_TAKEN", "that username is already in use")
	case errors.Is(err, users.ErrLastAdminProtected):
		writeError(w, r, http.StatusConflict, "LAST_ADMIN_PROTECTED", "the last administrator cannot be disabled or deleted")
	case errors.Is(err, users.ErrConfirmationMismatch):
		writeError(w, r, http.StatusConflict, "CONFIRMATION_MISMATCH", "the confirmation does not match the member's username")
	case errors.Is(err, users.ErrInvalidUsername):
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
	case errors.Is(err, auth.ErrPasswordPolicy):
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", policyMessage(err))
	case sqlite.IsBusy(err):
		writeError(w, r, http.StatusServiceUnavailable, "DATABASE_BUSY", "database temporarily busy; retry later")
	default:
		writeError(w, r, http.StatusInternalServerError, "INTERNAL", "the member request could not be completed")
	}
}
