// Package integration runs black-box API tests against the assembled HTTP
// handler, mirroring how the binary wires its dependencies.
package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tiny-password/tiny-password/internal/httpapi"
	"github.com/tiny-password/tiny-password/internal/webassets"
)

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(httpapi.New(httpapi.Options{SPA: webassets.SPAHandler()}))
	t.Cleanup(srv.Close)
	return srv
}

func TestHealthzReportsAlive(t *testing.T) {
	srv := newServer(t)
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Status != "ok" {
		t.Fatalf("status field = %q, want %q", body.Status, "ok")
	}
}

func TestUnknownAPIPathReturnsJSON404(t *testing.T) {
	srv := newServer(t)
	resp, err := http.Get(srv.URL + "/api/v1/does-not-exist")
	if err != nil {
		t.Fatalf("GET unknown api path: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("content-type = %q, want application/json", ct)
	}
	var body struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Code != "NOT_FOUND" {
		t.Fatalf("code = %q, want NOT_FOUND", body.Code)
	}
	if body.RequestID == "" {
		t.Fatal("request_id missing from error envelope")
	}
}

func TestSPARouteServesEntry(t *testing.T) {
	srv := newServer(t)
	resp, err := http.Get(srv.URL + "/vault/personal")
	if err != nil {
		t.Fatalf("GET SPA route: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("content-type = %q, want text/html", ct)
	}
	buf := make([]byte, 64)
	n, _ := resp.Body.Read(buf)
	if n == 0 {
		t.Fatal("SPA fallback body is empty")
	}
}

func TestHealthzDoesNotRequireDataDirectory(t *testing.T) {
	// /healthz must stay alive even when no storage dependency is wired.
	srv := newServer(t)
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}
