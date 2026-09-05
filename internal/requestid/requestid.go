// Package requestid carries the per-request correlation id through HTTP
// middleware, services and audit records. Services import this package so
// they can correlate without importing net/http.
package requestid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

type contextKey struct{}

// New returns an opaque 16-hex-character correlation id. It never encodes
// secrets, usernames or resource identifiers.
func New() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "req-unknown"
	}
	return hex.EncodeToString(b[:])
}

// IntoContext attaches id to ctx for downstream services (audit correlation).
func IntoContext(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// FromContext returns the correlation id, or "" when absent (non-HTTP callers).
func FromContext(ctx context.Context) string {
	id, _ := ctx.Value(contextKey{}).(string)
	return id
}
