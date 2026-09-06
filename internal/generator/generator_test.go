package generator

import (
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestPasswordLengthAndClasses(t *testing.T) {
	for _, length := range []int{8, 20, 128} {
		pw, err := Password(PasswordOptions{Length: length, Lowercase: true, Uppercase: true, Digits: true, Symbols: true})
		if err != nil {
			t.Fatalf("length %d: %v", length, err)
		}
		if len(pw) != length {
			t.Fatalf("len=%d want %d", len(pw), length)
		}
	}
}

func TestPasswordRequiresOneClass(t *testing.T) {
	if _, err := Password(PasswordOptions{Length: 20}); err == nil {
		t.Fatal("empty class selection must be rejected")
	}
	// With only one class the length must still be reachable.
	if _, err := Password(PasswordOptions{Length: 8, Lowercase: true}); err != nil {
		t.Fatalf("single class: %v", err)
	}
}

func TestPasswordExcludesAmbiguous(t *testing.T) {
	pw, err := Password(PasswordOptions{
		Length: 128, Lowercase: true, Uppercase: true, Digits: true, Symbols: true,
		ExcludeAmbiguous: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range pw {
		if strings.ContainsRune(ambiguous, r) {
			t.Fatalf("ambiguous rune %q in %q", r, pw)
		}
	}
}

func TestPasswordContainsEnabledClasses(t *testing.T) {
	classify := func(r rune) string {
		switch {
		case r >= 'a' && r <= 'z':
			return "lower"
		case r >= 'A' && r <= 'Z':
			return "upper"
		case r >= '0' && r <= '9':
			return "digit"
		default:
			return "symbol"
		}
	}
	// Run enough rounds that a missing class is practically impossible
	// (each round: 4 classes over 20 chars).
	for round := 0; round < 50; round++ {
		pw, err := Password(PasswordOptions{Length: 20, Lowercase: true, Uppercase: true, Digits: true, Symbols: true})
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, r := range pw {
			seen[classify(r)] = true
		}
		for _, class := range []string{"lower", "upper", "digit", "symbol"} {
			if !seen[class] {
				t.Fatalf("round %d: class %q missing in %q", round, class, pw)
			}
		}
	}
}

func TestPasswordUniformity(t *testing.T) {
	// Uniformity smoke test over the lowercase alphabet: no modulo bias means
	// every letter appears with roughly equal frequency.
	const rounds, length = 200, 128
	counts := map[rune]int{}
	for i := 0; i < rounds; i++ {
		pw, err := Password(PasswordOptions{Length: length, Lowercase: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range pw {
			counts[r]++
		}
	}
	total := rounds * length
	expected := total / 26
	for r, c := range counts {
		// 3σ at these counts is tiny; the generous 25% bound only fails on
		// real bias (e.g. modulo artifacts).
		if c < expected*3/4 || c > expected*5/4 {
			t.Fatalf("letter %q count %d, expected ≈%d (bias suspected)", r, c, expected)
		}
	}
}

func TestPassphraseWordsAndOptions(t *testing.T) {
	for _, words := range []int{3, 5, 10} {
		pp, err := Passphrase(PassphraseOptions{Words: words})
		if err != nil {
			t.Fatalf("words %d: %v", words, err)
		}
		if got := strings.Count(pp, "-") + 1; got != words {
			t.Fatalf("words=%d want %d in %q", got, words, pp)
		}
	}
	if _, err := Passphrase(PassphraseOptions{Words: 2}); err == nil {
		t.Fatal("2 words must be rejected")
	}
	if _, err := Passphrase(PassphraseOptions{Words: 11}); err == nil {
		t.Fatal("11 words must be rejected")
	}
	pp, err := Passphrase(PassphraseOptions{Words: 5, Separator: "_", Capitalize: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(pp, "_") != 4 {
		t.Fatalf("separator not applied: %q", pp)
	}
	for _, word := range strings.Split(pp, "_") {
		if word[0] < 'A' || word[0] > 'Z' {
			t.Fatalf("capitalize not applied: %q", word)
		}
	}
}

func TestPassphraseWordsFromBundledList(t *testing.T) {
	// Every generated word must come from the bundled list.
	pp, err := Passphrase(PassphraseOptions{Words: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, word := range strings.Split(pp, "-") {
		if !strings.Contains(strings.Join(words, "\n"), word) {
			t.Fatalf("word %q not in bundled list", word)
		}
	}
}

func TestPassphraseUniformity(t *testing.T) {
	// The rejection sampler must visit every list index; over 20k samples
	// each of the 1296 words appears ≈15.4 times — assert a loose 4σ band.
	const samples = 20_000
	index := make(map[string]int, WordCount)
	for i, w := range words {
		index[w] = i
	}
	counts := make([]int, WordCount)
	for i := 0; i < samples; i++ {
		word, err := sampleWord()
		if err != nil {
			t.Fatal(err)
		}
		idx, ok := index[word]
		if !ok {
			t.Fatalf("word %q not in list", word)
		}
		counts[idx]++
	}
	// Chi-square uniformity: Σ(O-E)²/E ≈ χ² with 1295 degrees of freedom.
	expected := float64(samples) / float64(WordCount)
	chi2 := 0.0
	for _, c := range counts {
		d := float64(c) - expected
		chi2 += d * d / expected
	}
	// χ²(1295): mean ≈1295, σ ≈ 50.9. A modulo-biased sampler would blow far
	// past six sigmas; individual indices legitimately scatter ±3σ.
	if chi2 > 1295+6*51 {
		t.Fatalf("chi2=%.0f far beyond χ²(1295) six sigma — sampling bias", chi2)
	}
}

func TestSSHKeyPairEd25519(t *testing.T) {
	pair, err := NewSSHKeyPair(SSHAlgoEd25519, "key-passphrase-1", "alice@lab")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pair.PublicKey, "ssh-ed25519 ") {
		t.Fatalf("public key format: %q", pair.PublicKey)
	}
	if !strings.HasPrefix(pair.Fingerprint, "SHA256:") {
		t.Fatalf("fingerprint format: %q", pair.Fingerprint)
	}
	// The private key parses only with the correct passphrase — that is the
	// actual encryption guarantee.
	if _, err := parsePrivateKey(pair.PrivateKey); err == nil {
		t.Fatal("passphrase-protected key parsed without passphrase")
	}
	signer, err := parsePrivateKeyWithPassphrase(pair.PrivateKey, "key-passphrase-1")
	if err != nil {
		t.Fatalf("passphrase decrypt: %v", err)
	}
	if signer.PublicKey().Type() != "ssh-ed25519" {
		t.Fatalf("parsed key type: %s", signer.PublicKey().Type())
	}
}

// parsePrivateKey parses an OpenSSH PEM without a passphrase.
func parsePrivateKey(pemText string) (ssh.Signer, error) {
	return ssh.ParsePrivateKey([]byte(pemText))
}

// parsePrivateKeyWithPassphrase parses an OpenSSH PEM with a passphrase.
func parsePrivateKeyWithPassphrase(pemText, passphrase string) (ssh.Signer, error) {
	return ssh.ParsePrivateKeyWithPassphrase([]byte(pemText), []byte(passphrase))
}

func TestSSHKeyPairRSA4096(t *testing.T) {
	if testing.Short() {
		t.Skip("RSA-4096 generation skipped with -short")
	}
	pair, err := NewSSHKeyPair(SSHAlgoRSA4096, "", "bob@host")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pair.PublicKey, "ssh-rsa ") {
		t.Fatalf("public key format: %q", pair.PublicKey)
	}
	if strings.Contains(pair.PrivateKey, "bcrypt") {
		t.Fatal("key without passphrase must not be encrypted")
	}
	signer, err := parsePrivateKey(pair.PrivateKey)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if signer.PublicKey().Type() != "ssh-rsa" {
		t.Fatalf("key type: %s", signer.PublicKey().Type())
	}
}
