package httpapi

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// originAllowed enforces same-origin for state-changing requests.
//
// The Origin host must exactly match the request Host after default-port
// normalization. Hosts are compared as parsed URL authorities, so a spoofing
// origin like "https://evil.com//app.example.com" cannot pass by suffix.
// Either scheme is accepted: production may terminate TLS in a reverse proxy
// that is deliberately not a configured trusted proxy (decision D02), so the
// backend cannot always know the browser-facing scheme. Scheme enforcement
// would break that deployment; cookie integrity is unaffected because Secure
// cookies never travel over plain HTTP.
//
// A missing Origin is allowed: non-browser clients legitimately omit it, and
// browsers always send Origin on cross-origin and most same-origin writes.
// The literal "null" origin (sandboxed frames) is rejected.
func originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return sameAuthority(u, r.Host)
}

// sameAuthority compares an origin authority with the request host, treating
// omitted default ports (:80 http, :443 https) as equal.
func sameAuthority(u *url.URL, requestHost string) bool {
	host, port, err := splitAuthority(u.Host)
	if err != nil {
		return false
	}
	reqHost, reqPort, err := splitAuthority(requestHost)
	if err != nil {
		return false
	}
	if !strings.EqualFold(host, reqHost) {
		return false
	}
	if port == "" {
		port = defaultPort(u.Scheme)
	}
	if reqPort == "" {
		reqPort = defaultPort(u.Scheme)
	}
	return port == reqPort
}

func splitAuthority(authority string) (host, port string, err error) {
	if strings.Contains(authority, "/") || strings.Contains(authority, "@") {
		return "", "", strconv.ErrSyntax
	}
	host, port, err = net.SplitHostPort(authority)
	if err != nil {
		var addrErr *net.AddrError
		if errors.As(err, &addrErr) && addrErr.Err == "missing port in address" {
			// Whole string is the host; bracketed IPv6 literals included.
			return authority, "", nil
		}
		return "", "", err
	}
	return host, port, nil
}

func defaultPort(scheme string) string {
	if scheme == "https" {
		return "443"
	}
	return "80"
}

// secFetchSiteAllowed hardens writes against CSRF for browsers that send
// Fetch Metadata. "same-origin" and "none" (direct navigation) are allowed;
// cross-site and same-site (sibling subdomain) contexts are rejected.
// Non-browser clients omit the header and are unaffected.
func secFetchSiteAllowed(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
		return true
	default:
		return false
	}
}
