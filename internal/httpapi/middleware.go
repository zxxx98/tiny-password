package httpapi

import (
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/tiny-password/tiny-password/internal/requestid"
)

// ParseTrustedProxies parses a comma-separated CIDR list (TP_TRUSTED_PROXY_CIDRS).
// An empty list means "no proxies are trusted": forwarded headers are then
// ignored entirely and the peer address is the client address.
func ParseTrustedProxies(spec string) ([]*net.IPNet, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	var nets []*net.IPNet
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.Contains(part, "/") {
			if ip := net.ParseIP(part); ip != nil {
				bits := 128
				if ip.To4() != nil {
					bits = 32
				}
				part = part + "/" + itoa(bits)
			}
		}
		_, ipnet, err := net.ParseCIDR(part)
		if err != nil {
			return nil, err
		}
		nets = append(nets, ipnet)
	}
	return nets, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [4]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// ProxyConfig holds the trusted proxy networks and resolves the client
// address and protocol for a request.
//
// Resolution rules (design §10.4):
//   - If the immediate peer is not a trusted proxy, the peer address is the
//     client address and X-Forwarded-For / X-Forwarded-Proto are ignored
//     completely: untrusted forwarding headers can never change the source
//     address or the protocol judgment.
//   - If the peer is trusted, X-Forwarded-For is walked right-to-left and the
//     first untrusted entry is taken as the client address (multi-hop aware;
//     attacker-supplied entries appended by untrusted hops are skipped only
//     when the chain itself is trusted up to the peer). If every entry is
//     trusted, the leftmost entry wins. At most 64 entries are examined.
//   - X-Forwarded-Proto is honored only when the immediate peer is trusted
//     and its value is exactly http or https.
type ProxyConfig struct {
	Trusted []*net.IPNet
}

func (p ProxyConfig) trusted(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, n := range p.Trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

const maxForwardedHops = 64

// Resolve returns the client address (host, no port) and the effective scheme.
func (p ProxyConfig) Resolve(r *http.Request) (client, proto string) {
	proto = "http"
	if r.TLS != nil {
		proto = "https"
	}
	peerHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peerHost = r.RemoteAddr
	}
	peer := net.ParseIP(peerHost)
	if !p.trusted(peer) {
		return peerHost, proto
	}
	if xfp := r.Header.Get("X-Forwarded-Proto"); xfp == "https" || xfp == "http" {
		proto = xfp
	}
	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return peerHost, proto
	}
	entries := strings.Split(xff, ",")
	if len(entries) > maxForwardedHops {
		entries = entries[len(entries)-maxForwardedHops:]
	}
	client = entries[0]
	for i := len(entries) - 1; i >= 0; i-- {
		candidate := stripPort(strings.TrimSpace(entries[i]))
		if ip := net.ParseIP(candidate); ip != nil && !p.trusted(ip) {
			return candidate, proto
		}
	}
	return stripPort(strings.TrimSpace(entries[0])), proto
}

func stripPort(hostPort string) string {
	if host, _, err := net.SplitHostPort(hostPort); err == nil {
		return host
	}
	return hostPort
}

// statusRecorder captures the status code for access logging and lets the
// recovery middleware know whether a response already started.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// withRequestID assigns a correlation id, echoes it as a response header and
// exposes it to handlers and services via context.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := requestid.New()
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(requestid.IntoContext(r.Context(), id)))
	})
}

// withAccessLog emits one structured record per request. Logs carry the route
// template (never the concrete path), so path resource ids, query strings,
// cookies, bodies and user agents never reach the log (design §14.3).
func withAccessLog(logger *slog.Logger, proxy ProxyConfig, next http.Handler) http.Handler {
	if logger == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w}
		client, _ := proxy.Resolve(r)
		start := time.Now()
		next.ServeHTTP(rec, r)
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		route := r.Pattern
		if route == "" {
			route = "(unrouted)"
		}
		logger.Info("http_request",
			"method", r.Method,
			"route", route,
			"status", status,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote", client,
			"request_id", requestid.FromContext(r.Context()),
		)
	})
}

// withSecurityHeaders applies the baseline header set to every response.
// HSTS is emitted only when the transport is actually TLS (directly or via a
// trusted proxy), because HSTS over plain HTTP makes browsers refuse the site.
func withSecurityHeaders(proxy ProxyConfig, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; "+
				"font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'self'; "+
				"form-action 'self'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		_, proto := proxy.Resolve(r)
		if proto == "https" {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		if hasPrefixPath(r.URL.Path, "/api/") || r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// withRecovery converts handler panics into the stable 500 envelope instead
// of letting net/http reset the connection without a JSON error.
func withRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("handler panic recovered", "request_id", requestid.FromContext(r.Context()))
				writeError(w, r, http.StatusInternalServerError, "INTERNAL", "the request could not be completed")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// chain composes middleware with the first argument outermost.
func chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}
