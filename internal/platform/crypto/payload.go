// Package crypto implements the versioned payload encryption (XChaCha20-
// Poly1305) and the master key abstraction. Authorization must complete
// before any decryption happens; this package knows nothing about users.
package crypto

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"
)

// PayloadVersion is the current wire format version (design §10.3: versioned
// to leave room for key rotation and algorithm migration).
const PayloadVersion uint16 = 1

const (
	keySize   = 32
	nonceSize = 24 // XChaCha20
)

// MasterKey wraps the 256-bit instance master key.
type MasterKey struct {
	key [keySize]byte
}

// NewMasterKey validates raw key material. Never generate keys here.
func NewMasterKey(raw []byte) (*MasterKey, error) {
	if len(raw) != keySize {
		return nil, fmt.Errorf("master key must be exactly %d bytes, got %d", keySize, len(raw))
	}
	mk := &MasterKey{}
	copy(mk.key[:], raw)
	return mk, nil
}

// markerDomain separates the verification marker from every other use of the
// master key.
var markerDomain = []byte("tiny-password/master-key-verification/v1")

// Marker returns a deterministic, non-secret-checking HMAC of a fixed domain
// string. Stored in system_state to detect master key changes at startup.
func (k *MasterKey) Marker() string {
	mac := hmac.New(sha256.New, k.key[:])
	mac.Write(markerDomain)
	return base64.RawStdEncoding.EncodeToString(mac.Sum(nil))
}

// VerifyMarker reports whether marker was produced by this key.
func (k *MasterKey) VerifyMarker(marker string) bool {
	mac := hmac.New(sha256.New, k.key[:])
	mac.Write(markerDomain)
	want := mac.Sum(nil)
	got, err := base64.RawStdEncoding.DecodeString(marker)
	if err != nil {
		return false
	}
	return hmac.Equal(want, got)
}

// EncryptedPayload is the stored form of an encrypted field payload: the
// version header followed by a fresh nonce and the AEAD ciphertext.
type EncryptedPayload struct {
	Version    uint16
	Nonce      [nonceSize]byte
	Ciphertext []byte
}

// Encode serializes the payload for database storage:
// version (2 bytes LE) || nonce (24 bytes) || ciphertext.
func (p EncryptedPayload) Encode() []byte {
	out := make([]byte, 2+nonceSize+len(p.Ciphertext))
	binary.LittleEndian.PutUint16(out[0:2], p.Version)
	copy(out[2:2+nonceSize], p.Nonce[:])
	copy(out[2+nonceSize:], p.Ciphertext)
	return out
}

// DecodeEncryptedPayload parses the stored form.
func DecodeEncryptedPayload(blob []byte) (EncryptedPayload, error) {
	if len(blob) < 2+nonceSize+16 { // ciphertext must hold at least the Poly1305 tag
		return EncryptedPayload{}, fmt.Errorf("encrypted payload too short: %d bytes", len(blob))
	}
	var p EncryptedPayload
	p.Version = binary.LittleEndian.Uint16(blob[0:2])
	if p.Version != PayloadVersion {
		return EncryptedPayload{}, fmt.Errorf("unsupported payload version %d", p.Version)
	}
	copy(p.Nonce[:], blob[2:2+nonceSize])
	p.Ciphertext = make([]byte, len(blob)-2-nonceSize)
	copy(p.Ciphertext, blob[2+nonceSize:])
	return p, nil
}

// Encrypt seals plaintext under the master key with a fresh random nonce and
// the given additional authenticated data.
func (k *MasterKey) Encrypt(plaintext []byte, aad AAD) (EncryptedPayload, error) {
	aead, err := chacha20poly1305.NewX(k.key[:])
	if err != nil {
		return EncryptedPayload{}, fmt.Errorf("init aead: %w", err)
	}
	var nonce [nonceSize]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return EncryptedPayload{}, fmt.Errorf("generate nonce: %w", err)
	}
	ct := aead.Seal(nil, nonce[:], plaintext, aad.encode())
	p := EncryptedPayload{Version: PayloadVersion, Ciphertext: ct}
	p.Nonce = nonce
	return p, nil
}

// Decrypt opens an encrypted payload. Tampering with the nonce, ciphertext,
// version, or any AAD field fails authentication.
func (k *MasterKey) Decrypt(p EncryptedPayload, aad AAD) ([]byte, error) {
	if p.Version != PayloadVersion {
		return nil, fmt.Errorf("unsupported payload version %d", p.Version)
	}
	aead, err := chacha20poly1305.NewX(k.key[:])
	if err != nil {
		return nil, fmt.Errorf("init aead: %w", err)
	}
	plaintext, err := aead.Open(nil, p.Nonce[:], p.Ciphertext, aad.encode())
	if err != nil {
		return nil, fmt.Errorf("payload authentication failed")
	}
	return plaintext, nil
}

// EncryptEncoded / DecryptEncoded are convenience wrappers over the stored
// blob form used directly by repositories.
func (k *MasterKey) EncryptEncoded(plaintext []byte, aad AAD) ([]byte, error) {
	p, err := k.Encrypt(plaintext, aad)
	if err != nil {
		return nil, err
	}
	return p.Encode(), nil
}

func (k *MasterKey) DecryptEncoded(blob []byte, aad AAD) ([]byte, error) {
	p, err := DecodeEncryptedPayload(blob)
	if err != nil {
		return nil, err
	}
	return k.Decrypt(p, aad)
}

// DecryptColumns opens a payload stored as separate version/nonce/ciphertext
// columns (design §9 keeps the pieces apart). The nonce length is enforced
// here so a corrupted row fails closed instead of mis-sealing.
func (k *MasterKey) DecryptColumns(version uint16, nonce, ciphertext []byte, aad AAD) ([]byte, error) {
	if len(nonce) != nonceSize {
		return nil, fmt.Errorf("nonce must be %d bytes, got %d", nonceSize, len(nonce))
	}
	var n [nonceSize]byte
	copy(n[:], nonce)
	return k.Decrypt(EncryptedPayload{Version: version, Nonce: n, Ciphertext: ciphertext}, aad)
}

// IdempotencyMACKey derives a stable secret used only for request fingerprints.
// This domain must never be reused for public markers or payload encryption.
func (k *MasterKey) IdempotencyMACKey() []byte {
	mac := hmac.New(sha256.New, k.key[:])
	mac.Write([]byte("tiny-password/idempotency-fingerprint-key/v1"))
	return mac.Sum(nil)
}
