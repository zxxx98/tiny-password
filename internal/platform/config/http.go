package config

import "os"

// AllowInsecureCookies is an explicit development-only opt-in (D02).
// Unset and unrecognized values preserve secure defaults.
func AllowInsecureCookies() bool {
	return os.Getenv("TP_ALLOW_INSECURE_COOKIES") == "1"
}
