package config

import (
	"net"
	"os"

	"github.com/tiny-password/tiny-password/internal/httpapi"
)

// AllowInsecureCookies is an explicit development-only opt-in (D02).
// Unset and unrecognized values preserve secure defaults.
func AllowInsecureCookies() bool {
	return os.Getenv("TP_ALLOW_INSECURE_COOKIES") == "1"
}

// TrustedProxyCIDRsEnv selects the trusted proxy network configuration.
const TrustedProxyCIDRsEnv = "TP_TRUSTED_PROXY_CIDRS"

// TrustedProxyCIDRs parses the configured proxy networks. An empty value
// returns an empty list: no forwarded header is ever trusted by default.
// Invalid entries fail startup rather than being silently dropped.
func TrustedProxyCIDRs() ([]*net.IPNet, error) {
	return httpapi.ParseTrustedProxies(os.Getenv(TrustedProxyCIDRsEnv))
}
