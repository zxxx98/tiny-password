package bootstrap

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"log/slog"
	"sync"
)

// SetupTokenEvent is the only log event allowed to carry the setup token
// (decision D01). Log-scan allowlists must match exactly this name.
const SetupTokenEvent = "setup_token_issued"

// SetupToken is a one-time administrator initialization token. Its full
// value is printed once to the container log; only a SHA-256 digest is kept
// in process memory for verification.
type SetupToken struct {
	mu     sync.Mutex
	digest []byte
}

// NewSetupToken creates a high-entropy token and emits it to the log once.
func NewSetupToken(logger *slog.Logger) (*SetupToken, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	value := base64.RawURLEncoding.EncodeToString(raw)
	digest := sha256.Sum256([]byte(value))

	logger.Info(SetupTokenEvent,
		"token", value,
		"hint", "use this token once on the setup page to create the administrator",
	)
	return &SetupToken{digest: digest[:]}, nil
}

// Matches verifies the presented token in constant time.
func (t *SetupToken) Matches(input string) bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.digest == nil {
		return false
	}
	sum := sha256.Sum256([]byte(input))
	return subtle.ConstantTimeCompare(sum[:], t.digest) == 1
}

// Destroy wipes the verification material. Called after successful
// initialization (or when the token is otherwise retired) so the value can
// never be verified again.
func (t *SetupToken) Destroy() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := range t.digest {
		t.digest[i] = 0
	}
	t.digest = nil
}
