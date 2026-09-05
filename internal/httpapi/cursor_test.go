package httpapi

import (
	"strings"
	"testing"
	"time"
)

func newTestCursorCodec(t *testing.T) (*CursorCodec, *time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 5, 8, 0, 0, 0, time.UTC)
	codec, err := NewCursorCodec([]byte("0123456789abcdef0123456789abcdef"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	codec.now = func() time.Time { return now }
	return codec, &now
}

func TestCursorRoundTrip(t *testing.T) {
	codec, _ := newTestCursorCodec(t)
	token := codec.Encode("user-1", `{"scope":"personal"}`, []string{"2026-09-05T08:00:00Z", "item-9"})
	sort, err := codec.Decode(token, "user-1", `{"scope":"personal"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(sort) != 2 || sort[0] != "2026-09-05T08:00:00Z" || sort[1] != "item-9" {
		t.Fatalf("sort round trip: %v", sort)
	}
}

func TestCursorRejectsTamperForeignAndExpired(t *testing.T) {
	codec, now := newTestCursorCodec(t)
	token := codec.Encode("user-1", "filters-A", []string{"pos"})

	cases := []struct {
		name   string
		actor  string
		filter string
		mutate func(string) string
	}{
		{"foreign actor", "user-2", "filters-A", nil},
		{"altered filters", "user-1", "filters-B", nil},
		{"tampered payload", "user-1", "filters-A", func(tok string) string {
			body, sig, _ := strings.Cut(tok, ".")
			return body + "x." + sig
		}},
		{"tampered signature", "user-1", "filters-A", func(tok string) string {
			return strings.Split(tok, ".")[0] + ".invalidsignature"
		}},
		{"empty", "user-1", "filters-A", func(string) string { return "" }},
		{"no signature", "user-1", "filters-A", func(tok string) string { return strings.Split(tok, ".")[0] }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tok := token
			if tc.mutate != nil {
				tok = tc.mutate(token)
			}
			if _, err := codec.Decode(tok, tc.actor, tc.filter); err == nil {
				t.Fatal("cursor must be rejected")
			}
		})
	}

	// Expired cursors are rejected and time travel does not revive them.
	*now = now.Add(2 * time.Minute)
	if _, err := codec.Decode(token, "user-1", "filters-A"); err == nil {
		t.Fatal("expired cursor must be rejected")
	}
}

func TestCursorRejectsShortMACKey(t *testing.T) {
	if _, err := NewCursorCodec([]byte("short"), time.Minute); err == nil {
		t.Fatal("short MAC key must be rejected")
	}
}

func TestCursorRejectsOverlongToken(t *testing.T) {
	codec, _ := newTestCursorCodec(t)
	big := strings.Repeat("A", 8<<10)
	if _, err := codec.Decode(big, "user-1", "f"); err == nil {
		t.Fatal("overlong token must be rejected without processing")
	}
}
