package generator

import (
	"crypto/rand"
	"embed"
	"fmt"
	"strings"
)

//go:embed wordlist.txt
var wordlistFS embed.FS

// WordCount is the size of the bundled EFF short wordlist; log2(1296) ≈
// 10.25 bits of entropy per word.
const WordCount = 1296

// PassphraseOptions carries the passphrase request (design §6.5).
type PassphraseOptions struct {
	Words      int
	Separator  string
	Capitalize bool
}

// DefaultPassphraseOptions mirrors the OpenAPI defaults (5 words, '-' join).
func DefaultPassphraseOptions() PassphraseOptions {
	return PassphraseOptions{Words: 5, Separator: "-"}
}

var words []string

func init() {
	raw, err := wordlistFS.ReadFile("wordlist.txt")
	if err != nil {
		panic("generator: bundled wordlist missing: " + err.Error())
	}
	list := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(list) != WordCount {
		panic(fmt.Sprintf("generator: wordlist has %d words, want %d", len(list), WordCount))
	}
	words = list
}

// Passphrase builds one passphrase by rejection-sampling word indices over
// the bundled EFF short wordlist (uniform, no modulo bias).
func Passphrase(opts PassphraseOptions) (string, error) {
	if opts.Words < 3 || opts.Words > 10 {
		return "", fmt.Errorf("words must be between 3 and 10")
	}
	sep := opts.Separator
	if sep == "" {
		sep = "-"
	}
	if len([]rune(sep)) > 8 {
		return "", fmt.Errorf("separator must be at most 8 characters")
	}
	out := make([]string, 0, opts.Words)
	for i := 0; i < opts.Words; i++ {
		word, err := sampleWord()
		if err != nil {
			return "", err
		}
		if opts.Capitalize {
			r := []rune(word)
			if r[0] >= 'a' && r[0] <= 'z' {
				r[0] = r[0] - 'a' + 'A'
			}
			word = string(r)
		}
		out = append(out, word)
	}
	return strings.Join(out, sep), nil
}

// sampleWord draws one word index uniformly. The acceptance threshold is
// 65536 - (65536 mod 1296), so every index has exactly the same probability.
func sampleWord() (string, error) {
	const threshold = 65536 - (65536 % WordCount)
	for {
		var b [2]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", fmt.Errorf("crypto/rand unavailable: %w", err)
		}
		v := uint16(b[0])<<8 | uint16(b[1])
		if int(v) < threshold {
			return words[v%WordCount], nil
		}
	}
}
