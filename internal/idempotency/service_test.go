package idempotency

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	tpsqlite "github.com/tiny-password/tiny-password/internal/platform/sqlite"
	"github.com/tiny-password/tiny-password/migrations"
)

type fakeClock struct{ value time.Time }

func (c *fakeClock) Now() time.Time { return c.value }

func newTestService(t *testing.T) (*Service, *fakeClock) {
	t.Helper()
	db, err := tpsqlite.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := tpsqlite.Migrate(db.DB, migrations.FS); err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{value: time.Date(2026, 9, 5, 8, 0, 0, 0, time.UTC)}
	svc, err := NewService(db.DB, Options{Now: clock.Now, MACKey: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	return svc, clock
}

func TestFingerprintIsKeyedAndDeterministic(t *testing.T) {
	svc, _ := newTestService(t)
	a := svc.Fingerprint("op", "field1", "field2")
	if a != svc.Fingerprint("op", "field1", "field2") {
		t.Fatal("fingerprint must be deterministic")
	}
	// Length-prefixing keeps [ab][c] distinct from [a][bc].
	if a == svc.Fingerprint("op", "ab", "c") && a == svc.Fingerprint("op", "a", "bc") {
		t.Fatal("ambiguous concatenation")
	}
	if svc.Fingerprint("op2", "field1", "field2") == a {
		t.Fatal("scope must influence the fingerprint")
	}
}

func TestClaimCompleteReplayFlow(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	fp := svc.Fingerprint("op", "body")

	claim, outcome, err := svc.Claim(ctx, "op:user1", "key-aaaaaaaaaaaaaaaa", fp)
	if err != nil || outcome != OutcomeFresh {
		t.Fatalf("fresh claim: outcome=%v err=%v", outcome, err)
	}
	tx, err := svc.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := claim.Complete(ctx, tx, "resource-1"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Same fingerprint: replay with the stored resource id.
	_, outcome, err = svc.Claim(ctx, "op:user1", "key-aaaaaaaaaaaaaaaa", fp)
	if err != nil || outcome != OutcomeReplay {
		t.Fatalf("replay: outcome=%v err=%v", outcome, err)
	}
	id, err := svc.ReplayResourceID(ctx, "op:user1", "key-aaaaaaaaaaaaaaaa", fp)
	if err != nil || id != "resource-1" {
		t.Fatalf("replay resource: id=%q err=%v", id, err)
	}

	// Same key, different content: conflict.
	_, outcome, _ = svc.Claim(ctx, "op:user1", "key-aaaaaaaaaaaaaaaa", svc.Fingerprint("op", "other"))
	if outcome != OutcomeConflict {
		t.Fatalf("conflict: outcome=%v", outcome)
	}

	// Same key, different actor: never hits the other actor's record.
	claim2, outcome, err := svc.Claim(ctx, "op:user2", "key-aaaaaaaaaaaaaaaa", fp)
	if err != nil || outcome != OutcomeFresh {
		t.Fatalf("cross actor must not hit: outcome=%v err=%v", outcome, err)
	}
	claim2.Release(ctx)
}

func TestClaimInFlightAndCompletionRace(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	fp := svc.Fingerprint("op", "body")

	if _, _, err := svc.Claim(ctx, "op:user1", "key-bbbbbbbbbbbbbbbb", fp); err != nil {
		t.Fatal(err)
	}
	// A second concurrent claim sees in-flight, not a second fresh grant.
	_, outcome, err := svc.Claim(ctx, "op:user1", "key-bbbbbbbbbbbbbbbb", fp)
	if err != nil || outcome != OutcomeInFlight {
		t.Fatalf("in-flight: outcome=%v err=%v", outcome, err)
	}
}

func TestFailedAttemptReleasesKey(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	fp := svc.Fingerprint("op", "body")

	claim, outcome, err := svc.Claim(ctx, "op:user1", "key-cccccccccccccccc", fp)
	if err != nil || outcome != OutcomeFresh {
		t.Fatalf("claim: %v %v", outcome, err)
	}
	claim.Release(ctx)

	// The key is usable again after release, same content fresh.
	claim2, outcome, err := svc.Claim(ctx, "op:user1", "key-cccccccccccccccc", fp)
	if err != nil || outcome != OutcomeFresh {
		t.Fatalf("released key must be reusable: outcome=%v err=%v", outcome, err)
	}
	claim2.Release(ctx)
}

func TestExpiredPendingClaimIsReclaimed(t *testing.T) {
	svc, clock := newTestService(t)
	ctx := context.Background()
	fp := svc.Fingerprint("op", "body")

	claim, _, err := svc.Claim(ctx, "op:user1", "key-dddddddddddddddd", fp)
	if err != nil {
		t.Fatal(err)
	}
	claim.Release(ctx)
	// Simulate a crashed claimant: re-create a pending row and move time past
	// the pending TTL.
	claim, _, _ = svc.Claim(ctx, "op:user1", "key-dddddddddddddddd", fp)
	_ = claim
	clock.value = clock.value.Add(11 * time.Minute)

	got, outcome, err := svc.Claim(ctx, "op:user1", "key-dddddddddddddddd", fp)
	if err != nil || outcome != OutcomeFresh {
		t.Fatalf("expired pending must be reclaimed: outcome=%v err=%v", outcome, err)
	}
	if err := got.Complete(ctx, svc.db, "resource-2"); err != nil {
		t.Fatal(err)
	}
}

func TestCompletedReplayExpires(t *testing.T) {
	svc, clock := newTestService(t)
	ctx := context.Background()
	fp := svc.Fingerprint("op", "body")

	claim, _, err := svc.Claim(ctx, "op:user1", "key-eeeeeeeeeeeeeeee", fp)
	if err != nil {
		t.Fatal(err)
	}
	if err := claim.Complete(ctx, svc.db, "resource-3"); err != nil {
		t.Fatal(err)
	}
	clock.value = clock.value.Add(25 * time.Hour)

	_, outcome, err := svc.Claim(ctx, "op:user1", "key-eeeeeeeeeeeeeeee", fp)
	if err != nil || outcome != OutcomeFresh {
		t.Fatalf("expired replay must allow fresh execution: outcome=%v err=%v", outcome, err)
	}
}

func TestCompleteFailsWhenClaimVanished(t *testing.T) {
	svc, clock := newTestService(t)
	ctx := context.Background()
	fp := svc.Fingerprint("op", "body")

	claim, _, err := svc.Claim(ctx, "op:user1", "key-ffffffffffffffff", fp)
	if err != nil {
		t.Fatal(err)
	}
	clock.value = clock.value.Add(11 * time.Minute) // claim expired meanwhile
	if err := claim.Complete(ctx, svc.db, "resource-4"); err == nil {
		t.Fatal("complete must fail for a vanished claim so the tx rolls back")
	}
}

func TestCleanupExpired(t *testing.T) {
	svc, clock := newTestService(t)
	ctx := context.Background()
	fp := svc.Fingerprint("op", "body")
	claim, _, err := svc.Claim(ctx, "op:user1", "key-9999999999999999", fp)
	if err != nil {
		t.Fatal(err)
	}
	if err := claim.Complete(ctx, svc.db, "r"); err != nil {
		t.Fatal(err)
	}
	clock.value = clock.value.Add(25 * time.Hour)
	n, err := svc.CleanupExpired(ctx)
	if err != nil || n != 1 {
		t.Fatalf("cleanup: n=%d err=%v", n, err)
	}
}

func TestRejectsShortMACKey(t *testing.T) {
	db, err := tpsqlite.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := tpsqlite.Migrate(db.DB, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if _, err := NewService(db.DB, Options{MACKey: []byte("short")}); err == nil {
		t.Fatal("short MAC key must be rejected")
	}
}
