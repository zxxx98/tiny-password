package main

import (
	"bytes"
	"github.com/tiny-password/tiny-password/internal/idempotency"
	"github.com/tiny-password/tiny-password/internal/platform/crypto"
	"github.com/tiny-password/tiny-password/internal/platform/sqlite"
	"github.com/tiny-password/tiny-password/migrations"
	"path/filepath"
	"testing"
)

func TestIdempotencyReplayAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := sqlite.Migrate(db.DB, migrations.FS); err != nil {
		t.Fatal(err)
	}
	raw := bytes.Repeat([]byte{0x71}, 32)
	key, err := crypto.NewMasterKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := newIdempotencyService(db.DB, key)
	if err != nil {
		t.Fatal(err)
	}
	scope, requestKey := "users.create:admin", "restart-request-key"
	fp := svc.Fingerprint(scope, "member", "synthetic-password")
	claim, outcome, err := svc.Claim(t.Context(), scope, requestKey, fp)
	if err != nil || outcome != idempotency.OutcomeFresh {
		t.Fatalf("claim: %v %v", outcome, err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := claim.Complete(t.Context(), tx, "member-id"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db2, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	key2, err := crypto.NewMasterKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	svc2, err := newIdempotencyService(db2.DB, key2)
	if err != nil {
		t.Fatal(err)
	}
	fp2 := svc2.Fingerprint(scope, "member", "synthetic-password")
	_, outcome, err = svc2.Claim(t.Context(), scope, requestKey, fp2)
	if err != nil || outcome != idempotency.OutcomeReplay {
		t.Fatalf("after restart: %v %v, want replay", outcome, err)
	}
	id, err := svc2.ReplayResourceID(t.Context(), scope, requestKey, fp2)
	if err != nil || id != "member-id" {
		t.Fatalf("replayed resource: %q %v", id, err)
	}
	_, outcome, err = svc2.Claim(t.Context(), scope, requestKey, svc2.Fingerprint(scope, "member", "changed"))
	if err != nil || outcome != idempotency.OutcomeConflict {
		t.Fatalf("different request: %v %v", outcome, err)
	}
}

func TestMissingMasterKeyDoesNotCreateIdempotencyService(t *testing.T) {
	svc, err := newIdempotencyService(nil, nil)
	if err != nil || svc != nil {
		t.Fatalf("missing key should leave idempotency unavailable: %v %v", svc, err)
	}
}
