package config

import "testing"

func TestAllowInsecureCookiesRequiresExplicitOptIn(t *testing.T) {
	for _, value := range []string{"", "0", "true", "yes", "1"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("TP_ALLOW_INSECURE_COOKIES", value)
			if got := AllowInsecureCookies(); got != (value == "1") {
				t.Fatalf("value %q: insecure=%v", value, got)
			}
		})
	}
}
