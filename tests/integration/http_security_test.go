package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tiny-password/tiny-password/internal/httpapi"
)

// secret markers injected into requests; none may appear in logs or responses.
const (
	logSecretQuery = "SYNSECRET-QUERY-7f3a"
	logSecretBody  = "SYNSECRET-BODY-91cd"
	logSecretCk    = "SYNSECRET-COOKIE-b2e4"
)

func newSecurityHarness(t *testing.T) (*authHarness, *capturedHandler) {
	t.Helper()
	h := newAuthHarness(t)
	// Rebuild the server with a captured logger for access-log assertions.
	logger := &capturedHandler{}
	slogLogger := slog.New(logger)
	csrf := httpapi.NewPreAuthCSRF(false)
	srv := httptest.NewServer(httpapi.New(httpapi.Options{
		Setup:  &httpapi.SetupDeps{Service: h.boot, CSRF: csrf, Logger: slogLogger},
		Auth:   &httpapi.AuthDeps{Service: h.svc, CSRF: csrf},
		Logger: slogLogger,
	}))
	t.Cleanup(srv.Close)
	h.server = srv
	return h, logger
}

func slogDiscard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func mustCIDRs(t *testing.T, cidrs ...string) []*net.IPNet {
	t.Helper()
	out, err := httpapi.ParseTrustedProxies(strings.Join(cidrs, ","))
	if err != nil {
		t.Fatalf("parse trusted proxies: %v", err)
	}
	return out
}

// preAuthClient obtains a pre-auth CSRF context for unauthenticated writes.
func (h *authHarness) preAuthClient(t *testing.T) authClient {
	t.Helper()
	resp := h.request(t, "POST", "/csrf", nil, authClient{})
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("csrf status=%d", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	return authClient{cookie: resp.Cookies()[0], csrf: body["csrf_token"].(string)}
}

func (c authClient) cookies() []*http.Cookie {
	if c.cookie == nil {
		return nil
	}
	return []*http.Cookie{c.cookie}
}

func (h *authHarness) do(t *testing.T, req *http.Request, client authClient) *http.Response {
	t.Helper()
	req.Header.Set("Content-Type", "application/json")
	if req.Header.Get("Origin") == "" {
		req.Header.Set("Origin", h.server.URL)
	}
	req.Header.Set("X-CSRF-Token", client.csrf)
	for _, c := range client.cookies() {
		req.AddCookie(c)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func (h *authHarness) requestWithOrigin(t *testing.T, method, path string, body any, client authClient, origin string) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(method, h.server.URL+"/api/v1"+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	return h.do(t, req, client)
}

func (h *authHarness) requestWithSite(t *testing.T, method, path string, body any, client authClient, site string) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(method, h.server.URL+"/api/v1"+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Sec-Fetch-Site", site)
	return h.do(t, req, client)
}

func (h *authHarness) requestWithQuery(t *testing.T, method, path, query string, client authClient) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, h.server.URL+"/api/v1"+path+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	return h.do(t, req, client)
}

func (h *authHarness) requestWithCookieBody(t *testing.T, method, path, query string, client authClient, body any, extraCookie string) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(method, h.server.URL+"/api/v1"+path+query, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Cookie", "tracker="+extraCookie)
	return h.do(t, req, client)
}

func TestSecurityHeadersOnAllResponses(t *testing.T) {
	h, _ := newSecurityHarness(t)

	paths := []string{"/healthz", "/api/v1/setup/status", "/"}
	for _, path := range paths {
		resp, err := http.Get(h.server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		hdr := resp.Header
		if csp := hdr.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
			t.Errorf("%s: weak CSP %q", path, csp)
		}
		if hdr.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: missing nosniff", path)
		}
		if hdr.Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s: missing Referrer-Policy", path)
		}
		if hdr.Get("X-Frame-Options") != "DENY" {
			t.Errorf("%s: missing X-Frame-Options", path)
		}
		if hdr.Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s: CORS must stay disabled", path)
		}
		if hdr.Get("Strict-Transport-Security") != "" {
			t.Errorf("%s: HSTS must not be sent over plain HTTP", path)
		}
		if strings.HasPrefix(path, "/api/") && !strings.Contains(hdr.Get("Cache-Control"), "no-store") {
			t.Errorf("%s: API responses need no-store", path)
		}
	}
}

