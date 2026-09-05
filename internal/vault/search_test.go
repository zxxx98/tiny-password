package vault

import (
	"strings"
	"testing"
)

func TestMatchQueryFields(t *testing.T) {
	login := &LoginPayload{
		Name:     "GitHub Work",
		Username: "alice99",
		Password: "not-searched",
		URLs:     []string{"https://github.example/profile"},
		Notes:    "team notes",
	}
	note := &SecureNotePayload{Name: "Backup codes", Body: "keep SYNSECRET-safe"}
	ssh := &SSHKeyPayload{Name: "lab", Comment: "root@homelab"}

	cases := []struct {
		name   string
		query  string
		tag    string
		tags   []string
		typed  any
		want   bool
	}{
		{"name match", "github", "", nil, login, true},
		{"name case-insensitive", "GITHUB", "", nil, login, true},
		{"username match", "alice99", "", nil, login, true},
		{"url match", "profile", "", nil, login, true},
		{"notes match", "team notes", "", nil, login, true},
		{"password is not searchable", "not-searched", "", nil, login, false},
		{"no match", "gitlab", "", nil, login, false},
		{"tag participates in query", "code", "", []string{"code"}, login, true},
		{"tag filter hit", "", "code", []string{"code", "ops"}, login, true},
		{"tag filter miss", "", "ops", []string{"code"}, login, false},
		{"note body match", "SYNSECRET", "", nil, note, true},
		{"ssh comment match", "homelab", "", nil, ssh, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			match := matchQuery(tc.query, tc.tag)
			if got := match(itemRow{}, tc.typed, tc.tags); got != tc.want {
				t.Fatalf("match=%v want %v", got, tc.want)
			}
		})
	}
}

func TestWeakPasswordRule(t *testing.T) {
	if !weakPassword(strings.Repeat("a", WeakPasswordRunes-1)) {
		t.Fatal("11 ASCII runes must be weak")
	}
	if weakPassword(strings.Repeat("a", WeakPasswordRunes)) {
		t.Fatal("12 ASCII runes must not be weak")
	}
	// The rule is code-point based, not byte based: 12 CJK runes are fine.
	if weakPassword(strings.Repeat("密", WeakPasswordRunes)) {
		t.Fatal("code points, not bytes")
	}
	if !weakPassword("") {
		t.Fatal("empty password is weak")
	}
}

func TestExpiredPasswordDate(t *testing.T) {
	today := "2026-09-05"
	if expiredPasswordDate(nil, today) {
		t.Fatal("nil must not expire")
	}
	empty := ""
	if expiredPasswordDate(&empty, today) {
		t.Fatal("empty must not expire")
	}
	past := "2026-09-04"
	if !expiredPasswordDate(&past, today) {
		t.Fatal("yesterday must expire")
	}
	if expiredPasswordDate(&today, today) {
		t.Fatal("today is not yet expired")
	}
	future := "2027-01-01"
	if expiredPasswordDate(&future, today) {
		t.Fatal("future must not expire")
	}
}
