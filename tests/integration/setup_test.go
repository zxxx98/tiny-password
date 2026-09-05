package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/tiny-password/tiny-password/internal/bootstrap"
	"github.com/tiny-password/tiny-password/internal/httpapi"
	"github.com/tiny-password/tiny-password/internal/platform/crypto"
	"github.com/tiny-password/tiny-password/internal/platform/sqlite"
	"github.com/tiny-password/tiny-password/internal/webassets"
)

const validPassword = "correct horse battery 42"

// capturedHandler records slog output so tests can assert on D01 behavior.
type capturedHandler struct {
	records atomic.Value // []slog.Record
}

func (c *capturedHandler) Handle(_ context.Context, r slog.Record) error {
	records, _ := c.records.Load().([]slog.Record)
	c.records.Store(append(records, r))
	return nil
}
func (c *capturedHandler) Enabled(context.Context, slog.Level) bool { return true }
func (c *capturedHandler) WithAttrs([]slog.Attr) slog.Handler {
	return c
}
func (c *capturedHandler) WithGroup(string) slog.Handler { return c }

func (c *capturedHandler) countEvents(name string) int {
	records, _ := c.records.Load().([]slog.Record)
	n := 0
	for _, r := range records {
		if r.Message == name {
			n++
		}
	}
	return n
}

func (c *capturedHandler) tokenValue() string {
	records, _ := c.records.Load().([]slog.Record)
	for _, r := range records {
		if r.Message == bootstrap.SetupTokenEvent {
			val := ""
			r.Attrs(func(a slog.Attr) bool {
				if a.Key == "token" {
					val = a.Value.String()
				}
				return true
			})
			return val
		}
	}
	return ""
}

// harness assembles a fresh application (router + services) on a database.
type setupHarness struct {
	db     *sqlite.DB
	server *httptest.Server
	logger *capturedHandler
}

func newSetupHarness(t *testing.T) *setupHarness {
	t.Helper()
	return newSetupHarnessWithLimit(t, openMigratedDB(t), 100)
}

func newSetupHarnessOnDB(t *testing.T, db *sqlite.DB) *setupHarness {
	t.Helper()
	return newSetupHarnessWithLimit(t, db, 100)
}

func newSetupHarnessWithLimit(t *testing.T, db *sqlite.DB, rateLimit int) *setupHarness {
	t.Helper()
	logger := &capturedHandler{}
	slogLogger := slog.New(logger)

	key, err := crypto.NewMasterKey(bytes.Repeat([]byte{0x11}, 32))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := bootstrap.NewService(db, key, slogLogger)
	if err != nil {
		t.Fatalf("bootstrap service: %v", err)
	}
	srv := httptest.NewServer(httpapi.New(httpapi.Options{
		SPA: webassets.SPAHandler(),
		Setup: &httpapi.SetupDeps{
			Service:   svc,
			CSRF:      httpapi.NewPreAuthCSRF(),
			Logger:    slogLogger,
			RateLimit: rateLimit,
		},
	}))
	t.Cleanup(srv.Close)
	return &setupHarness{db: db, server: srv, logger: logger}
}

