package ident

import (
	"regexp"
	"testing"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestUUIDv7FormatAndVersion(t *testing.T) {
	for i := 0; i < 1000; i++ {
		id := NewUUIDv7()
		if !uuidPattern.MatchString(id) {
			t.Fatalf("invalid uuidv7: %s", id)
		}
	}
}

func TestUUIDv7UniqueAndMonotonic(t *testing.T) {
	seen := make(map[string]struct{})
	prev := ""
	for i := 0; i < 10000; i++ {
		id := NewUUIDv7()
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate uuid: %s", id)
		}
		if prev != "" && id < prev {
			t.Fatalf("uuid not monotonically ordered: %s after %s", id, prev)
		}
		seen[id] = struct{}{}
		prev = id
	}
}
