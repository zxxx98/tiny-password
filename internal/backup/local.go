package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// LocalStore publishes verified archives into the local backup directory.
// Publication is atomic within the target directory: the verified archive
// is copied under a hidden temporary name, its digest re-checked, and only
// then renamed to the final name on the same filesystem (design §11.3 step
// 7). A failure never leaves a partially visible backup.
type LocalStore struct {
	Dir string
}

// Publish copies the staged archive into the store under name and returns
// the final path, size, and SHA-256 of the published file.
func (s LocalStore) Publish(stagedPath, name string) (string, int64, string, error) {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return "", 0, "", fmt.Errorf("%w: create backup dir", ErrPublish)
	}
	staged, err := os.Open(stagedPath)
	if err != nil {
		return "", 0, "", fmt.Errorf("%w: staged archive unreadable", ErrPublish)
	}
	defer staged.Close()

	wantSum := sha256.New()
	size, err := io.Copy(wantSum, staged)
	if err != nil {
		return "", 0, "", fmt.Errorf("%w: staged archive unreadable", ErrPublish)
	}
	want := hex.EncodeToString(wantSum.Sum(nil))

	if _, err := staged.Seek(0, io.SeekStart); err != nil {
		return "", 0, "", fmt.Errorf("%w: staged archive unreadable", ErrPublish)
	}
	tmpPath := filepath.Join(s.Dir, ".publish-"+randomName())
	tmp, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", 0, "", fmt.Errorf("%w: staging file", ErrPublish)
	}
	gotSum := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, gotSum), staged); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", 0, "", fmt.Errorf("%w: copy failed", ErrPublish)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", 0, "", fmt.Errorf("%w: fsync failed", ErrPublish)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return "", 0, "", fmt.Errorf("%w: close failed", ErrPublish)
	}
	// The byte copy must reproduce the verified archive exactly; only then
	// is the rename (atomic, same filesystem) allowed to publish it.
	if hex.EncodeToString(gotSum.Sum(nil)) != want {
		os.Remove(tmpPath)
		return "", 0, "", fmt.Errorf("%w: publish digest mismatch", ErrPublish)
	}
	finalPath := filepath.Join(s.Dir, name)
	if err := os.Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		return "", 0, "", fmt.Errorf("%w: rename failed", ErrPublish)
	}
	syncDir(s.Dir)
	return finalPath, size, want, nil
}

// randomName produces a collision-resistant temporary file name.
func randomName() string {
	var b [8]byte
	if _, err := readRandom(b[:]); err != nil {
		// Fall back to time-based uniqueness; the rename target is unique
		// through the run id anyway.
		return fmt.Sprintf("t%d", timeNowUnixNano())
	}
	return hex.EncodeToString(b[:])
}

func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// ErrPublish reports a local publication failure.
var ErrPublish = errors.New("backup: publish failed")
