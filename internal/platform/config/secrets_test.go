package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReadMasterKeyFileMissing(t *testing.T) {
	_, err := ReadMasterKeyFile(filepath.Join(t.TempDir(), "absent"))
	if err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestReadMasterKeyFileWrongLength(t *testing.T) {
	for _, n := range []int{0, 16, 31, 33, 64} {
		path := filepath.Join(t.TempDir(), "key")
		if err := os.WriteFile(path, make([]byte, n), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadMasterKeyFile(path); err == nil {
			t.Errorf("%d-byte key file accepted", n)
		}
	}
}

func TestReadMasterKeyFileValid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMasterKeyFile(path)
	if err != nil {
		t.Fatalf("valid key rejected: %v", err)
	}
	if len(got) != 32 {
		t.Fatalf("got %d bytes", len(got))
	}
}

func TestReadMasterKeyFileAcceptsDockerSecretStyleMode(t *testing.T) {
	// Docker Secrets mounts secrets root-owned mode 0444; that must be
	// readable by the application user.
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, make([]byte, 32), 0o444); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadMasterKeyFile(path); err != nil {
		t.Fatalf("0444 key file rejected: %v", err)
	}
}

func TestReadMasterKeyFileRejectsNonRegular(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadMasterKeyFile(dir); err == nil {
		t.Fatal("directory accepted as key file")
	}
}

func TestMasterKeyFileDefault(t *testing.T) {
	t.Setenv(MasterKeyFileEnv, "")
	if MasterKeyFile() != DefaultMasterKeyFile {
		t.Fatalf("default = %q", MasterKeyFile())
	}
	t.Setenv(MasterKeyFileEnv, "/tmp/other-key")
	if MasterKeyFile() != "/tmp/other-key" {
		t.Fatalf("override ignored: %q", MasterKeyFile())
	}
	_ = errors.New
}
