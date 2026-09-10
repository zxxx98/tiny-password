package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tiny-password/tiny-password/internal/httpapi"
	"github.com/tiny-password/tiny-password/internal/platform/sqlite"
	"github.com/tiny-password/tiny-password/internal/webassets"
	"github.com/tiny-password/tiny-password/migrations"
)

func openMigratedDB(t *testing.T) *sqlite.DB {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := sqlite.Migrate(db.DB, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func insertUser(t *testing.T, db *sqlite.DB, id, norm, display, role string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(
		`INSERT INTO users (id, username_norm, username_display, role, status, must_change_password, password_hash, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'active', 1, 'argon2id$test', ?, ?)`,
		id, norm, display, role, now, now,
	)
	if err != nil {
		t.Fatalf("insert user %s: %v", norm, err)
	}
}

func TestMigrationCreatesCoreTables(t *testing.T) {
	db := openMigratedDB(t)
	for _, table := range []string{
		"system_state", "users", "sessions", "login_attempts", "vault_items",
		"item_versions", "audit_events", "app_settings", "backup_jobs", "backup_runs",
		"idempotency_keys", "schema_migrations",
	} {
		var name string
		err := db.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name = ?", table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %s missing: %v", table, err)
		}
	}
	var initialized string
	if err := db.QueryRow("SELECT value FROM system_state WHERE key = 'initialized'").Scan(&initialized); err != nil {
		t.Fatalf("system_state initialized flag missing: %v", err)
	}
	if initialized != "0" {
		t.Fatalf("initialized = %q, want 0", initialized)
	}
}

