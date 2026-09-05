// Package auth implements password hashing and login/session services.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
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

// DefaultHashParams is the V1 baseline: 64 MiB, 3 iterations, 2 threads.
// Release validation (T30) tunes these to the 250–500 ms target per
// architecture; hashes keep the parameters they were created with.
var DefaultHashParams = HashParams{MemoryKiB: 64 * 1024, Iterations: 3, Parallelism: 2}

// Absolute ceilings accepted when parsing an encoded hash. Crafted hashes
// with oversized parameters would otherwise turn verification into a DoS.
const (
	maxMemoryKiB   = 1 << 20 // 1 GiB
	maxIterations  = 64
	maxParallelism = 16
)

var (
	// ErrPasswordPolicy reports a user-chosen password violating the policy;
	// messages are safe to show to users.
	ErrPasswordPolicy = errors.New("password does not meet the policy")
	// ErrHashFormat reports a malformed or out-of-range encoded hash.
	ErrHashFormat = errors.New("malformed password hash")
)

var (
	hashLimiterOnce sync.Once
	hashLimiter     chan struct{}
)

// hashSlots bounds concurrently running Argon2id computations: each costs
// DefaultHashParams.MemoryKiB, so an unbounded burst could exhaust memory.
func hashSlots() chan struct{} {
	hashLimiterOnce.Do(func() {
		n := runtime.NumCPU()
		if n > 4 {
			n = 4
		}
		if n < 1 {
			n = 1
		}
		hashLimiter = make(chan struct{}, n)
	})
	return hashLimiter
}

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

// HashPassword validates the policy and returns an encoded Argon2id hash in
// the standard PHC string format.
func HashPassword(password string) (string, error) {
	if err := ValidateNewPassword(password); err != nil {
		return "", err
	}
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	return hashWithParams(password, salt, DefaultHashParams), nil
}

func hashWithParams(password string, salt []byte, params HashParams) string {
	hashSlots() <- struct{}{}
	defer func() { <-hashSlots() }()
	key := argon2.IDKey([]byte(password), salt, params.Iterations, params.MemoryKiB, params.Parallelism, hashKeyLength)
	return encodeHash(params, salt, key)
}

func encodeHash(params HashParams, salt, key []byte) string {
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, params.MemoryKiB, params.Iterations, params.Parallelism,
		b64.EncodeToString(salt), b64.EncodeToString(key),
	)
}

// VerifyPassword checks password against an encoded Argon2id hash. Malformed
// hashes yield ErrHashFormat; wrong passwords return (false, nil).
func VerifyPassword(password, encoded string) (bool, error) {
	params, salt, want, err := parseHash(encoded)
	if err != nil {
		return false, err
	}
	got := argon2Key(password, salt, params)
	if subtle.ConstantTimeCompare(got, want) == 1 {
		return true, nil
	}
	return false, nil
}

func argon2Key(password string, salt []byte, params HashParams) []byte {
	hashSlots() <- struct{}{}
	defer func() { <-hashSlots() }()
	return argon2.IDKey([]byte(password), salt, params.Iterations, params.MemoryKiB, params.Parallelism, hashKeyLength)
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
	if params.MemoryKiB == 0 || params.MemoryKiB > maxMemoryKiB ||
		params.Iterations == 0 || params.Iterations > maxIterations ||
		params.Parallelism == 0 || params.Parallelism > maxParallelism {
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
