package httpapi

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	preAuthCookieName = "tiny_password_preauth"
	preAuthTTL        = 15 * time.Minute
)

// PreAuthCSRF issues short-lived CSRF contexts for pre-authentication write
// requests (decision D07): setup and login. The cookie holds an opaque
// context id; the server keeps only the token digest in memory.
type PreAuthCSRF struct {
	mu      sync.Mutex
	entries map[string]preAuthEntry
}

type preAuthEntry struct {
	tokenSHA256 []byte
	expires     time.Time
}

func NewPreAuthCSRF() *PreAuthCSRF {
	return &PreAuthCSRF{entries: map[string]preAuthEntry{}}
}

// Issue creates (or re-uses) the pre-auth context for the request's cookie
// and returns a fresh CSRF token. The cookie carries an opaque context id;
// the CSRF token is independent random material whose digest is stored
// server-side.
func (c *PreAuthCSRF) Issue(w http.ResponseWriter, r *http.Request) string {
	ctxID := make([]byte, 32)
	tokenRaw := make([]byte, 32)
	if _, err := rand.Read(ctxID); err != nil {
		panic("httpapi: crypto/rand unavailable: " + err.Error())
	}
	if _, err := rand.Read(tokenRaw); err != nil {
		panic("httpapi: crypto/rand unavailable: " + err.Error())
	}
	token := base64.RawURLEncoding.EncodeToString(tokenRaw)
	sum := sha256.Sum256([]byte(token))

	c.mu.Lock()
	c.pruneLocked()
	c.entries[string(ctxID)] = preAuthEntry{tokenSHA256: sum[:], expires: time.Now().Add(preAuthTTL)}
	c.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     preAuthCookieName,
		Value:    base64.RawURLEncoding.EncodeToString(ctxID),
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
	return token
}

// Verify checks the presented X-CSRF-Token against the context bound to the
// request's pre-auth cookie.
func (c *PreAuthCSRF) Verify(r *http.Request) bool {
	cookie, err := r.Cookie(preAuthCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	ctxID, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return false
	}
	presented := r.Header.Get("X-CSRF-Token")
	if presented == "" {
		return false
	}
	sum := sha256.Sum256([]byte(presented))

	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[string(ctxID)]
	if !ok || time.Now().After(entry.expires) {
		delete(c.entries, string(ctxID))
		return false
	}
	for i := range entry.tokenSHA256 {
		if entry.tokenSHA256[i] != sum[i] {
			return false
		}
	}
	return true
}

// Consume removes the context (used after setup/login success so the token
// cannot be replayed).
func (c *PreAuthCSRF) Consume(r *http.Request) {
	cookie, err := r.Cookie(preAuthCookieName)
	if err != nil || cookie.Value == "" {
		return
	}
	ctxID, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return
	}
	c.mu.Lock()
	delete(c.entries, string(ctxID))
	c.mu.Unlock()
}

func (c *PreAuthCSRF) pruneLocked() {
	now := time.Now()
	for id, entry := range c.entries {
		if now.After(entry.expires) {
			delete(c.entries, id)
		}
	}
}

// originAllowed validates the Origin header when present: same-origin only.
func originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	// Reject anything whose host does not exactly match the request host.
	return strings.HasSuffix(origin, "/"+r.Host) || origin == "https://"+r.Host || origin == "http://"+r.Host
}

// RateLimiter is a minimal fixed-window in-memory limiter for pre-auth
// endpoints. Full login/session rate limiting arrives with T06.
type RateLimiter struct {
	mu     sync.Mutex
	hits   map[string][]time.Time
	limit  int
	window time.Duration
}

func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{hits: map[string][]time.Time{}, limit: limit, window: window}
}

// Allow records a hit for key and reports whether it is within the limit.
func (l *RateLimiter) Allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	recent := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if now.Sub(t) < l.window {
			recent = append(recent, t)
		}
	}
	if len(recent) >= l.limit {
		l.hits[key] = recent
		return false
	}
	l.hits[key] = append(recent, now)
	return true
}
