// Package auth implements password hashing and login/session services.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// Policy limits for user-chosen passwords (decision D09).
const (
	MinPasswordChars = 12
	MaxPasswordBytes = 1024
	saltLength       = 16
	hashKeyLength    = 32
)

// HashParams mirrors the Argon2id parameters stored inside every encoded
// hash, so tuning never invalidates existing hashes (design §10.2).
type HashParams struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
}

// DefaultHashParams preserves the V1 baseline: 64 MiB, 3 iterations,
// 2 lanes. Production may override the target through HashPolicy.
var DefaultHashParams = HashParams{MemoryKiB: 64 * 1024, Iterations: 3, Parallelism: 2}

// Absolute PHC ceilings. Runtime verification applies tighter policy limits
// before Argon2 runs, so a crafted stored hash cannot force these maxima.
const (
	hardMaxMemoryKiB   = 1 << 20 // 1 GiB
	hardMaxIterations  = 64
	hardMaxParallelism = 16

	minTargetMemoryKiB = 7 * 1024
	maxTargetMemoryKiB = 256 * 1024
	maxTargetIterations = 10
	maxTargetParallelism = 4
)

// HashPolicy controls new hashes and the resources accepted for legacy
// verification. TargetParams is authoritative: a successfully authenticated
// hash whose parameters differ in either direction is eligible for rehash.
type HashPolicy struct {
	TargetParams         HashParams
	VerifyMaxMemoryKiB   uint32
	VerifyMaxIterations  uint32
	VerifyMaxParallelism uint8
	MemoryBudgetKiB      uint32
	MaxConcurrency       int
}

// DefaultHashPolicy keeps current password strength while bounding Argon2 to
// two concurrent computations and 128 MiB total process memory.
func DefaultHashPolicy() HashPolicy {
	return HashPolicy{
		TargetParams:         DefaultHashParams,
		VerifyMaxMemoryKiB:   128 * 1024,
		VerifyMaxIterations:  10,
		VerifyMaxParallelism: 4,
		MemoryBudgetKiB:      128 * 1024,
		MaxConcurrency:       2,
	}
}

// Validate rejects policies that could make newly-created hashes impossible
// to verify or exceed the intended deployment-safe tuning range.
func (p HashPolicy) Validate() error {
	t := p.TargetParams
	if t.MemoryKiB < minTargetMemoryKiB || t.MemoryKiB > maxTargetMemoryKiB {
		return fmt.Errorf("argon2 target memory must be between %d and %d KiB", minTargetMemoryKiB, maxTargetMemoryKiB)
	}
	if t.Iterations < 1 || t.Iterations > maxTargetIterations {
		return fmt.Errorf("argon2 target iterations must be between 1 and %d", maxTargetIterations)
	}
	if t.Parallelism < 1 || t.Parallelism > maxTargetParallelism {
		return fmt.Errorf("argon2 target parallelism must be between 1 and %d", maxTargetParallelism)
	}
	if p.VerifyMaxMemoryKiB < t.MemoryKiB || p.VerifyMaxMemoryKiB > hardMaxMemoryKiB {
		return fmt.Errorf("argon2 verify max memory must be between target memory and %d KiB", hardMaxMemoryKiB)
	}
	if p.VerifyMaxIterations < t.Iterations || p.VerifyMaxIterations > hardMaxIterations {
		return fmt.Errorf("argon2 verify max iterations must be between target iterations and %d", hardMaxIterations)
	}
	if p.VerifyMaxParallelism < t.Parallelism || p.VerifyMaxParallelism > hardMaxParallelism {
		return fmt.Errorf("argon2 verify max parallelism must be between target parallelism and %d", hardMaxParallelism)
	}
	if p.MemoryBudgetKiB < t.MemoryKiB {
		return errors.New("argon2 memory budget must be at least the target memory")
	}
	if p.MemoryBudgetKiB > hardMaxMemoryKiB {
		return fmt.Errorf("argon2 memory budget must not exceed %d KiB", hardMaxMemoryKiB)
	}
	if p.MaxConcurrency < 1 || p.MaxConcurrency > 32 {
		return errors.New("argon2 max concurrency must be between 1 and 32")
	}
	return nil
}

// PasswordHasher owns the Argon2 resource gates shared by every password
// operation in a process.
type PasswordHasher struct {
	policy HashPolicy
	slots  chan struct{}
	memory *memoryGate
}

type memoryGate struct {
	mu       sync.Mutex
	cond     *sync.Cond
	capacity uint32
	used     uint32
}

func newMemoryGate(capacity uint32) *memoryGate {
	g := &memoryGate{capacity: capacity}
	g.cond = sync.NewCond(&g.mu)
	return g
}

func (g *memoryGate) acquire(amount uint32) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for g.capacity-g.used < amount {
		g.cond.Wait()
	}
	g.used += amount
}

func (g *memoryGate) release(amount uint32) {
	g.mu.Lock()
	g.used -= amount
	g.mu.Unlock()
	g.cond.Broadcast()
}

// NewPasswordHasher validates policy once and creates shared CPU/memory gates.
func NewPasswordHasher(policy HashPolicy) (*PasswordHasher, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	return &PasswordHasher{
		policy: policy,
		slots:  make(chan struct{}, policy.MaxConcurrency),
		memory: newMemoryGate(policy.MemoryBudgetKiB),
	}, nil
}

var (
	defaultHasherOnce sync.Once
	defaultHasher     *PasswordHasher
)

