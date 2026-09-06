package httpapi

import (
	"context"
	"net/http"
	"time"

	tpsqlite "github.com/tiny-password/tiny-password/internal/platform/sqlite"
)

// ReadyChecker aggregates the dependencies /readyz validates: database
// availability, completed migrations, and (once wired) master key validity.
// Only checks with a registered validator are reported; a failing check makes
// the endpoint return 503 (design §14.3).
type ReadyChecker struct {
	DB             *tpsqlite.DB
	MasterKeyCheck func() error
	Timeout        time.Duration
}

// Run exposes the readiness checks to the admin settings endpoint.
func (rc *ReadyChecker) Run(ctx context.Context) (map[string]bool, bool) { return rc.run(ctx) }

func (rc *ReadyChecker) run(ctx context.Context) (map[string]bool, bool) {
	checks := map[string]bool{}
	ok := true
	if rc.DB != nil {
		checks["database"] = false
		pingCtx, cancel := context.WithTimeout(ctx, rc.timeout())
		err := rc.DB.PingContext(pingCtx)
		cancel()
		checks["database"] = err == nil
		if err == nil {
			checks["migrations"] = false
			version, err := tpsqlite.SchemaVersion(rc.DB.DB)
			checks["migrations"] = err == nil && version >= 1
		}
	}
	if rc.MasterKeyCheck != nil {
		checks["master_key"] = rc.MasterKeyCheck() == nil
	}
	for _, passed := range checks {
		if !passed {
			ok = false
		}
	}
	return checks, ok
}

func (rc *ReadyChecker) timeout() time.Duration {
	if rc.Timeout > 0 {
		return rc.Timeout
	}
	return 2 * time.Second
}

func handleReady(rc *ReadyChecker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		checks, ok := rc.run(r.Context())
		code := http.StatusOK
		status := "ready"
		if !ok {
			code = http.StatusServiceUnavailable
			status = "not_ready"
		}
		writeJSON(w, code, map[string]any{
			"status": status,
			"checks": checks,
		})
	}
}
