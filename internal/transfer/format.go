// Package transfer implements the personal import/export (design §6.5/§11,
// decision D10): a versioned, encrypted 7z archive holding the caller's own
// personal items and shared items they created — current versions only, no
// history or trash.
package transfer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// FormatVersion of the on-disk (inside-archive) transfer format.
const FormatVersion = 1

// Manifest is the archive's index file. It deliberately carries no account
// material: no password hashes, no sessions, no audit, no master keys — the
// import endpoint refuses archives whose manifest claims such fields.
type Manifest struct {
	Version int            `json:"version"`
	Files   []ManifestFile `json:"files"`
	Counts  map[string]int `json:"counts"`
}

// ManifestFile is one item payload file with its integrity digest.
type ManifestFile struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Scope  string `json:"scope"` // personal | shared
	Path   string `json:"path"`  // relative to the archive root
	SHA256 string `json:"sha256"`
}

// itemPayloadFile is one decrypted item payload as stored inside the
// archive: the full vault payload plus the tags.
type itemPayloadFile struct {
	ItemType string          `json:"item_type"`
	Tags     []string        `json:"tags,omitempty"`
	Favorite bool            `json:"favorite,omitempty"`
	Payload  json.RawMessage `json:"payload"`
}

// ExportInput carries the export request.
type ExportInput struct {
	Passphrase        string
	PassphraseConfirm string
}

const MaxPassphraseBytes = 1024

var ErrInvalidInput = errors.New("transfer: invalid input")

var transferIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// ValidateExportInput enforces the OpenAPI contract: 12..1024 characters and
// both entries equal, excluding line breaks (D04).
func ValidateExportInput(in ExportInput) error {
	if in.Passphrase != in.PassphraseConfirm {
		return fmt.Errorf("%w: passphrases do not match", ErrInvalidInput)
	}
	if len(in.Passphrase) < 12 || len(in.Passphrase) > MaxPassphraseBytes {
		return fmt.Errorf("%w: passphrase must be 12..1024 characters", ErrInvalidInput)
	}
	if stringsContainsAny(in.Passphrase, "\r\n") {
		return fmt.Errorf("%w: passphrase must not contain line breaks", ErrInvalidInput)
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
	if len(m.Files) == 0 {
		return fmt.Errorf("manifest contains no items")
	}
	if m.Counts == nil {
		return fmt.Errorf("manifest counts are missing")
	}
	seenIDs := make(map[string]struct{}, len(m.Files))
	seenPaths := make(map[string]struct{}, len(m.Files))
	actualCounts := make(map[string]int, len(m.Files))
	for _, f := range m.Files {
		if _, err := checkedManifestPath(f); err != nil {
			return err
		}
		if _, duplicate := seenIDs[f.ID]; duplicate {
			return fmt.Errorf("duplicate item id in manifest")
		}
		if _, duplicate := seenPaths[f.Path]; duplicate {
			return fmt.Errorf("duplicate item path in manifest")
		}
		seenIDs[f.ID] = struct{}{}
		seenPaths[f.Path] = struct{}{}
		if len(f.SHA256) != sha256.Size*2 {
			return fmt.Errorf("invalid item digest in manifest")
		}
		if _, err := hex.DecodeString(f.SHA256); err != nil {
			return fmt.Errorf("invalid item digest in manifest")
		}
		switch f.Type {
		case "login", "ssh_key", "credit_card", "identity", "secure_note", "secret":
		default:
			return fmt.Errorf("unknown item type %q in manifest", f.Type)
		}
		switch f.Scope {
		case "personal", "shared":
		default:
			return fmt.Errorf("unknown scope %q in manifest", f.Scope)
		}
		actualCounts[f.Type]++
	}
	if len(m.Counts) != len(actualCounts) {
		return fmt.Errorf("manifest counts do not match files")
	}
	total := 0
	for itemType, count := range m.Counts {
		if count <= 0 || actualCounts[itemType] != count {
			return fmt.Errorf("manifest counts do not match files")
		}
		total += count
	}
	if total != len(m.Files) {
		return fmt.Errorf("manifest counts do not match files")
	}
	return nil
}

// checkedManifestPath validates the archive-relative name immediately before
// it is joined to an extraction directory. The exact form prevents a
// manifest from selecting any file other than its declared item payload.
func checkedManifestPath(f ManifestFile) (string, error) {
	if !transferIDPattern.MatchString(f.ID) {
		return "", fmt.Errorf("invalid item id in manifest")
	}
	expectedPath := "items/" + f.ID + ".json"
	cleanPath := path.Clean(f.Path)
	if f.Path != expectedPath || f.Path == "." || path.IsAbs(f.Path) || strings.Contains(f.Path, `\`) || cleanPath != f.Path {
		return "", fmt.Errorf("unsafe item path in manifest")
	}
	return f.Path, nil
}
