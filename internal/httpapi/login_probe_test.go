package httpapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestNormalizeLoginProbeURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{"example.com", "https://example.com/", true},
		{"https://example.com/login", "https://example.com/login", true},
		{"https://example.com?next=/login", "https://example.com/?next=/login", true},
		{"//example.com/login", "https://example.com/login", true},
		{"localhost:8080", "", false},
		{"http://127.0.0.1/login", "", false},
		{"javascript:alert(1)", "", false},
		{"javascript:80", "", false},
		{"https://user:pass@example.com", "", false},
	}
	for _, tt := range tests {
		got, err := normalizeLoginProbeURL(tt.input)
		if tt.ok {
			if err != nil || got.String() != tt.want {
				t.Errorf("normalizeLoginProbeURL(%q) = %v, %v; want %q", tt.input, got, err, tt.want)
			}
		} else if err == nil {
			t.Errorf("normalizeLoginProbeURL(%q) unexpectedly accepted %s", tt.input, got)
		}
	}
}

func TestAnalyzeLoginHTML(t *testing.T) {
	result := analyzeLoginHTML([]byte(`
		<html><head><title>Sign in</title></head><body>
		<form><input type="email" autocomplete="username">
		<input type='password' autocomplete='current-password'>
		<button type="submit">Log in</button></form>
		</body></html>`))
	if !result.PasswordField || !result.LoginSignal || result.Score < 8 {
		t.Fatalf("analyzeLoginHTML() = %+v; want a high-confidence login form", result)
	}

	register := analyzeLoginHTML([]byte(`<h1>Create account</h1><input type="password" autocomplete="new-password">`))
	if register.Score >= result.Score || register.LoginSignal {
		t.Fatalf("register page was scored like a login page: %+v", register)
	}
	chinese := analyzeLoginHTML([]byte(`<h1>登录</h1><input type="password">`))
	if !chinese.LoginSignal {
		t.Fatalf("Chinese login text was not detected: %+v", chinese)
	}
}

func TestLoginProbeBlocksPrivateAddresses(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "10.0.0.5", "192.168.1.1", "::1", "localhost", "metadata.google.internal"} {
		u, err := normalizeLoginProbeURL("https://" + host)
		if err == nil && !blockedLoginProbeHost(u.Hostname()) {
			t.Errorf("host %q was not blocked", host)
		}
	}
}

func TestProbeLoginPageFindsLoginLinkAndRanksPasswordForm(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `<h1>Welcome</h1><a href="/login">Sign in</a>`
		if req.URL.Path == "/login" {
			body = `<form><input type="text" autocomplete="username"><input type="password" autocomplete="current-password"><button type="submit">Sign in</button></form>`
		}
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"text/html"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}
	result, err := probeLoginPage(context.Background(), "example.com", client)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) == 0 || result.Candidates[0].URL != "https://example.com/login" {
		t.Fatalf("probeLoginPage() = %+v; want /login as the top candidate", result.Candidates)
	}
	if !result.Candidates[0].PasswordField {
		t.Fatalf("top candidate did not report a password field: %+v", result.Candidates[0])
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
