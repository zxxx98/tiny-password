package integration

import (
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/tiny-password/tiny-password/internal/platform/sqlite"
	"github.com/tiny-password/tiny-password/migrations"
)

func TestAuthMigrationPreservesExistingUsersAndSessionHashes(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	initial, err := migrations.FS.ReadFile("0001_initial.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err = sqlite.Migrate(db.DB, fstest.MapFS{"0001_initial.sql": &fstest.MapFile{Data: initial}}); err != nil {
		t.Fatal(err)
	}
	insertUser(t, db, "alice", "alice", "Alice", "member")
	for _, id := range []string{"old-session-hash-a", "old-session-hash-b"} {
		if _, err = db.Exec(`INSERT INTO sessions(id,user_id,created_at,absolute_expires_at,idle_expires_at) VALUES(?,'alice','2026-09-05T00:00:00Z','2026-09-06T00:00:00Z','2026-09-05T00:15:00Z')`, id); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err = sqlite.Migrate(db.DB, migrations.FS); err != nil {
			t.Fatal(err)
		}
	}
	var idle int
	var hash string
	if err = db.QueryRow("SELECT idle_timeout_minutes,password_hash FROM users WHERE id='alice'").Scan(&idle, &hash); err != nil {
		t.Fatal(err)
	}
	if idle != 15 || hash != "argon2id$test" {
		t.Fatalf("user not preserved: idle=%d hash changed=%v", idle, hash != "argon2id$test")
	}
	var distinct, old int
	if err = db.QueryRow("SELECT count(DISTINCT public_id) FROM sessions WHERE public_id<>''").Scan(&distinct); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT count(*) FROM sessions WHERE id IN ('old-session-hash-a','old-session-hash-b')").Scan(&old); err != nil {
		t.Fatal(err)
	}
	if distinct != 2 || old != 2 {
		t.Fatalf("session migration: public=%d hashes=%d", distinct, old)
	}
}
