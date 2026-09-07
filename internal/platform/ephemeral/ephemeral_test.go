package ephemeral

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareWorkDirCreatesAndCleansOnlyItsChild(t *testing.T) {
	if !IsTmpfs("/dev/shm") {
		t.Skip("/dev/shm is not an available tmpfs")
	}
	parent, err := os.MkdirTemp("/dev/shm", "tiny-password-ephemeral-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(parent)
	sentinel := filepath.Join(parent, "keep-me")
	if err := os.WriteFile(sentinel, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	child, cleanup, err := PrepareWorkDir(parent, "tiny-password-test-")
	if err != nil {
		t.Fatal(err)
	}
	if child == parent || filepath.Dir(child) != parent {
		t.Fatalf("child=%q parent=%q", child, parent)
	}
	if info, err := os.Stat(child); err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("work child is not a restricted directory: info=%v err=%v", info, err)
	}
	cleanup()
	if _, err := os.Stat(child); !os.IsNotExist(err) {
		t.Fatalf("child still exists after cleanup: %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("cleanup removed parent content: %v", err)
	}
}

func TestPrepareWorkDirRejectsNonTmpfsParent(t *testing.T) {
	if IsTmpfs(t.TempDir()) {
		t.Skip("test filesystem is tmpfs")
	}
	if _, _, err := PrepareWorkDir(t.TempDir(), "tiny-password-test-"); err == nil {
		t.Fatal("PrepareWorkDir accepted a non-tmpfs parent")
	}
}

func TestPrepareWorkDirSelectsTmpfsByDefault(t *testing.T) {
	if !IsTmpfs("/tmp") && !IsTmpfs("/dev/shm") {
		t.Skip("no default tmpfs candidate on this host")
	}
	child, cleanup, err := PrepareWorkDir("", "tiny-password-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !strings.HasPrefix(filepath.Base(child), "tiny-password-test-") {
		t.Fatalf("child does not use the requested prefix: %q", child)
	}
	if info, err := os.Stat(child); err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("default work child is not a restricted directory: info=%v err=%v", info, err)
	}
}
