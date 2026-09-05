package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestHashAndVerifyCorrectPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery 42")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	ok, err := VerifyPassword("correct horse battery 42", hash)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !ok {
		t.Fatal("correct password rejected")
	}
}

func TestWrongPasswordFails(t *testing.T) {
	hash, err := HashPassword("correct horse battery 42")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := VerifyPassword("wrong horse battery 42", hash)
	if err != nil {
		t.Fatalf("verify error: %v", err)
	}
	if ok {
		t.Fatal("wrong password accepted")
	}
}

func TestUnicodePasswordRoundTrip(t *testing.T) {
	pw := "正确的密码安全🔒 securely-äöü"
	hash, err := HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := VerifyPassword(pw, hash)
	if err != nil || !ok {
		t.Fatalf("unicode password round trip failed: ok=%v err=%v", ok, err)
	}
}

func TestPasswordPolicy(t *testing.T) {
	cases := []struct {
		name    string
		pw      string
		wantErr bool
	}{
		{"11 ascii chars", "abcdefghijk", true},
		{"12 ascii chars", "abcdefghijkl", false},
		{"11 unicode runes ok bytes", "парольпарольп", false}, // 13 runes, 26 bytes
		{"empty", "", true},
		{"nul byte", "abcdefghijkl\x00", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateNewPassword(tc.pw)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateNewPassword(%q) err = %v, wantErr %v", tc.pw, err, tc.wantErr)
			}
		})
	}

	big := strings.Repeat("a", MaxPasswordBytes+1)
	if err := ValidateNewPassword("1234567890" + big); err == nil {
		t.Fatal("oversized password accepted")
	}
	atLimit := strings.Repeat("a", MaxPasswordBytes)
	if err := ValidateNewPassword(atLimit); err != nil {
		t.Fatalf("exactly-at-limit password rejected: %v", err)
	}
}

func TestHashParametersAreStoredPerHash(t *testing.T) {
	// Hash with non-default params and confirm the encoded string carries
	// them and verification still works.
	salt := make([]byte, saltLength)
	params := HashParams{MemoryKiB: 8 * 1024, Iterations: 2, Parallelism: 1}
	encoded := hashWithParams("some password 42", salt, params)
	if !strings.Contains(encoded, "m=8192,t=2,p=1") {
		t.Fatalf("params not stored in hash: %s", encoded)
	}
	ok, err := VerifyPassword("some password 42", encoded)
	if err != nil || !ok {
		t.Fatalf("verify with custom params: ok=%v err=%v", ok, err)
	}
}

func TestMalformedHashesRejected(t *testing.T) {
	cases := []string{
		"",
		"argon2id$v=19$m=65536,t=3,p=2$salt$hash",                     // missing leading $
		"$argon2i$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA",                 // wrong variant
		"$argon2id$v=16$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0$aGFzaA",      // wrong version
		"$argon2id$v=19$m=65536,t=3$c2FsdHNhbHRzYWx0$aGFzaA",          // missing p
		"$argon2id$v=19$m=4194304,t=3,p=2$c2FsdHNhbHRzYWx0$aGFzaA",    // memory > ceiling
		"$argon2id$v=19$m=65536,t=4096,p=2$c2FsdHNhbHRzYWx0$aGFzaA",   // iterations > ceiling
		"$argon2id$v=19$m=65536,t=3,p=512$c2FsdHNhbHRzYWx0$aGFzaA",    // parallelism > ceiling
		"$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA",                // salt too short
		"$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0$bm90MzJieXRlcw", // hash not 32 bytes
		"not a hash at all",
	}
	for _, hash := range cases {
		ok, err := VerifyPassword("whatever", hash)
		if !errors.Is(err, ErrHashFormat) {
			t.Errorf("hash %q: err = %v, want ErrHashFormat (ok=%v)", hash, err, ok)
		}
	}
}
