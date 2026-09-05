package httpapi

import (
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tiny-password/tiny-password/internal/requestid"
)

func TestParseTrustedProxies(t *testing.T) {
	nets, err := ParseTrustedProxies("")
	if err != nil || len(nets) != 0 {
		t.Fatalf("empty config: %v %v", nets, err)
	}
	nets, err = ParseTrustedProxies(" 10.0.0.0/8 , 192.0.2.1 , fd00::/8 ")
	if err != nil {
		t.Fatal(err)
	}
	if len(nets) != 3 {
		t.Fatalf("want 3 networks, got %d", len(nets))
	}
	if _, err := ParseTrustedProxies("not-a-cidr"); err == nil {
		t.Fatal("invalid entries must fail")
	}
}

func request(t *testing.T, remoteAddr string, headers map[string]string, isTLS bool) *http.Request {
	t.Helper()
	r := httptest.NewRequest("GET", "http://app.internal/api", nil)
	r.RemoteAddr = remoteAddr
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	if isTLS {
		r.TLS = &tls.ConnectionState{}
	}
	return r
}

func mustNets(t *testing.T, cidrs ...string) []*net.IPNet {
	t.Helper()
	nets, err := ParseTrustedProxies(strings.Join(cidrs, ","))
	if err != nil {
		t.Fatal(err)
	}
	return nets
}

func TestResolveUntrustedPeerIgnoresForwardedHeaders(t *testing.T) {
	proxy := ProxyConfig{}
	r := request(t, "203.0.113.9:5555", map[string]string{
		"X-Forwarded-For":   "1.2.3.4, 10.0.0.1",
		"X-Forwarded-Proto": "https",
	}, true)
	client, proto := proxy.Resolve(r)
	if client != "203.0.113.9" {
		t.Fatalf("client=%q, want direct peer", client)
	}
	if proto != "https" {
		t.Fatalf("proto=%q, want transport TLS", proto)
	}
}

func TestResolveTrustedProxyMultiHop(t *testing.T) {
	proxy := ProxyConfig{Trusted: mustNets(t, "10.0.0.0/8")}
	r := request(t, "10.0.0.5:4000", map[string]string{
		"X-Forwarded-For":   "198.51.100.7, 10.0.0.1, 10.0.0.2",
		"X-Forwarded-Proto": "https",
	}, false)
	client, proto := proxy.Resolve(r)
	if client != "198.51.100.7" {
		t.Fatalf("client=%q, want first untrusted entry", client)
	}
	if proto != "https" {
		t.Fatalf("proto=%q", proto)
	}
}

func TestResolveTrustedProxySpoofedChainStillResolves(t *testing.T) {
	// The client appended its own entry; the trusted chain from the right
	// still yields the address the outermost trusted proxy observed.
	proxy := ProxyConfig{Trusted: mustNets(t, "10.0.0.0/8")}
	r := request(t, "10.0.0.5:4000", map[string]string{
		"X-Forwarded-For": "6.6.6.6, 198.51.100.7",
	}, false)
	client, _ := proxy.Resolve(r)
	if client != "198.51.100.7" {
		t.Fatalf("client=%q, want last untrusted hop", client)
	}
}

func TestResolveAllTrustedFallsBackToLeftmost(t *testing.T) {
	proxy := ProxyConfig{Trusted: mustNets(t, "10.0.0.0/8", "192.0.2.0/24")}
	r := request(t, "10.0.0.5:4000", map[string]string{
		"X-Forwarded-For": "192.0.2.9, 10.0.0.1",
	}, false)
	if client, _ := proxy.Resolve(r); client != "192.0.2.9" {
		t.Fatalf("client=%q", client)
	}
}

func TestResolveInvalidProtoValueIgnored(t *testing.T) {
	proxy := ProxyConfig{Trusted: mustNets(t, "10.0.0.0/8")}
	r := request(t, "10.0.0.5:4000", map[string]string{
		"X-Forwarded-Proto": "javascript:alert(1)",
	}, false)
	if _, proto := proxy.Resolve(r); proto != "http" {
		t.Fatalf("proto=%q, want fallback http", proto)
	}
}

func TestWithRequestIDEchoesHeader(t *testing.T) {
	var seen string
	handler := withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = requestid.FromContext(r.Context())
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if seen == "" || seen != rec.Header().Get("X-Request-ID") {
		t.Fatalf("context id %q vs header %q", seen, rec.Header().Get("X-Request-ID"))
	}
}

func TestRecoveryReturnsStable500AndHeadersStillApply(t *testing.T) {
	panicking := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("synthetic secret PANIC-MARKER must not leak")
	})
	// Chain order as in New: request ID → headers → recovery.
	handler := chain(panicking, withRequestID,
		func(next http.Handler) http.Handler { return withSecurityHeaders(ProxyConfig{}, next) },
		withRecovery)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/boom", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d", rec.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != "INTERNAL" || body["request_id"] == "" {
		t.Fatalf("body=%v", body)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("security headers missing on recovered response")
	}
	if strings.Contains(rec.Body.String(), "PANIC-MARKER") {
		t.Fatal("panic message leaked to the client")
	}
}
