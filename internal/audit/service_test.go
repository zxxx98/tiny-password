package audit

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tiny-password/tiny-password/internal/platform/sqlite"
	"github.com/tiny-password/tiny-password/migrations"
)

func newAuditDB(t *testing.T) *sqlite.DB {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := sqlite.Migrate(db.DB, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestRecordRejectsUnknownEventsAndResults(t *testing.T) {
	svc := NewService(Options{})
	db := newAuditDB(t)
	ctx := context.Background()

	cases := []struct {
		name  string
		event Event
		want  error
	}{
		{"unknown event", Event{Name: "totally.made.up", Result: ResultSuccess}, ErrEventNotAllowed},
		{"token-like event", Event{Name: "setup_token_issued", Result: ResultSuccess}, ErrEventNotAllowed},
		{"bad result", Event{Name: EventLoginSuccess, Result: "ok"}, ErrInvalidResult},
		{"oversized target", Event{Name: EventUserCreated, Result: ResultSuccess, TargetID: strings.Repeat("x", 65)}, ErrFieldTooLong},
	}
	for _, tc := range cases {
		if err := svc.Record(ctx, db, tc.event); err != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, err, tc.want)
		}
	}
}

func TestRecordFailsWithFault(t *testing.T) {
	svc := NewService(Options{Fault: func() error { return errInjected }})
	db := newAuditDB(t)
	if err := svc.Record(context.Background(), db, Event{Name: EventLoginSuccess, Result: ResultSuccess}); err != errInjected {
		t.Fatalf("got %v", err)
	}
}

var errInjected = &injectedError{}

type injectedError struct{}

func (*injectedError) Error() string { return "injected audit failure" }

func TestRecordEmptyActorBecomesAnonymous(t *testing.T) {
	svc := NewService(Options{Now: func() time.Time { return time.Date(2026, 9, 5, 8, 0, 0, 0, time.UTC) }})
	db := newAuditDB(t)
	if err := svc.Record(context.Background(), db, Event{Name: EventLoginFailure, Result: ResultFailure}); err != nil {
		t.Fatal(err)
	}
	var actor string
	if err := db.QueryRow(`SELECT actor_id FROM audit_events`).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	if actor != Anonymous {
		t.Fatalf("actor=%q", actor)
	}
}