// DefaultPasswordHasher is the shared compatibility hasher used by callers
// that do not inject a deployment policy explicitly.
func DefaultPasswordHasher() *PasswordHasher {
	defaultHasherOnce.Do(func() {
		var err error
		defaultHasher, err = NewPasswordHasher(DefaultHashPolicy())
		if err != nil {
			panic(err)
		}
	})
	return defaultHasher
}

// Policy returns a copy of this hasher's immutable policy.
func (h *PasswordHasher) Policy() HashPolicy { return h.policy }

// ValidateNewPassword enforces the password policy: at least
// MinPasswordChars characters (any Unicode), at most MaxPasswordBytes UTF-8
// bytes, and no NUL bytes.
func ValidateNewPassword(password string) error {
	if strings.ContainsRune(password, 0) {
		return fmt.Errorf("%w: must not contain NUL", ErrPasswordPolicy)
	}
	if runes := len([]rune(password)); runes < MinPasswordChars {
		return fmt.Errorf("%w: at least %d characters required", ErrPasswordPolicy, MinPasswordChars)
	}
	if len(password) > MaxPasswordBytes {
		return fmt.Errorf("%w: at most %d bytes", ErrPasswordPolicy, MaxPasswordBytes)
	}
	return nil
}

var (
	// ErrPasswordPolicy reports a user-chosen password violating the policy;
	// messages are safe to show to users.
	ErrPasswordPolicy = errors.New("password does not meet the policy")
	// ErrHashFormat reports a malformed or policy-incompatible encoded hash.
	ErrHashFormat = errors.New("malformed password hash")
)

// Hash validates the password and creates a PHC string using the current
// target parameters and a fresh random salt.
func (h *PasswordHasher) Hash(password string) (string, error) {
	if err := ValidateNewPassword(password); err != nil {
		return "", err
	}
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key, err := h.derive(password, salt, h.policy.TargetParams)
	if err != nil {
		return "", err
	}
	return encodeHash(h.policy.TargetParams, salt, key), nil
}

// Verify checks a password against an encoded Argon2id hash. Runtime policy
// bounds are checked before acquiring resources or invoking Argon2.
func (h *PasswordHasher) Verify(password, encoded string) (bool, error) {
	params, salt, want, err := parseHash(encoded)
	if err != nil {
		return false, err
	}
	if err := h.validateVerifyParams(params); err != nil {
		return false, err
	}
	got, err := h.derive(password, salt, params)
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// NeedsRehash reports any mismatch with the configured target. This is
// intentionally bidirectional so operators may trade Argon2 memory for CPU
// and gradually migrate existing accounts after successful login.
func (h *PasswordHasher) NeedsRehash(encoded string) bool {
	params, _, _, err := parseHash(encoded)
	if err != nil {
		return false
	}
	return params != h.policy.TargetParams
}

// HashParamsOf returns the PHC parameters without running Argon2.
func HashParamsOf(encoded string) (HashParams, error) {
	params, _, _, err := parseHash(encoded)
	return params, err
}

func (h *PasswordHasher) validateVerifyParams(params HashParams) error {
	if params.MemoryKiB > h.policy.VerifyMaxMemoryKiB || params.MemoryKiB > h.policy.MemoryBudgetKiB ||
		params.Iterations > h.policy.VerifyMaxIterations ||
		params.Parallelism > h.policy.VerifyMaxParallelism {
		return ErrHashFormat
	}
	return nil
}

func (h *PasswordHasher) derive(password string, salt []byte, params HashParams) ([]byte, error) {
	if err := h.validateVerifyParams(params); err != nil {
		return nil, err
	}
	h.slots <- struct{}{}
	defer func() { <-h.slots }()
	h.memory.acquire(params.MemoryKiB)
	defer h.memory.release(params.MemoryKiB)
	return argon2.IDKey([]byte(password), salt, params.Iterations, params.MemoryKiB, params.Parallelism, hashKeyLength), nil
}

// HashPassword is the default-policy compatibility wrapper.
func HashPassword(password string) (string, error) {
	return DefaultPasswordHasher().Hash(password)
}

// VerifyPassword is the default-policy compatibility wrapper.
func VerifyPassword(password, encoded string) (bool, error) {
	return DefaultPasswordHasher().Verify(password, encoded)
}

func hashWithParams(password string, salt []byte, params HashParams) string {
	key, err := DefaultPasswordHasher().derive(password, salt, params)
	if err != nil {
		panic(err)
	}
	return encodeHash(params, salt, key)
}

func encodeHash(params HashParams, salt, key []byte) string {
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, params.MemoryKiB, params.Iterations, params.Parallelism,
		b64.EncodeToString(salt), b64.EncodeToString(key),
	)
}

func parseHash(encoded string) (HashParams, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, hash
	if len(parts) != 6 {
		return HashParams{}, nil, nil, ErrHashFormat
	}
	if parts[0] != "" || parts[1] != "argon2id" {
		return HashParams{}, nil, nil, ErrHashFormat
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return HashParams{}, nil, nil, ErrHashFormat
	}

	var params HashParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d",
		&params.MemoryKiB, &params.Iterations, &params.Parallelism); err != nil {
		return HashParams{}, nil, nil, ErrHashFormat
	}
	if params.MemoryKiB == 0 || params.MemoryKiB > hardMaxMemoryKiB ||
		params.Iterations == 0 || params.Iterations > hardMaxIterations ||
		params.Parallelism == 0 || params.Parallelism > hardMaxParallelism {
		return HashParams{}, nil, nil, ErrHashFormat
	}

	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return HashParams{}, nil, nil, ErrHashFormat
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) != hashKeyLength {
		return HashParams{}, nil, nil, ErrHashFormat
	}
	return params, salt, want, nil
}
