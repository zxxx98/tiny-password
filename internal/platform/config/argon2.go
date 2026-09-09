package config

import (
	"fmt"
	"os"
	"strconv"

	"github.com/tiny-password/tiny-password/internal/auth"
)

const (
	Argon2MemoryMiBEnv          = "TP_ARGON2_MEMORY_MIB"
	Argon2IterationsEnv         = "TP_ARGON2_ITERATIONS"
	Argon2ParallelismEnv        = "TP_ARGON2_PARALLELISM"
	Argon2MemoryBudgetMiBEnv    = "TP_ARGON2_MEMORY_BUDGET_MIB"
	Argon2MaxConcurrencyEnv     = "TP_ARGON2_MAX_CONCURRENCY"
	Argon2VerifyMaxMemoryMiBEnv = "TP_ARGON2_VERIFY_MAX_MEMORY_MIB"
)

// Argon2HashPolicy loads deployment tuning from the environment while
// preserving secure defaults when variables are absent. Invalid values fail
// startup instead of being silently ignored.
func Argon2HashPolicy() (auth.HashPolicy, error) {
	policy := auth.DefaultHashPolicy()
	var err error

	if policy.TargetParams.MemoryKiB, err = envMiB(
		Argon2MemoryMiBEnv,
		policy.TargetParams.MemoryKiB,
	); err != nil {
		return auth.HashPolicy{}, err
	}
	if policy.TargetParams.Iterations, err = envUint32(
		Argon2IterationsEnv,
		policy.TargetParams.Iterations,
	); err != nil {
		return auth.HashPolicy{}, err
	}
	parallelism, err := envUint32(Argon2ParallelismEnv, uint32(policy.TargetParams.Parallelism))
	if err != nil {
		return auth.HashPolicy{}, err
	}
	if parallelism > 255 {
		return auth.HashPolicy{}, fmt.Errorf("%s must be at most 255", Argon2ParallelismEnv)
	}
	policy.TargetParams.Parallelism = uint8(parallelism)

	if policy.MemoryBudgetKiB, err = envMiB(
		Argon2MemoryBudgetMiBEnv,
		policy.MemoryBudgetKiB,
	); err != nil {
		return auth.HashPolicy{}, err
	}
	if policy.VerifyMaxMemoryKiB, err = envMiB(
		Argon2VerifyMaxMemoryMiBEnv,
		policy.VerifyMaxMemoryKiB,
	); err != nil {
		return auth.HashPolicy{}, err
	}
	concurrency, err := envUint32(Argon2MaxConcurrencyEnv, uint32(policy.MaxConcurrency))
	if err != nil {
		return auth.HashPolicy{}, err
	}
	policy.MaxConcurrency = int(concurrency)

	if err := policy.Validate(); err != nil {
		return auth.HashPolicy{}, fmt.Errorf("argon2 configuration: %w", err)
	}
	return policy, nil
}

func envMiB(name string, fallbackKiB uint32) (uint32, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallbackKiB, nil
	}
	value, err := strconv.ParseUint(raw, 10, 32)
	if err != nil || value == 0 {
		return 0, fmt.Errorf("%s must be a positive integer MiB value", name)
	}
	if value > uint64(^uint32(0))/1024 {
		return 0, fmt.Errorf("%s is too large", name)
	}
	return uint32(value * 1024), nil
}

func envUint32(name string, fallback uint32) (uint32, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseUint(raw, 10, 32)
	if err != nil || value == 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return uint32(value), nil
}