// setupRequest performs the full pre-auth dance: fetch a CSRF context, then
// post the setup payload with the CSRF header and Origin set.
func setupRequest(t *testing.T, srvURL, token, username, password string) *http.Response {
	t.Helper()
	client := &http.Client{}

	csrfResp, err := client.Post(srvURL+"/api/v1/csrf", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var csrfBody struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.NewDecoder(csrfResp.Body).Decode(&csrfBody); err != nil {
		t.Fatalf("decode csrf response: %v", err)
	}
	cookies := csrfResp.Cookies()
	csrfResp.Body.Close()

	body, _ := json.Marshal(map[string]string{
		"token":    token,
		"username": username,
		"password": password,
	})
	req, err := http.NewRequest(http.MethodPost, srvURL+"/api/v1/setup/init", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", srvURL)
	req.Header.Set("X-CSRF-Token", csrfBody.CSRFToken)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	out, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func decodeBody(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return m
}

func TestSetupStatusAndTokenIssuedOnce(t *testing.T) {
	h := newSetupHarness(t)

	resp, err := http.Get(h.server.URL + "/api/v1/setup/status")
	if err != nil {
		t.Fatal(err)
	}
	body := decodeBody(t, resp)
	if body["initialized"] != false {
		t.Fatalf("expected uninitialized instance, got %v", body)
	}
	if n := h.logger.countEvents(bootstrap.SetupTokenEvent); n != 1 {
		t.Fatalf("setup token logged %d times, want exactly 1", n)
	}
}

func TestSetupRejectsMissingAndWrongToken(t *testing.T) {
	h := newSetupHarness(t)

	// No token at all.
	resp := setupRequest(t, h.server.URL, "", "admin", validPassword)
	body := decodeBody(t, resp)
	if resp.StatusCode != http.StatusUnauthorized || body["code"] != "SETUP_TOKEN_INVALID" {
		t.Fatalf("missing token: status=%d body=%v", resp.StatusCode, body)
	}

	// Wrong token.
	resp = setupRequest(t, h.server.URL, "totally-wrong-token", "admin", validPassword)
	body = decodeBody(t, resp)
	if resp.StatusCode != http.StatusUnauthorized || body["code"] != "SETUP_TOKEN_INVALID" {
		t.Fatalf("wrong token: status=%d body=%v", resp.StatusCode, body)
	}

	// No admin was created.
	var count int
	if err := h.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("users created despite failures: %d", count)
	}
}

func TestSetupSucceedsWithTokenFromLog(t *testing.T) {
	h := newSetupHarness(t)
	token := h.logger.tokenValue()
	if token == "" {
		t.Fatal("no token was logged")
	}

	resp := setupRequest(t, h.server.URL, token, "Admin", validPassword)
	body := decodeBody(t, resp)
	if resp.StatusCode != http.StatusOK || body["initialized"] != true {
		t.Fatalf("init failed: status=%d body=%v", resp.StatusCode, body)
	}

	var role, display string
	if err := h.db.QueryRow(
		"SELECT role, username_display FROM users WHERE username_norm = 'admin'",
	).Scan(&role, &display); err != nil {
		t.Fatalf("admin not created: %v", err)
	}
	if role != "admin" || display != "Admin" {
		t.Fatalf("role=%q display=%q", role, display)
	}
}

func TestSetupConcurrentTwentyExactlyOneWins(t *testing.T) {
	h := newSetupHarness(t)
	token := h.logger.tokenValue()

	var mu sync.Mutex
	var oks, conflicts int
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp := setupRequest(t, h.server.URL, token, fmt.Sprintf("admin%d", i), validPassword)
			mu.Lock()
			defer mu.Unlock()
			switch resp.StatusCode {
			case http.StatusOK:
				oks++
			case http.StatusConflict:
				conflicts++
			}
			resp.Body.Close()
		}(i)
	}
	wg.Wait()
	if oks != 1 || conflicts != 19 {
		t.Fatalf("oks=%d conflicts=%d, want exactly 1 success and 19 conflicts", oks, conflicts)
	}

	var count int
	if err := h.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("admins created: %d, want exactly 1", count)
	}
}

func TestSetupClosedAfterRestartAndEnvChange(t *testing.T) {
	db := openMigratedDB(t)
	h := newSetupHarnessOnDB(t, db)
	token := h.logger.tokenValue()

	resp := setupRequest(t, h.server.URL, token, "admin", validPassword)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first init failed: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Simulate a restart: a new service + router on the same database.
	h2 := newSetupHarnessOnDB(t, db)

	// Status must report initialized and no new token may be logged.
	status, err := http.Get(h2.server.URL + "/api/v1/setup/status")
	if err != nil {
		t.Fatal(err)
	}
	body := decodeBody(t, status)
	if body["initialized"] != true {
		t.Fatalf("status after restart: %v", body)
	}
	if n := h2.logger.countEvents(bootstrap.SetupTokenEvent); n != 0 {
		t.Fatalf("token re-issued after restart (%d times)", n)
	}

	// Any setup attempt, even with the original token, must fail with 409.
	resp = setupRequest(t, h2.server.URL, token, "admin2", validPassword)
	body = decodeBody(t, resp)
	if resp.StatusCode != http.StatusConflict || body["code"] != "SETUP_ALREADY_DONE" {
		t.Fatalf("setup reopened after restart: status=%d body=%v", resp.StatusCode, body)
	}
}

