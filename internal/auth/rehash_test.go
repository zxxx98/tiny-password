package auth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/tiny-password/tiny-password/internal/platform/sqlite"
	"github.com/tiny-password/tiny-password/migrations"
)

func testHashPolicy(memoryMiB, verifyMiB, budgetMiB uint32) HashPolicy {
	return HashPolicy{
		TargetParams:         HashParams{MemoryKiB: memoryMiB * 1024, Iterations: 1, Parallelism: 1},
		VerifyMaxMemoryKiB:   verifyMiB * 1024,
		VerifyMaxIterations:  4,
		VerifyMaxParallelism: 2,
		MemoryBudgetKiB:      budgetMiB * 1024,
		MaxConcurrency:       2,
	}
}

func openRehashDB(t *testing.T, encoded string) *sqlite.DB {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "rehash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := sqlite.Migrate(db.DB, migrations.FS); err != nil {
		t.Fatal(err)
	}
	now := timestamp(time.Now())
	if _, err := db.Exec(
		`INSERT INTO users(id,username_norm,username_display,role,status,must_change_password,password_hash,created_at,updated_at)
		 VALUES('alice','alice','alice','member','active',0,?,?,?)`,
		encoded, now, now,
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestLoginRehashesParameterMismatchInBothDirections(t *testing.T) {
	for _, tc := range []struct {
		name      string
		oldMiB    uint32
		targetMiB uint32
	}{
		{name: "lower memory", oldMiB: 16, targetMiB: 7},
		{name: "raise memory", oldMiB: 7, targetMiB: 16},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldHasher, err := NewPasswordHasher(testHashPolicy(tc.oldMiB, 32, 32))
			if err != nil {
				t.Fatal(err)
			}
			oldHash, err := oldHasher.Hash(testPassword)
			if err != nil {
				t.Fatal(err)
			}
			db := openRehashDB(t, oldHash)

			targetHasher, err := NewPasswordHasher(testHashPolicy(tc.targetMiB, 32, 32))
			if err != nil {
				t.Fatal(err)
			}
			service, err := NewService(db.DB, Options{
				Hasher: targetHasher,
				Limits: Limits{Username: 100, Source: 100, Global: 100},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.Login(context.Background(), "alice", testPassword, "test"); err != nil {
				t.Fatal(err)
			}

			var got string
			if err := db.QueryRow("SELECT password_hash FROM users WHERE id='alice'").Scan(&got); err != nil {
				t.Fatal(err)
			}
			params, err := HashParamsOf(got)
			if err != nil {
				t.Fatal(err)
			}
			if params != targetHasher.Policy().TargetParams {
				t.Fatalf("rehash params=%+v want %+v", params, targetHasher.Policy().TargetParams)
			}
			if got == oldHash {
				t.Fatal("successful login did not replace mismatched hash")
			}
		})
	}
}

func TestWrongPasswordNeverRehashes(t *testing.T) {
	oldHasher, err := NewPasswordHasher(testHashPolicy(16, 32, 32))
	if err != nil {
		t.Fatal(err)
	}
	oldHash, err := oldHasher.Hash(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	db := openRehashDB(t, oldHash)
	targetHasher, err := NewPasswordHasher(testHashPolicy(7, 32, 32))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(db.DB, Options{
		Hasher: targetHasher,
		Limits: Limits{Username: 100, Source: 100, Global: 100},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.Login(context.Background(), "alice", "wrong password value", "test"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong password error=%v", err)
	}
	var got string
	if err := db.QueryRow("SELECT password_hash FROM users WHERE id='alice'").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != oldHash {
		t.Fatal("wrong password changed stored hash")
	}
}

func TestInspectStoredHashesRejectsLegacyHashAboveRuntimeBudget(t *testing.T) {
	oldHasher, err := NewPasswordHasher(testHashPolicy(16, 32, 32))
	if err != nil {
		t.Fatal(err)
	}
	oldHash, err := oldHasher.Hash(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	db := openRehashDB(t, oldHash)
	lowBudget, err := NewPasswordHasher(testHashPolicy(7, 8, 8))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := InspectStoredHashes(context.Background(), db.DB, lowBudget); err == nil {
		t.Fatal("startup compatibility check accepted an unverifiable legacy hash")
	}
}

func TestVerifyRejectsHashAboveRuntimeMemoryLimit(t *testing.T) {
	hasher, err := NewPasswordHasher(testHashPolicy(7, 8, 8))
	if err != nil {
		t.Fatal(err)
	}
	encoded := encodeHash(
		HashParams{MemoryKiB: 16 * 1024, Iterations: 1, Parallelism: 1},
		make([]byte, saltLength),
		make([]byte, hashKeyLength),
	)
	if _, err := hasher.Verify(testPassword, encoded); !errors.Is(err, ErrHashFormat) {
		t.Fatalf("verify error=%v want ErrHashFormat", err)
	}
}
