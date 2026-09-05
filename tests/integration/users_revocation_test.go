package integration

import (
	"encoding/json"
	"errors"
	"github.com/tiny-password/tiny-password/internal/auth"
	"github.com/tiny-password/tiny-password/internal/httpapi"
	"github.com/tiny-password/tiny-password/internal/users"
	"net/http/httptest"
	"strings"
	"testing"
)

type revokingReader struct {
	*strings.Reader
	before func()
}

func (r *revokingReader) Read(p []byte) (int, error) {
	if r.before != nil {
		f := r.before
		r.before = nil
		f()
	}
	return r.Reader.Read(p)
}

func TestUsersRepeatedDisableReturnsUser(t *testing.T) {
	h := newUsersHarness(t)
	h.bootstrapAdmin(t)
	h.user(t, "repeatmember", false)
	resp := h.request(t, "POST", "/users/repeatmember/disable", nil, h.admin)
	expectStatus(t, resp, 200)
	resp = h.request(t, "POST", "/users/repeatmember/disable", nil, h.admin)
	defer resp.Body.Close()
	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || got["id"] != "repeatmember" || got["status"] != "disabled" || got["username"] != "repeatmember" || got["created_at"] == "" || got["role"] != "member" {
		t.Fatalf("invalid repeated disable response: %d %v", resp.StatusCode, got)
	}
}

func TestUsersCreateRejectsRevocationDuringBodyRead(t *testing.T) {
	h := newUsersHarness(t)
	h.bootstrapAdmin(t)
	handler := httpapi.New(httpapi.Options{Users: &httpapi.UsersDeps{Service: h.users, Session: h.svc}})
	reader := &revokingReader{Reader: strings.NewReader(`{"username":"afterrevoke","initial_password":"` + validPassword + `"}`), before: func() {
		if err := h.svc.Logout(t.Context(), h.admin.cookie.Value); err != nil {
			t.Fatal(err)
		}
	}}
	req := httptest.NewRequest("POST", "http://example.com/api/v1/users", reader)
	req.AddCookie(h.admin.cookie)
	req.Header.Set("Origin", "http://example.com")
	req.Header.Set("X-CSRF-Token", h.admin.csrf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Errorf("revoked caller: status=%d body=%s", w.Code, w.Body.String())
	}
	var count int
	if err := h.db.QueryRow(`SELECT count(*) FROM users WHERE username_norm='afterrevoke'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("revoked caller created member")
	}
}

func TestUsersWritesRecheckLiveAuthority(t *testing.T) {
	for _, invalidate := range []string{"revoked", "expired", "disabled", "first-login", "demoted", "deleted"} {
		t.Run(invalidate, func(t *testing.T) {
			h := newUsersHarness(t)
			h.bootstrapAdmin(t)
			h.user(t, "target", false)
			p, err := h.svc.Authenticate(t.Context(), h.admin.cookie.Value)
			if err != nil {
				t.Fatal(err)
			}
			var query string
			switch invalidate {
			case "revoked":
				query = `UPDATE sessions SET revoked_at='2026-01-01' WHERE user_id=?`
			case "expired":
				query = `UPDATE sessions SET idle_expires_at='2026-01-01' WHERE user_id=?`
			case "disabled":
				query = `UPDATE users SET status='disabled' WHERE id=?`
			case "first-login":
				query = `UPDATE users SET must_change_password=1 WHERE id=?`
			case "demoted":
				query = `UPDATE users SET role='member' WHERE id=?`
			case "deleted":
				query = `DELETE FROM users WHERE id=?`
			}
			if _, err := h.db.Exec(query, p.UserID); err != nil {
				t.Fatal(err)
			}
			actions := map[string]func() error{
				"create": func() error {
					_, err := h.users.Create(t.Context(), p, users.CreateInput{Username: "blocked", InitialPassword: validPassword}, nil)
					return err
				},
				"disable": func() error { _, err := h.users.Disable(t.Context(), p, "target"); return err },
				"enable":  func() error { _, err := h.users.Enable(t.Context(), p, "target"); return err },
				"revoke":  func() error { _, err := h.users.RevokeSessions(t.Context(), p, "target"); return err },
				"delete": func() error {
					return h.users.Delete(t.Context(), p, "target", users.DeleteInput{ConfirmUsername: "target"})
				},
			}
			for name, action := range actions {
				if err := action(); !errors.Is(err, auth.ErrUnauthorized) {
					t.Errorf("%s: %v, want unauthorized", name, err)
				}
			}
			var count int
			if err := h.db.QueryRow(`SELECT count(*) FROM users WHERE id='target' AND status='active'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatal("target changed despite lost authority")
			}
		})
	}
}

func TestUsersRejectsIdempotencyWhenKeyUnavailable(t *testing.T) {
	h := newUsersHarness(t)
	h.bootstrapAdmin(t)
	handler := httpapi.New(httpapi.Options{Users: &httpapi.UsersDeps{Service: h.users, Session: h.svc}})
	req := httptest.NewRequest("POST", "http://example.com/api/v1/users", strings.NewReader(`{"username":"unavailable","initial_password":"`+validPassword+`"}`))
	req.AddCookie(h.admin.cookie)
	req.Header.Set("X-CSRF-Token", h.admin.csrf)
	req.Header.Set("Idempotency-Key", "unavailable-request-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 503 {
		t.Fatalf("unavailable idempotency: %d", w.Code)
	}
}

func TestUsersReplayRejectsRevocationDuringBodyRead(t *testing.T) {
	h := newUsersHarness(t)
	h.bootstrapAdmin(t)
	p, err := h.svc.Authenticate(t.Context(), h.admin.cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	scope := httpapi.ScopeFor("users.create", p.UserID)
	key := "revoked-replay-request-key"
	claim, _, err := h.idem.Claim(t.Context(), scope, key, h.idem.Fingerprint(scope, "replaymember", validPassword))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.users.Create(t.Context(), p, users.CreateInput{Username: "replaymember", InitialPassword: validPassword}, claim); err != nil {
		t.Fatal(err)
	}
	handler := httpapi.New(httpapi.Options{Users: &httpapi.UsersDeps{Service: h.users, Session: h.svc, Idempotency: h.idem}})
	reader := &revokingReader{Reader: strings.NewReader(`{"username":"replaymember","initial_password":"` + validPassword + `"}`), before: func() {
		if err := h.svc.Logout(t.Context(), h.admin.cookie.Value); err != nil {
			t.Fatal(err)
		}
	}}
	req := httptest.NewRequest("POST", "http://example.com/api/v1/users", reader)
	req.AddCookie(h.admin.cookie)
	req.Header.Set("X-CSRF-Token", h.admin.csrf)
	req.Header.Set("Idempotency-Key", key)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("revoked replay: %d %s", w.Code, w.Body.String())
	}
}
