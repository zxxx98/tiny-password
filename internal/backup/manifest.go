// Package backup implements whole-instance backups (design §11): a
// consistent SQLite snapshot, the master key, and a verified manifest
// packed into one passphrase-encrypted 7z archive. Restore never runs
// inside the web process (T25); this package only produces and verifies
// archives. Secrets never appear in logs, errors, or audit rows.
package backup

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/tiny-password/tiny-password/internal/platform/ident"
)

// FormatVersion is the backup manifest format version. Bump on any
// incompatible change; restore rejects formats outside its compatibility
// range (T25).
const FormatVersion = 1

// Archive entry paths. Restore verifies the extracted set against exactly
// this whitelist — anything extra or missing fails the backup.
const (
	ManifestPath  = "manifest.json"
	SnapshotPath  = "db/tiny-password.db"
	MasterKeyPath = "secrets/master_key"
)

// ManifestFile records one archived file's size and digest.
type ManifestFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Manifest is the instance backup manifest (design §11.1).
type Manifest struct {
	FormatVersion int            `json:"format_version"`
	AppVersion    string         `json:"app_version"`
	SchemaVersion int64          `json:"schema_version"`
	BackupID      string         `json:"backup_id"`
	CreatedAt     string         `json:"created_at"`
	InstanceID    string         `json:"instance_id"`
	Arch          string         `json:"arch"`
	Files         []ManifestFile `json:"files"`
}

// allowedManifestPaths is the closed set of archivable files. A config
// snapshot may join later; anything not listed here must never enter an
// archive (no R2 credentials, tunnel tokens, backup passphrases, TLS keys,
// or runtime logs — design §11.1).
var allowedManifestPaths = map[string]bool{
	SnapshotPath:  true,
	MasterKeyPath: true,
}

// Check validates the manifest's self-consistency: format version, allowed
// paths, and digest/size presence.
func (m *Manifest) Check() error {
	if m.FormatVersion != FormatVersion {
		return fmt.Errorf("unsupported backup format version %d", m.FormatVersion)
	}
	if m.BackupID == "" || m.InstanceID == "" {
		return errors.New("manifest missing ids")
	}
	if _, err := time.Parse(time.RFC3339Nano, m.CreatedAt); err != nil {
		return errors.New("manifest created_at invalid")
	}
	if len(m.Files) == 0 {
		return errors.New("manifest lists no files")
	}
	seen := map[string]bool{}
	for _, f := range m.Files {
		if !allowedManifestPaths[f.Path] {
			return fmt.Errorf("manifest path %q is not allowed", f.Path)
		}
		if seen[f.Path] {
			return fmt.Errorf("manifest lists %q twice", f.Path)
		}
		seen[f.Path] = true
		if f.Size <= 0 || len(f.SHA256) != 64 {
			return fmt.Errorf("manifest entry %q lacks size or digest", f.Path)
		}
	}
	return nil
}

// instanceIDKey stores the stable instance identifier in system_state.
const instanceIDKey = "instance_id"

// EnsureInstanceID returns the instance's stable identifier, creating one on
// first use. The value is non-sensitive (it identifies backups, not data).
func EnsureInstanceID(db *sql.DB) (string, error) {
	var id string
	err := db.QueryRow("SELECT value FROM system_state WHERE key = ?", instanceIDKey).Scan(&id)
	if err == nil && id != "" {
		return id, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("read instance id: %w", err)
	}
	id = ident.NewUUIDv7()
	_, err = db.Exec(
		"INSERT INTO system_state (key, value, updated_at) VALUES (?, ?, ?) ON CONFLICT(key) DO NOTHING",
		instanceIDKey, id, time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return "", fmt.Errorf("store instance id: %w", err)
	}
	// A concurrent creator may have won; read back the authoritative value.
	var stored string
	if err := db.QueryRow("SELECT value FROM system_state WHERE key = ?", instanceIDKey).Scan(&stored); err != nil {
		return "", fmt.Errorf("reread instance id: %w", err)
	}
	return stored, nil
}

// fileDigest writes size and SHA-256 for one staged file.
func fileDigest(root, relPath string) (ManifestFile, error) {
	raw, err := os.ReadFile(filepath.Join(root, relPath))
	if err != nil {
		return ManifestFile{}, fmt.Errorf("stage file %s: %w", relPath, err)
	}
	sum := sha256.Sum256(raw)
	return ManifestFile{Path: relPath, Size: int64(len(raw)), SHA256: hex.EncodeToString(sum[:])}, nil
}

// writeManifestFile serializes the manifest into the staging root.
func writeManifestFile(root string, m *Manifest) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	return os.WriteFile(filepath.Join(root, ManifestPath), raw, 0o600)
}

// readManifestFile parses and checks the manifest from an extracted tree.
func readManifestFile(root string) (*Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(root, ManifestPath))
	if err != nil {
		return nil, errors.New("manifest missing")
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, errors.New("manifest unreadable")
	}
	if err := (&m).Check(); err != nil {
		return nil, err
	}
	return &m, nil
}

// goArchLabel is the architecture stamp recorded in every manifest.
func goArchLabel() string { return runtime.GOOS + "/" + runtime.GOARCH }
