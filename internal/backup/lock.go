package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrDataDirLocked reports that the data directory is held by another
// process — under normal operation that means the HTTP service is still
// running and restore must not proceed (design §11.5 step 1, T25).
var ErrDataDirLocked = errors.New("restore: data directory is locked by a running service")

// DataDirLock is the held exclusive lock on a data directory.
type DataDirLock struct {
	file *os.File
}

// AcquireDataDirLock takes an exclusive non-blocking lock on
// <dataDir>/service.lock. The HTTP service holds it for its whole lifetime;
// the offline restore command takes it too, so exactly one of them can run
// per data directory.
func AcquireDataDirLock(dataDir string) (*DataDirLock, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("restore: create data dir: %w", err)
	}
	path := filepath.Join(dataDir, "service.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("restore: open lock file: %w", err)
	}
	if err := lockFileExclusive(f); err != nil {
		f.Close()
		return nil, ErrDataDirLocked
	}
	return &DataDirLock{file: f}, nil
}

// Release drops the lock.
func (l *DataDirLock) Release() {
	if l == nil || l.file == nil {
		return
	}
	_ = unlockFile(l.file)
	_ = l.file.Close()
	l.file = nil
}
