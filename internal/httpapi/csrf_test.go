package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPreAuthCookieSecureByDefault(t *testing.T) {
	for _, url := range []string{"http://example.test/api/v1/csrf", "https://example.test/api/v1/csrf"} {
		t.Run(url, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, url, nil)
			r.Header.Set("X-Forwarded-Proto", "http")
			w := httptest.NewRecorder()
			NewPreAuthCSRF(false).Issue(w, r)
			cookie := w.Result().Cookies()[0]
			if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
				t.Errorf("unsafe cookie: %s", cookie)
			}
		})
	}
}

func TestPreAuthCookieAllowsExplicitDevelopmentMode(t *testing.T) {
	csrf := NewPreAuthCSRF(true)
	r := httptest.NewRequest(http.MethodPost, "http://example.test/api/v1/csrf", nil)
	w := httptest.NewRecorder()
	token := csrf.Issue(w, r)
	cookie := w.Result().Cookies()[0]
	if cookie.Secure {
		t.Fatal("development opt-in ignored")
	}
	r.AddCookie(cookie)
	r.Header.Set("X-CSRF-Token", token)
	if !csrf.Verify(r) {
		t.Fatal("issued context does not verify")
	}
	csrf.Consume(r)
	if csrf.Verify(r) {
		t.Fatal("consumed context can be replayed")
	}
}
