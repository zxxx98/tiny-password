package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/tiny-password/tiny-password/internal/auth"
	"github.com/tiny-password/tiny-password/internal/bootstrap"
	"github.com/tiny-password/tiny-password/internal/httpapi"
	"github.com/tiny-password/tiny-password/internal/platform/crypto"
	"github.com/tiny-password/tiny-password/internal/platform/sqlite"
)

type authClock struct {
	mu    sync.Mutex
	value time.Time
}

func (c *authClock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.value }
func (c *authClock) Advance(d time.Duration) { c.mu.Lock(); c.value = c.value.Add(d); c.mu.Unlock() }

type authHarness struct {
	db     *sqlite.DB
	svc    *auth.Service
	boot   *bootstrap.Service
	server *httptest.Server
	clock  *authClock
}

func newAuthHarness(t *testing.T) *authHarness {
	t.Helper()
	db := openMigratedDB(t)
	clock := &authClock{value: time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)}
	svc, err := auth.NewService(db.DB, auth.Options{Now: clock.Now, Limits: auth.Limits{Username: 100, Source: 200, Global: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	key, _ := crypto.NewMasterKey(bytes.Repeat([]byte{1}, 32))
	boot, err := bootstrap.NewService(db, key, logger)
	if err != nil {
		t.Fatal(err)
	}
	csrf := httpapi.NewPreAuthCSRF(false)
	srv := httptest.NewServer(httpapi.New(httpapi.Options{
		Setup: &httpapi.SetupDeps{Service: boot, CSRF: csrf, Logger: logger},
		Auth:  &httpapi.AuthDeps{Service: svc, CSRF: csrf},
	}))
	t.Cleanup(srv.Close)
	return &authHarness{db: db, svc: svc, boot: boot, server: srv, clock: clock}
}
func (h *authHarness) user(t *testing.T, id string, force bool) {
	t.Helper()
	hash, err := auth.HashPassword(validPassword)
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.db.Exec(`INSERT INTO users(id,username_norm,username_display,role,status,must_change_password,password_hash,created_at,updated_at) VALUES(?,?,?,'member','active',?,?,?,?)`, id, id, id, force, hash, h.clock.Now().Format(time.RFC3339Nano), h.clock.Now().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
}

type authClient struct {
	cookie *http.Cookie
	csrf   string
}

func (h *authHarness) request(t *testing.T, method, path string, body any, client authClient) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(method, h.server.URL+"/api/v1"+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", h.server.URL)
	req.Header.Set("X-CSRF-Token", client.csrf)
	if client.cookie != nil {
		req.AddCookie(client.cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
func (h *authHarness) login(t *testing.T, name, password string) (authClient, *http.Response) {
	t.Helper()
	resp := h.request(t, "POST", "/csrf", nil, authClient{})
	body := decodeBody(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("csrf=%d", resp.StatusCode)
	}
	pre := authClient{cookie: resp.Cookies()[0], csrf: body["csrf_token"].(string)}
	resp = h.request(t, "POST", "/auth/login", map[string]string{"username": name, "password": password}, pre)
	client := authClient{}
	if resp.StatusCode == 200 {
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(data))
		var body map[string]any
		if err := json.Unmarshal(data, &body); err != nil {
			t.Fatal(err)
		}
		client.csrf, _ = body["csrf_token"].(string)
		for _, cookie := range resp.Cookies() {
			if cookie.Name == "tiny_password_session" {
				client.cookie = cookie
			}
		}
	}
	return client, resp
}
func expectStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != want {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d want=%d body=%s", resp.StatusCode, want, data)
	}
}
func TestAuthLoginCookieHashAndUniformErrors(t *testing.T) {
	h := newAuthHarness(t)
	h.user(t, "alice", false)
	c, resp := h.login(t, "ALICE", validPassword)
	expectStatus(t, resp, 200)
	if c.cookie == nil || !c.cookie.Secure || !c.cookie.HttpOnly || c.cookie.SameSite != http.SameSiteLaxMode || c.csrf == "" {
		t.Fatal("missing secure session/CSRF")
	}
	var stored string
	if err := h.db.QueryRow("SELECT id FROM sessions").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == c.cookie.Value {
		t.Fatal("raw session persisted")
	}
	expectStatus(t, h.request(t, "GET", "/auth/session", nil, c), 200)
	_, bad := h.login(t, "alice", "wrong")
	_, unknown := h.login(t, "missing", "wrong")
	b1, b2 := decodeBody(t, bad), decodeBody(t, unknown)
	if bad.StatusCode != 401 || unknown.StatusCode != 401 || b1["code"] != b2["code"] || b1["message"] != b2["message"] {
		t.Fatalf("enumerating errors: %v %v", b1, b2)
	}
	if _, err := h.db.Exec("UPDATE users SET status='disabled' WHERE id='alice'"); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, h.request(t, "GET", "/auth/session", nil, c), 401)
	_, resp = h.login(t, "alice", validPassword)
	expectStatus(t, resp, 403)
	_, resp = h.login(t, "alice", "wrong")
	expectStatus(t, resp, 401)
}
func TestSessionPollingExpiresAndActivityCannotRevive(t *testing.T) {
	h := newAuthHarness(t)
	h.user(t, "alice", false)
	c, resp := h.login(t, "alice", validPassword)
	expectStatus(t, resp, 200)
	for i := 0; i < 14; i++ {
		h.clock.Advance(time.Minute)
		expectStatus(t, h.request(t, "GET", "/auth/session", nil, c), 200)
	}
	h.clock.Advance(time.Minute)
	expectStatus(t, h.request(t, "GET", "/auth/session", nil, c), 401)
	expectStatus(t, h.request(t, "POST", "/auth/session/activity", nil, c), 401)
}
func TestSessionActivityRespectsAbsoluteDeadline(t *testing.T) {
	h := newAuthHarness(t)
	h.user(t, "alice", false)
	c, resp := h.login(t, "alice", validPassword)
	expectStatus(t, resp, 200)
	for i := 0; i < 143; i++ {
		h.clock.Advance(10 * time.Minute)
		expectStatus(t, h.request(t, "POST", "/auth/session/activity", nil, c), 204)
	}
	h.clock.Advance(10 * time.Minute)
	expectStatus(t, h.request(t, "POST", "/auth/session/activity", nil, c), 401)
}
func TestPasswordFirstLoginRotationAndOldSessionRevocation(t *testing.T) {
	h := newAuthHarness(t)
	h.user(t, "alice", true)
	c, resp := h.login(t, "alice", validPassword)
	body := decodeBody(t, resp)
	if body["must_change_password"] != true {
		t.Fatal("must-change flag absent")
	}
	other, resp := h.login(t, "alice", validPassword)
	expectStatus(t, resp, 200)
	expectStatus(t, h.request(t, "GET", "/auth/session", nil, c), 200)
	expectStatus(t, h.request(t, "GET", "/auth/sessions", nil, c), 403)
	expectStatus(t, h.request(t, "POST", "/auth/session/activity", nil, c), 403)
	expectStatus(t, h.request(t, "POST", "/auth/password", map[string]string{"current_password": "wrong", "new_password": "new correct battery 55"}, c), 401)
	resp = h.request(t, "POST", "/auth/password", map[string]string{"current_password": validPassword, "new_password": "new correct battery 55"}, c)
	rotated := authClient{csrf: resp.Header.Get("X-CSRF-Token")}
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "tiny_password_session" {
			rotated.cookie = cookie
		}
	}
	expectStatus(t, resp, 204)
	expectStatus(t, h.request(t, "GET", "/auth/session", nil, c), 401)
	expectStatus(t, h.request(t, "GET", "/auth/session", nil, other), 401)
	expectStatus(t, h.request(t, "GET", "/auth/sessions", nil, rotated), 200)
	expectStatus(t, h.request(t, "POST", "/auth/logout", nil, rotated), 204)
	expectStatus(t, h.request(t, "GET", "/auth/session", nil, rotated), 401)
	_, resp = h.login(t, "alice", validPassword)
	expectStatus(t, resp, 401)
	_, resp = h.login(t, "alice", "new correct battery 55")
	expectStatus(t, resp, 200)
}
func TestSessionOwnershipRevocationAndPreferences(t *testing.T) {
	h := newAuthHarness(t)
	h.user(t, "alice", false)
	h.user(t, "bobby", false)
	a, resp := h.login(t, "alice", validPassword)
	expectStatus(t, resp, 200)
	b, resp := h.login(t, "bobby", validPassword)
	expectStatus(t, resp, 200)
	resp = h.request(t, "GET", "/auth/sessions", nil, b)
	body := decodeBody(t, resp)
	items := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("sessions=%v", items)
	}
	id := items[0].(map[string]any)["id"].(string)
	expectStatus(t, h.request(t, "DELETE", "/auth/sessions/"+id, nil, a), 404)
	for _, minutes := range []int{4, 31} {
		expectStatus(t, h.request(t, "PATCH", "/auth/preferences", map[string]int{"idle_timeout_minutes": minutes}, b), 400)
	}
	expectStatus(t, h.request(t, "PATCH", "/auth/preferences", map[string]int{"idle_timeout_minutes": 5}, b), 204)
	h.clock.Advance(5 * time.Minute)
	expectStatus(t, h.request(t, "GET", "/auth/session", nil, b), 401)
	expectStatus(t, h.request(t, "GET", "/auth/session", nil, a), 200)
	resp = h.request(t, "GET", "/auth/sessions", nil, a)
	body = decodeBody(t, resp)
	id = body["items"].([]any)[0].(map[string]any)["id"].(string)
	expectStatus(t, h.request(t, "DELETE", "/auth/sessions/"+id, nil, a), 204)
	expectStatus(t, h.request(t, "POST", "/auth/session/activity", nil, a), 401)
}
func TestAuthWritesRequireSessionBoundCSRF(t *testing.T) {
	h := newAuthHarness(t)
	h.user(t, "alice", false)
	expectStatus(t, h.request(t, "POST", "/auth/login", map[string]string{"username": "alice", "password": validPassword}, authClient{}), 403)
	a, resp := h.login(t, "alice", validPassword)
	expectStatus(t, resp, 200)
	b, resp := h.login(t, "alice", validPassword)
	expectStatus(t, resp, 200)
	expectStatus(t, h.request(t, "POST", "/auth/logout", nil, authClient{cookie: a.cookie, csrf: b.csrf}), 403)
	expectStatus(t, h.request(t, "POST", "/auth/logout", nil, authClient{cookie: a.cookie}), 403)
	expectStatus(t, h.request(t, "GET", "/auth/session", nil, a), 200)
}
func TestAuthRateLimitAllDimensionsAndRestart(t *testing.T) {
	for _, dimension := range []string{"username", "source", "global"} {
		t.Run(dimension, func(t *testing.T) {
			h := newAuthHarness(t)
			limits := auth.Limits{Username: 100, Source: 100, Global: 100}
			switch dimension {
			case "username":
				limits.Username = 2
			case "source":
				limits.Source = 2
			case "global":
				limits.Global = 2
			}
			svc, err := auth.NewService(h.db.DB, auth.Options{Now: h.clock.Now, Limits: limits})
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 3; i++ {
				name, source := "missing", "source"
				if dimension != "username" {
					name += string(rune('a' + i))
				}
				if dimension != "source" {
					source += string(rune('a' + i))
				}
				_, err := svc.Login(context.Background(), name, "wrong", source)
				if i < 2 && err != auth.ErrUnauthorized {
					t.Fatalf("attempt %d: %v", i, err)
				}
				if i == 2 && err != auth.ErrRateLimited {
					t.Fatalf("limit bypassed: %v", err)
				}
				if i == 1 {
					svc, err = auth.NewService(h.db.DB, auth.Options{Now: h.clock.Now, Limits: limits})
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			h.clock.Advance(time.Minute)
			if _, err := svc.Login(context.Background(), "missing", "wrong", "source"); err != auth.ErrUnauthorized {
				t.Fatalf("window did not expire: %v", err)
			}
		})
	}
}