func TestSetupRollsBackOnUsernameConflict(t *testing.T) {
	db := openMigratedDB(t)
	// Pre-insert a colliding user: INSERT inside setup will fail and the
	// transaction must roll back, leaving the instance uninitialized.
	insertUser(t, db, "u-collide", "admin", "admin", "member")

	h := newSetupHarnessOnDB(t, db)
	token := h.logger.tokenValue()

	resp := setupRequest(t, h.server.URL, token, "admin", validPassword)
	body := decodeBody(t, resp)
	if resp.StatusCode != http.StatusConflict || body["code"] != "USERNAME_TAKEN" {
		t.Fatalf("expected USERNAME_TAKEN, got status=%d body=%v", resp.StatusCode, body)
	}

	// The instance must still be uninitialized and retryable with another
	// username (token material intact).
	status, err := http.Get(h.server.URL + "/api/v1/setup/status")
	if err != nil {
		t.Fatal(err)
	}
	if decodeBody(t, status)["initialized"] != false {
		t.Fatal("instance marked initialized despite rolled-back setup")
	}
	resp = setupRequest(t, h.server.URL, token, "root", validPassword)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("retry after conflict failed: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestSetupValidationErrors(t *testing.T) {
	h := newSetupHarness(t)
	token := h.logger.tokenValue()

	cases := []struct {
		name     string
		username string
		password string
	}{
		{"short username", "ab", validPassword},
		{"bad username chars", "sp ace", validPassword},
		{"short password", "admin", "short12"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := setupRequest(t, h.server.URL, token, tc.username, tc.password)
			body := decodeBody(t, resp)
			if resp.StatusCode != http.StatusBadRequest || body["code"] != "VALIDATION_ERROR" {
				t.Fatalf("status=%d body=%v", resp.StatusCode, body)
			}
		})
	}
}

func TestSetupRequiresCSRF(t *testing.T) {
	h := newSetupHarness(t)
	token := h.logger.tokenValue()

	body, _ := json.Marshal(map[string]string{
		"token": token, "username": "admin", "password": validPassword,
	})
	req, err := http.NewRequest(http.MethodPost, h.server.URL+"/api/v1/setup/init", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	m := decodeBody(t, resp)
	if resp.StatusCode != http.StatusForbidden || m["code"] != "FORBIDDEN" {
		t.Fatalf("setup without CSRF: status=%d body=%v", resp.StatusCode, m)
	}
}

func TestSetupRejectsCrossOrigin(t *testing.T) {
	h := newSetupHarness(t)
	token := h.logger.tokenValue()

	// Cross-origin request: Origin header present with a foreign host.
	client := &http.Client{}
	csrfResp, err := client.Post(h.server.URL+"/api/v1/csrf", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer csrfResp.Body.Close()
	var csrfBody struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.NewDecoder(csrfResp.Body).Decode(&csrfBody); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]string{"token": token, "username": "admin", "password": validPassword})
	req, err := http.NewRequest(http.MethodPost, h.server.URL+"/api/v1/setup/init", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example.com")
	req.Header.Set("X-CSRF-Token", csrfBody.CSRFToken)
	for _, c := range csrfResp.Cookies() {
		req.AddCookie(c)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	m := decodeBody(t, resp)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin accepted: status=%d body=%v", resp.StatusCode, m)
	}
}

func TestSetupRateLimited(t *testing.T) {
	h := newSetupHarnessWithLimit(t, openMigratedDB(t), 5)

	var lastStatus int
	for i := 0; i < 10; i++ {
		resp := setupRequest(t, h.server.URL, "wrong-token", "admin", validPassword)
		lastStatus = resp.StatusCode
		resp.Body.Close()
		if lastStatus == http.StatusTooManyRequests {
			return
		}
	}
	t.Fatalf("rate limit never triggered; last status=%d", lastStatus)
}

func TestSetupTokenNeverLeaksIntoAudit(t *testing.T) {
	h := newSetupHarness(t)
	token := h.logger.tokenValue()

	resp := setupRequest(t, h.server.URL, token, "bad user", validPassword)
	resp.Body.Close()

	rows, err := h.db.Query("SELECT event, actor_id, request_id FROM audit_events")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var event, actor string
		var reqID *string
		if err := rows.Scan(&event, &actor, &reqID); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(event, token) || strings.Contains(actor, token) {
			t.Fatal("setup token leaked into audit events")
		}
	}
}
