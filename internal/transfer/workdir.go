package transfer

import (
	"fmt"
	"os"
)

// PrepareWorkDir selects or validates a tmpfs parent and creates a private
// per-process child below it. The caller owns the returned child cleanup; the
// parent is never removed or chmod-ed. This keeps a custom TP_TRANSFER_WORK_DIR
// useful as an operator-managed mount while ensuring one process cannot sweep
// another process's previews.
func PrepareWorkDir(configured string) (string, func(), error) {
	parent := configured
	if parent == "" {
		var err error
		parent, err = selectEphemeralRoot()
		if err != nil {
			return "", nil, err
		}
	} else {
		if err := ensureDirectory(parent); err != nil {
			return "", nil, err
		}
		ok, err := approvedEphemeralFS(parent)
		if err != nil {
			return "", nil, fmt.Errorf("transfer work directory is unavailable")
		}
		if !ok {
			return "", nil, fmt.Errorf("transfer work directory must be on tmpfs")
		}
	}

	child, err := os.MkdirTemp(parent, "tiny-password-transfer-")
	if err != nil {
		return "", nil, fmt.Errorf("transfer work directory is unavailable")
	}
	if err := os.Chmod(child, 0o700); err != nil {
		_ = os.RemoveAll(child)
		return "", nil, fmt.Errorf("transfer work directory is unavailable")
	}
	return child, func() { _ = os.RemoveAll(child) }, nil
}

func selectEphemeralRoot() (string, error) {
	// /tmp is the normal container mount. /dev/shm is a reliable Linux tmpfs
	// fallback when a bare host's /tmp is backed by overlayfs/ext4. Include an
	// operator-provided TMPDIR only as an additional candidate; every choice is
	// checked rather than trusted by name.
	candidates := []string{os.TempDir(), "/tmp", "/dev/shm"}
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if _, duplicate := seen[candidate]; duplicate {
			continue
		}
		seen[candidate] = struct{}{}
		if err := ensureDirectory(candidate); err != nil {
			continue
		}
		ok, err := approvedEphemeralFS(candidate)
		if err == nil && ok {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no approved tmpfs available for transfer staging")
}

func ensureDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("not a directory")
	}
	return nil
}

// isLinuxEphemeralFS is intentionally kept small for tests without exposing
// the platform-specific statfs implementation as public API.
func isLinuxEphemeralFS(path string) bool {
	ok, err := approvedEphemeralFS(path)
	return err == nil && ok
}
