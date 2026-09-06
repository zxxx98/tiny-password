// Package generator produces credentials with the operating system's
// cryptographic random source (design §6.5). Generated values are returned
// once and are never persisted, logged, or audited.
package generator

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
)

// Ambiguous characters excluded by exclude_ambiguous (design §6.5):
// easily confused glyphs — zero/O, one/l/I, pipe.
const ambiguous = "0O1lI|"

// Character classes for the password generator. The symbol set omits
// characters that are ambiguous or hard to type/read in terminals.
const (
	lowerClass = "abcdefghijklmnopqrstuvwxyz"
	upperClass = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	digitClass = "0123456789"
	symbolClass = "!@#$%^&*()-_=+[]{};:,.?/"
)

// PasswordOptions carries the password request (design §6.5).
type PasswordOptions struct {
	Length           int
	Lowercase        bool
	Uppercase        bool
	Digits           bool
	Symbols          bool
	ExcludeAmbiguous bool
}

// DefaultPasswordOptions mirrors the OpenAPI defaults.
func DefaultPasswordOptions() PasswordOptions {
	return PasswordOptions{Length: 20, Lowercase: true, Uppercase: true, Digits: true, Symbols: true}
}

// Password builds one password from the enabled classes. Every position is
// rejection-sampled (no modulo bias), and each enabled class appears at
// least once.
func Password(opts PasswordOptions) (string, error) {
	if opts.Length < 8 || opts.Length > 128 {
		return "", fmt.Errorf("length must be between 8 and 128")
	}
	var pool strings.Builder
	var required []string
	addClass := func(class string, enabled bool, name string) {
		if !enabled {
			return
		}
		filtered := class
		if opts.ExcludeAmbiguous {
			filtered = removeAmbiguous(class)
		}
		if filtered == "" {
			return
		}
		pool.WriteString(filtered)
		required = append(required, filtered)
	}
	addClass(lowerClass, opts.Lowercase, "lowercase")
	addClass(upperClass, opts.Uppercase, "uppercase")
	addClass(digitClass, opts.Digits, "digits")
	addClass(symbolClass, opts.Symbols, "symbols")
	if pool.Len() == 0 {
		return "", fmt.Errorf("at least one character class must be enabled")
	}
	if len(required) > opts.Length {
		return "", fmt.Errorf("length must be at least the number of enabled character classes (%d)", len(required))
	}

	chars := []byte(pool.String())
	out := make([]byte, opts.Length)
	for i := range out {
		c, err := sampleChar(chars)
		if err != nil {
			return "", err
		}
		out[i] = c
	}
	// Guarantee one character per enabled class: place each required class's
	// representative at a distinct random position.
	positions := make([]int, len(out))
	for i := range positions {
		positions[i] = i
	}
	shuffle(positions)
	for k, class := range required {
		c, err := sampleChar([]byte(class))
		if err != nil {
			return "", err
		}
		out[positions[k]] = c
	}
	return string(out), nil
}

func removeAmbiguous(class string) string {
	out := make([]rune, 0, len(class))
	for _, r := range class {
		if !containsRune(ambiguous, r) {
			out = append(out, r)
		}
	}
	return string(out)
}

func containsRune(set string, r rune) bool {
	return strings.ContainsRune(set, r)
}

// sampleChar picks one byte uniformly via rejection sampling — `modulo` over
// crypto/rand would bias the tail of the alphabet.
func sampleChar(alphabet []byte) (byte, error) {
	max := big.NewInt(int64(len(alphabet)))
	for {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return 0, fmt.Errorf("crypto/rand unavailable: %w", err)
		}
		return alphabet[n.Int64()], nil
	}
}

// shuffle applies a Fisher-Yates shuffle with rejection-sampled indices.
func shuffle(items []int) {
	for i := len(items) - 1; i > 0; i-- {
		j, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			panic("generator: crypto/rand unavailable: " + err.Error())
		}
		items[i], items[j.Int64()] = items[j.Int64()], items[i]
	}
}
