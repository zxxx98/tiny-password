package httpapi

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ErrCursorInvalid covers every rejected cursor: bad MAC, tampered payload,
// foreign actor, altered filters, expired, or unknown version. Handlers map
// it to 400 VALIDATION_ERROR without distinguishing causes (no oracle).
var ErrCursorInvalid = errors.New("invalid or expired cursor")

// CursorCodec produces authenticated opaque pagination cursors. A cursor is
// bound to the requesting actor, the exact filter set, the ordering position
// and an expiry. The MAC key is per-process: outstanding cursors do not
// survive restarts, which is acceptable for transient pagination state and
// avoids persisting derived secrets.
type CursorCodec struct {
	macKey []byte
	now    func() time.Time
	ttl    time.Duration
}

// NewCursorCodec requires a >=32-byte MAC key.
func NewCursorCodec(macKey []byte, ttl time.Duration) (*CursorCodec, error) {
	if len(macKey) < 32 {
		return nil, errors.New("cursor: MAC key must be at least 32 bytes")
	}
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	return &CursorCodec{macKey: macKey, now: time.Now, ttl: ttl}, nil
}

type cursorPayload struct {
	Version int      `json:"v"`
	Actor   string   `json:"a"`
	Filters string   `json:"f"`
	Sort    []string `json:"s"`
	Expires int64    `json:"e"`
}

const cursorVersion = 1

// Encode seals a cursor for actor under the given canonical filter string.
// Sort carries the keyset position (e.g. created_at then id of the last row).
func (c *CursorCodec) Encode(actor, filters string, sort []string) string {
	payload := cursorPayload{
		Version: cursorVersion,
		Actor:   actor,
		Filters: filters,
		Sort:    sort,
		Expires: c.now().Add(c.ttl).Unix(),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		panic("httpapi: cursor payload marshal: " + err.Error())
	}
	body := base64.RawURLEncoding.EncodeToString(raw)
	return body + "." + c.sign(body)
}

// maxCursorTokenBytes bounds the accepted token before any work; the OpenAPI
// contract caps cursors at 512 characters, this allows headroom for legitimate
// encodings while rejecting abusive payloads.
const maxCursorTokenBytes = 4 << 10

// Decode verifies the MAC, expiry, actor binding and filter binding, then
// returns the stored sort position.
func (c *CursorCodec) Decode(token, actor, filters string) ([]string, error) {
	if len(token) > maxCursorTokenBytes {
		return nil, ErrCursorInvalid
	}
	body, sig, ok := strings.Cut(token, ".")
	if !ok || body == "" || sig == "" {
		return nil, ErrCursorInvalid
	}
	if !hmac.Equal([]byte(sig), []byte(c.sign(body))) {
		return nil, ErrCursorInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return nil, ErrCursorInvalid
	}
	var payload cursorPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, ErrCursorInvalid
	}
	if payload.Version != cursorVersion {
		return nil, ErrCursorInvalid
	}
	if payload.Actor != actor || payload.Filters != filters {
		return nil, ErrCursorInvalid
	}
	if c.now().Unix() > payload.Expires {
		return nil, ErrCursorInvalid
	}
	if len(payload.Sort) > 8 {
		return nil, ErrCursorInvalid
	}
	return payload.Sort, nil
}

func (c *CursorCodec) sign(body string) string {
	mac := hmac.New(sha256.New, c.macKey)
	mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// NewCursorMACKey returns a fresh per-process cursor secret.
func NewCursorMACKey() ([]byte, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}
