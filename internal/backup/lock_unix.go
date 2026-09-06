//go:build !windows

package backup

import (
	"os"
	"syscall"
)

// lockFileExclusive takes an advisory flock (LOCK_EX|LOCK_NB).
func lockFileExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