func TestHSTSOnTLSAndTrustedProxy(t *testing.T) {
	h := newAuthHarness(t)
	csrf := httpapi.NewPreAuthCSRF(false)
	build := func(proxy httpapi.ProxyConfig) http.Handler {
		return httpapi.New(httpapi.Options{
			Setup: &httpapi.SetupDeps{Service: h.boot, CSRF: csrf, Logger: slogDiscard()},
			Auth:  &httpapi.AuthDeps{Service: h.svc, CSRF: csrf},
			Proxy: proxy,
		})
	}

	tlsSrv := httptest.NewTLSServer(build(httpapi.ProxyConfig{}))
	defer tlsSrv.Close()
	client := tlsSrv.Client()
	resp, err := client.Get(tlsSrv.URL + "/api/v1/setup/status")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("Strict-Transport-Security") == "" {
		t.Error("TLS responses must carry HSTS")
	}

	plainSrv := httptest.NewServer(build(httpapi.ProxyConfig{}))
	defer plainSrv.Close()

	// Untrusted peer claiming https: protocol judgment must not change.
	req, _ := http.NewRequest("GET", plainSrv.URL+"/api/v1/setup/status", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("Strict-Transport-Security") != "" {
		t.Error("untrusted X-Forwarded-Proto must not trigger HSTS")
	}

	// Trusted proxy announcing https enables HSTS. httptest dials from a
	// loopback source, so the loopback ranges are trusted here as well.
	trustedSrv := httptest.NewServer(build(httpapi.ProxyConfig{Trusted: mustCIDRs(t, "10.64.0.0/16", "127.0.0.0/8", "::1/128")}))
	defer trustedSrv.Close()
	req, _ = http.NewRequest("GET", trustedSrv.URL+"/api/v1/setup/status", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("Strict-Transport-Security") == "" {
		t.Error("trusted proxy https must trigger HSTS")
	}
}

func TestOriginValidationStrictHostMatch(t *testing.T) {
	h := newAuthHarness(t)
	h.user(t, "alice", false)

	cases := []struct {
		name   string
		origin string
		want   int // 401 means CSRF+origin passed and login ran; 403 means blocked
	}{
		{"correct origin", h.server.URL, 401},
		{"no origin (non-browser)", "", 401},
		{"foreign host", "https://evil.example.com", 403},
		{"suffix spoof", "https://evil.com//" + strings.TrimPrefix(h.server.URL, "http://"), 403},
		{"wrong port", "http://127.0.0.1:9", 403},
		{"origin null", "null", 403},
		{"origin with path", h.server.URL + "/path", 403},
		{"origin with userinfo", "http://user:pass@" + strings.TrimPrefix(h.server.URL, "http://"), 403},
		{"wrong scheme ftp", "ftp://" + strings.TrimPrefix(h.server.URL, "http://"), 403},
		{"ipv6 literal with port", "http://[::1]:9", 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Valid CSRF but hostile origin: only the origin decision matters.
			pre := h.preAuthClient(t)
			resp := h.requestWithOrigin(t, "POST", "/auth/login",
				map[string]string{"username": "alice", "password": "wrong-password"},
				pre, tc.origin)
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("origin=%q status=%d want=%d body=%s", tc.origin, resp.StatusCode, tc.want, body)
			}
		})
	}
}

