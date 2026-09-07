// Package ephemeral provides restricted staging directories on verified
// tmpfs mounts (D11: sensitive intermediates such as plaintext exports and
// master key copies must never land on persistent storage, and a failure
// never falls back to a regular disk). A parent is either discovered among
// the platform's tmpfs mounts or — when the operator configures one —
// accepted only after an on-disk filesystem check. Work always happens
// inside a private 0700 child that the caller cleans up.
package ephemeral

import (
	"fmt"
	"os"
)

// PrepareWorkDir returns a private 0700 child of a tmpfs parent plus a
// cleanup that removes only that child; the parent itself is never removed
// or re-chmod-ed, so an operator-managed mount keeps its content.
//
// An empty configured value selects the first usable tmpfs among the usual
// ephemeral mounts. A configured parent is refused unless it really is
// tmpfs — by name alone it is never trusted.
func PrepareWorkDir(configured, prefix string) (string, func(), error) {
	parent := configured
	if parent == "" {
		var err error
		parent, err = selectRoot()
		if err != nil {
			return "", nil, err
		}
	} else {
		if err := ensureDirectory(parent); err != nil {
			return "", nil, err
		}
		ok, err := approvedEphemeralFS(parent)
		if err != nil {
			return "", nil, fmt.Errorf("work directory is unavailable: %w", err)
		}
		if !ok {
			return "", nil, fmt.Errorf("work directory must be on tmpfs")
		}
	}

	child, err := os.MkdirTemp(parent, prefix)
	if err != nil {
		return "", nil, fmt.Errorf("work directory is unavailable: %w", err)
	}
	if err := os.Chmod(child, 0o700); err != nil {
		_ = os.RemoveAll(child)
		return "", nil, fmt.Errorf("work directory is unavailable: %w", err)
	}
	return child, func() { _ = os.RemoveAll(child) }, nil
}

// IsTmpfs reports whether path sits on a tmpfs filesystem.
func IsTmpfs(path string) bool {
	ok, err := approvedEphemeralFS(path)
	return err == nil && ok
}

func selectRoot() (string, error) {
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
		if IsTmpfs(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no approved tmpfs available for staging")
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