func TestSecretMigrationPreservesVaultItemsAndExtendsTypeCheck(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "app.db")
	raw, err := sql.Open(sqlite.DriverName, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(filepath.Join("..", "fixtures", "schema-v1.sql"))
	if err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.Exec(string(fixture)); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO schema_migrations(version, name, applied_at) VALUES (1, 'old.sql', '2026-01-01T00:00:00Z')`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO users(id, username_norm, username_display, role, status, must_change_password, password_hash, created_at, updated_at)
		VALUES ('u1', 'u1', 'u1', 'member', 'active', 0, 'hash', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO vault_items(id, vault_scope, owner_user_id, item_type, favorite, payload_version, nonce, ciphertext, revision, created_at, updated_at)
		VALUES ('old-item', 'personal', 'u1', 'login', 1, 1, X'00', X'01', 2, '2026-01-01T00:00:00Z', '2026-01-02T00:00:00Z')`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO item_versions(item_id, revision, payload_version, nonce, ciphertext, created_at)
		VALUES ('old-item', 1, 1, X'02', X'03', '2026-01-01T00:00:00Z')`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := sqlite.Migrate(db.DB, migrations.FS); err != nil {
		t.Fatalf("migrate old schema: %v", err)
	}
	var oldType string
	var oldFavorite, oldRevision int
	if err := db.QueryRow(`SELECT item_type, favorite, revision FROM vault_items WHERE id='old-item'`).Scan(&oldType, &oldFavorite, &oldRevision); err != nil {
		t.Fatal(err)
	}
	if oldType != "login" || oldFavorite != 1 || oldRevision != 2 {
		t.Fatalf("old row changed during migration: type=%q favorite=%d revision=%d", oldType, oldFavorite, oldRevision)
	}
	var historyCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM item_versions WHERE item_id='old-item' AND revision=1`).Scan(&historyCount); err != nil {
		t.Fatal(err)
	}
	if historyCount != 1 {
		t.Fatalf("old history row lost during migration: count=%d", historyCount)
	}
	if _, err := db.Exec(`INSERT INTO vault_items(id, vault_scope, owner_user_id, item_type, payload_version, nonce, ciphertext, revision, created_at, updated_at)
		VALUES ('secret-item', 'personal', 'u1', 'secret', 1, X'00', X'01', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("secret item rejected after migration: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO vault_items(id, vault_scope, owner_user_id, item_type, payload_version, nonce, ciphertext, revision, created_at, updated_at)
		VALUES ('unknown-item', 'personal', 'u1', 'unknown', 1, X'00', X'01', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err == nil {
		t.Fatal("unknown item type accepted after migration")
	}
}

func TestVaultItemScopeOwnerConstraint(t *testing.T) {
	db := openMigratedDB(t)
	insertUser(t, db, "u-admin", "admin", "admin", "admin")
	insertUser(t, db, "u-member", "member", "member", "member")

	now := time.Now().UTC().Format(time.RFC3339Nano)
	nonce := make([]byte, 24)
	itemBase := "INSERT INTO vault_items (id, vault_scope, owner_user_id, created_by_user_id, item_type, payload_version, nonce, ciphertext, revision, created_at, updated_at) VALUES (?, ?, ?, ?, 'login', 1, ?, ?, 1, ?, ?)"
	ciphertext := []byte{0}

	// personal with owner: valid
	if _, err := db.Exec(itemBase, "i1", "personal", "u-member", nil, nonce, ciphertext, now, now); err != nil {
		t.Errorf("personal item with owner rejected: %v", err)
	}
	// shared with creator: valid
	if _, err := db.Exec(itemBase, "i2", "shared", nil, "u-member", nonce, ciphertext, now, now); err != nil {
		t.Errorf("shared item with creator rejected: %v", err)
	}
	// personal without owner: rejected
	if _, err := db.Exec(itemBase, "i3", "personal", nil, nil, nonce, ciphertext, now, now); err == nil {
		t.Error("personal item without owner accepted")
	}
	// personal with creator instead of owner: rejected
	if _, err := db.Exec(itemBase, "i4", "personal", nil, "u-member", nonce, ciphertext, now, now); err == nil {
		t.Error("personal item with creator accepted")
	}
	// shared with owner instead of creator: rejected
	if _, err := db.Exec(itemBase, "i5", "shared", "u-member", nil, nonce, ciphertext, now, now); err == nil {
		t.Error("shared item with owner accepted")
	}
	// invalid scope: rejected
	if _, err := db.Exec(itemBase, "i6", "team", nil, "u-member", nonce, ciphertext, now, now); err == nil {
		t.Error("invalid scope accepted")
	}
}

func TestUsernameNormalizedUnique(t *testing.T) {
	db := openMigratedDB(t)
	insertUser(t, db, "u1", "alice", "alice", "member")
	// The normalized form is unique; duplicates must be rejected regardless of
	// display spelling.
	if _, err := db.Exec(
		`INSERT INTO users (id, username_norm, username_display, role, status, must_change_password, password_hash, created_at, updated_at)
		 VALUES ('u2', 'alice', 'Alice', 'member', 'active', 1, 'h', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
	); err == nil {
		t.Fatal("duplicate normalized username accepted")
	}
	// Distinct normalized form with different display form: accepted.
	if _, err := db.Exec(
		`INSERT INTO users (id, username_norm, username_display, role, status, must_change_password, password_hash, created_at, updated_at)
		 VALUES ('u3', 'bob', 'Bob', 'member', 'active', 1, 'h', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("distinct username rejected: %v", err)
	}
}

func TestAuditEventsSurviveUserDeletion(t *testing.T) {
	db := openMigratedDB(t)
	insertUser(t, db, "u1", "alice", "alice", "member")

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(
		`INSERT INTO audit_events (id, event, actor_id, target_type, target_id, result, request_id, created_at)
		 VALUES ('a1', 'item.create', 'u1', 'vault_item', 'i9', 'success', 'req1', ?)`, now,
	); err != nil {
		t.Fatalf("insert audit event: %v", err)
	}

	if _, err := db.Exec("DELETE FROM users WHERE id = 'u1'"); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	var actorID string
	if err := db.QueryRow("SELECT actor_id FROM audit_events WHERE id = 'a1'").Scan(&actorID); err != nil {
		t.Fatalf("audit event did not survive user deletion: %v", err)
	}
	if actorID != "u1" {
		t.Fatalf("actor_id = %q, want opaque internal id u1", actorID)
	}
}

func TestUserDeletionCascadesSessions(t *testing.T) {
	db := openMigratedDB(t)
	insertUser(t, db, "u1", "alice", "alice", "member")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(
		`INSERT INTO sessions (id, user_id, created_at, absolute_expires_at, idle_expires_at)
		 VALUES ('s1', 'u1', ?, ?, ?)`, now, now, now,
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	if _, err := db.Exec("DELETE FROM users WHERE id = 'u1'"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM sessions WHERE user_id = 'u1'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("sessions survived user deletion: %d", count)
	}
}

func TestReadyzReportsReadyAndNotReady(t *testing.T) {
	db := openMigratedDB(t)

	newServer := func(rc *httpapi.ReadyChecker) *httptest.Server {
		srv := httptest.NewServer(httpapi.New(httpapi.Options{
			SPA:   webassets.SPAHandler(),
			Ready: rc,
		}))
		t.Cleanup(srv.Close)
		return srv
	}

	t.Run("all checks pass", func(t *testing.T) {
		srv := newServer(&httpapi.ReadyChecker{
			DB:             db,
			MasterKeyCheck: func() error { return nil },
		})
		resp, err := http.Get(srv.URL + "/readyz")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		var body struct {
			Status string          `json:"status"`
			Checks map[string]bool `json:"checks"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Status != "ready" || !body.Checks["database"] || !body.Checks["migrations"] || !body.Checks["master_key"] {
			t.Fatalf("unexpected ready body: %+v", body)
		}
	})

	t.Run("master key failure yields 503", func(t *testing.T) {
		srv := newServer(&httpapi.ReadyChecker{
			DB:             db,
			MasterKeyCheck: func() error { return context.Canceled },
		})
		resp, err := http.Get(srv.URL + "/readyz")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", resp.StatusCode)
		}
	})

	t.Run("no ready checker yields 503", func(t *testing.T) {
		srv := httptest.NewServer(httpapi.New(httpapi.Options{SPA: webassets.SPAHandler()}))
		t.Cleanup(srv.Close)
		resp, err := http.Get(srv.URL + "/readyz")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", resp.StatusCode)
		}
	})
}

func TestSnapshotOfProductionSchemaIsConsistent(t *testing.T) {
	db := openMigratedDB(t)
	insertUser(t, db, "u1", "alice", "alice", "member")

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			i++
			now := time.Now().UTC().Format(time.RFC3339Nano)
			_, _ = db.Exec(
				`INSERT INTO audit_events (id, event, actor_id, result, created_at) VALUES (?, 'probe', 'u1', 'success', ?)`,
				"ae-"+string(rune('a'+i%26))+time.Now().UTC().Format("150405.000000000"), now,
			)
			time.Sleep(time.Millisecond)
		}
	}()
	time.Sleep(150 * time.Millisecond)

	snapPath := filepath.Join(t.TempDir(), "snap.db")
	if err := db.Snapshot(context.Background(), snapPath); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	close(stop)
	<-done

	result, err := sqlite.VerifySnapshot(snapPath)
	if err != nil {
		t.Fatal(err)
	}
	if result != "ok" {
		t.Fatalf("integrity = %q", result)
	}
	var events int
	snap, err := sqlite.Open(snapPath)
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	if err := snap.QueryRow("SELECT COUNT(*) FROM audit_events").Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events == 0 {
		t.Fatal("snapshot unexpectedly empty")
	}
}
