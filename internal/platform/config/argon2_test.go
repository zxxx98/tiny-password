package config

import (
	"testing"

	"github.com/tiny-password/tiny-password/internal/auth"
)

func TestArgon2HashPolicyDefaults(t *testing.T) {
	for _, name := range []string{
		Argon2MemoryMiBEnv,
		Argon2IterationsEnv,
		Argon2ParallelismEnv,
		Argon2MemoryBudgetMiBEnv,
		Argon2MaxConcurrencyEnv,
		Argon2VerifyMaxMemoryMiBEnv,
	} {
		t.Setenv(name, "")
	}
	got, err := Argon2HashPolicy()
	if err != nil {
		t.Fatal(err)
	}
	want := auth.DefaultHashPolicy()
	if got != want {
		t.Fatalf("policy=%+v want %+v", got, want)
	}
}

func TestArgon2HashPolicyAllowsLowMemoryTarget(t *testing.T) {
	t.Setenv(Argon2MemoryMiBEnv, "19")
	t.Setenv(Argon2IterationsEnv, "2")
	t.Setenv(Argon2ParallelismEnv, "1")
	t.Setenv(Argon2MemoryBudgetMiBEnv, "64")
	t.Setenv(Argon2VerifyMaxMemoryMiBEnv, "64")
	t.Setenv(Argon2MaxConcurrencyEnv, "2")

	got, err := Argon2HashPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if got.TargetParams != (auth.HashParams{MemoryKiB: 19 * 1024, Iterations: 2, Parallelism: 1}) {
		t.Fatalf("target=%+v", got.TargetParams)
	}
	if got.MemoryBudgetKiB != 64*1024 || got.VerifyMaxMemoryKiB != 64*1024 || got.MaxConcurrency != 2 {
		t.Fatalf("limits=%+v", got)
	}
}

func TestArgon2HashPolicyRejectsBudgetBelowTarget(t *testing.T) {
	t.Setenv(Argon2MemoryMiBEnv, "64")
	t.Setenv(Argon2MemoryBudgetMiBEnv, "32")
	if _, err := Argon2HashPolicy(); err == nil {
		t.Fatal("budget below target accepted")
	}
}

func TestArgon2HashPolicyRejectsTooSmallTarget(t *testing.T) {
	t.Setenv(Argon2MemoryMiBEnv, "6")
	if _, err := Argon2HashPolicy(); err == nil {
		t.Fatal("target below supported floor accepted")
	}
}
