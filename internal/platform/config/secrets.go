// Package config loads configuration and secret files. Secrets are only ever
// read from files mounted by the operator (Docker Secrets); the application
// never generates or persists replacement secrets.
package config

import (
	"fmt"
	"os"
)

// MasterKeyFileEnv selects the master key secret path.
const MasterKeyFileEnv = "TP_MASTER_KEY_FILE"

// DefaultMasterKeyFile is the conventional Docker Secrets mount point.
const DefaultMasterKeyFile = "/run/secrets/master_key"

// MasterKeyFile returns the configured master key path.
func MasterKeyFile() string {
	if v := os.Getenv(MasterKeyFileEnv); v != "" {
		return v
	}
	return DefaultMasterKeyFile
}

// ReadMasterKeyFile reads exactly 32 bytes from the given secret file.
// Missing files and wrong lengths are hard errors: the application must
// never auto-generate a replacement key. File ownership/mode are an
// operator concern (Docker Secrets mount secrets root-owned mode 0444).
func ReadMasterKeyFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("master key secret %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("master key secret %s is not a regular file", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read master key secret %s: %w", path, err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("master key secret %s must be exactly 32 bytes, got %d", path, len(raw))
	}
	return raw, nil
}