func TestSecFetchSiteHeaderEnforced(t *testing.T) {
	h := newAuthHarness(t)
	h.user(t, "alice", false)
	pre := h.preAuthClient(t)

	resp := h.requestWithOrigin(t, "POST", "/auth/login",
		map[string]string{"username": "alice", "password": "wrong-password"}, pre, h.server.URL)
	expectStatus(t, resp, 401)

	resp = h.requestWithSite(t, "POST", "/auth/login",
		map[string]string{"username": "alice", "password": "wrong-password"}, pre, "cross-site")
	expectStatus(t, resp, 403)

	resp = h.requestWithSite(t, "POST", "/auth/login",
		map[string]string{"username": "alice", "password": "wrong-password"}, pre, "same-origin")
	expectStatus(t, resp, 401)
}

func TestRequestBodyLimits(t *testing.T) {
	h := newAuthHarness(t)
	pre := h.preAuthClient(t)

	big := bytes.Repeat([]byte("a"), 20<<10)
	req, _ := http.NewRequest("POST", h.server.URL+"/api/v1/auth/login", bytes.NewReader(big))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", h.server.URL)
	req.Header.Set("X-CSRF-Token", pre.csrf)
	for _, c := range pre.cookies() {
		req.AddCookie(c)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("oversized body: status=%d body=%s", resp.StatusCode, body)
	}
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	if m["code"] != "PAYLOAD_TOO_LARGE" {
		t.Fatalf("code=%v", m["code"])
	}
}

func TestAccessLogUsesRouteTemplateAndOmitsSensitiveData(t *testing.T) {
	h, logger := newSecurityHarness(t)
	h.user(t, "alice", false)

	client, resp := h.login(t, "alice", "wrong-password-xx")
	expectStatus(t, resp, 401)

	// Authenticated request carrying secrets in query, cookie and body.
	resp = h.requestWithQuery(t, "GET", "/auth/session", "?q="+logSecretQuery, client)
	resp.Body.Close()
	resp = h.requestWithCookieBody(t, "POST", "/auth/session/activity", "", client,
		map[string]string{"note": logSecretBody}, logSecretCk)
	resp.Body.Close()
	// Path resource id: revoke a session by fabricated id.
	resp = h.request(t, "DELETE", "/auth/sessions/some-session-id", nil, client)
	resp.Body.Close()

	records, _ := logger.records.Load().([]slog.Record)
	if len(records) == 0 {
		t.Fatal("no access log records captured")
	}
	for _, r := range records {
		var line strings.Builder
		line.WriteString(r.Message)
		r.Attrs(func(a slog.Attr) bool {
			line.WriteString("\x00" + a.Key + "\x01" + a.Value.String())
			return true
		})
		text := line.String()
		for _, secret := range []string{logSecretQuery, logSecretBody, logSecretCk, "wrong-password-xx", "some-session-id"} {
			if strings.Contains(text, secret) {
				t.Fatalf("access log leaked %q: %s", secret, text)
			}
		}
		if strings.Contains(text, "?q=") {
			t.Fatalf("access log contains query string: %s", text)
		}
	}
	// Route templates must appear instead of concrete paths.
	foundRoute := false
	for _, r := range records {
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "route" && strings.Contains(a.Value.String(), "/auth/sessions/{sessionId}") {
				foundRoute = true
			}
			return true
		})
	}
	if !foundRoute {
		t.Fatal("access log missing route template for session revoke")
	}
}

func TestRequestIDCorrelation(t *testing.T) {
	h, logger := newSecurityHarness(t)

	// Unknown API path: the stable 404 envelope carries the request id.
	resp, err := http.Get(h.server.URL + "/api/v1/does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	id := resp.Header.Get("X-Request-ID")
	if id == "" {
		t.Fatal("missing X-Request-ID response header")
	}
	if m["request_id"] != id {
		t.Fatalf("error/request id mismatch: header=%s body=%v", id, m["request_id"])
	}

	records, _ := logger.records.Load().([]slog.Record)
	found := false
	for _, r := range records {
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "request_id" && a.Value.String() == id {
				found = true
			}
			return true
		})
	}
	if !found {
		t.Fatal("access log missing the request id")
	}
}
