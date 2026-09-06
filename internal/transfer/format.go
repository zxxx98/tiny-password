// Package transfer implements the personal import/export (design §6.5/§11,
// decision D10): a versioned, encrypted 7z archive holding the caller's own
// personal items and shared items they created — current versions only, no
// history or trash.
package transfer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// FormatVersion of the on-disk (inside-archive) transfer format.
const FormatVersion = 1

// Manifest is the archive's index file. It deliberately carries no account
// material: no password hashes, no sessions, no audit, no master keys — the
// import endpoint refuses archives whose manifest claims such fields.
type Manifest struct {
	Version int                  `json:"version"`
	Files   []ManifestFile       `json:"files"`
	Counts  map[string]int       `json:"counts"`
}

// ManifestFile is one item payload file with its integrity digest.
type ManifestFile struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Scope   string `json:"scope"` // personal | shared
	Path    string `json:"path"`  // relative to the archive root
	SHA256  string `json:"sha256"`
}

// itemPayloadFile is one decrypted item payload as stored inside the
// archive: the full vault payload plus the tags.
type itemPayloadFile struct {
	ItemType string          `json:"item_type"`
	Tags     []string        `json:"tags,omitempty"`
	Payload  json.RawMessage `json:"payload"`
}

// ExportInput carries the export request.
type ExportInput struct {
	Passphrase        string
	PassphraseConfirm string
}

// ValidateExportInput enforces the OpenAPI contract: 12..1024 characters and
// both entries equal, excluding line breaks (D04).
func ValidateExportInput(in ExportInput) error {
	if in.Passphrase != in.PassphraseConfirm {
		return fmt.Errorf("passphrases do not match")
	}
	if len(in.Passphrase) < 12 || len(in.Passphrase) > 1024 {
		return fmt.Errorf("passphrase must be 12..1024 characters")
	}
	if stringsContainsAny(in.Passphrase, "\r\n") {
		return fmt.Errorf("passphrase must not contain line breaks")
	}
	return nil
}

func stringsContainsAny(s, chars string) bool {
	return strings.ContainsAny(s, chars)
}

// sha256Hex digests one file's bytes for the manifest.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ManifestSelfCheck rejects manifests that claim account material — the
// import must never accept anything beyond item payloads (design §11.4).
func ManifestSelfCheck(m Manifest) error {
	if m.Version != FormatVersion {
		return fmt.Errorf("unsupported transfer format version %d", m.Version)
	}
	for _, f := range m.Files {
		switch f.Type {
		case "login", "ssh_key", "credit_card", "identity", "secure_note":
		default:
			return fmt.Errorf("unknown item type %q in manifest", f.Type)
		}
		switch f.Scope {
		case "personal", "shared":
		default:
			return fmt.Errorf("unknown scope %q in manifest", f.Scope)
		}
	}
	return nil
}
