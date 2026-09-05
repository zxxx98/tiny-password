package httpapi

import (
	"net/http"
	"regexp"

	"github.com/tiny-password/tiny-password/internal/idempotency"
)

// IdempotencyKeyHeader is the request header carrying the client's key.
const IdempotencyKeyHeader = "Idempotency-Key"

// idempotencyKeyPattern mirrors the OpenAPI parameter: 16-128 chars of
// [A-Za-z0-9_-]. Keys never enter logs or audit records.
var idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

// IdempotencyKey extracts and validates the header.
// present=false means the client omitted it (plain non-idempotent request);
// invalid=true means a present key violates the format (400).
func IdempotencyKey(r *http.Request) (key string, present, invalid bool) {
	key = r.Header.Get(IdempotencyKeyHeader)
	if key == "" {
		return "", false, false
	}
	if !idempotencyKeyPattern.MatchString(key) {
		return "", true, true
	}
	return key, true, false
}

// ScopeFor binds an operation to the acting user: the scope embeds the actor
// id, so another user's identical key can never hit this record — the
// (scope, key) primary key already differs.
func ScopeFor(operation, actorID string) string {
	return operation + ":" + actorID
}

// IdempotencyDependency bundles the service for handlers.
type IdempotencyDependency struct {
	Service *idempotency.Service
}
