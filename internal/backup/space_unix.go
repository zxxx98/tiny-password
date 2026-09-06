//go:build unix

package backup

import (
	"fmt"
	"syscall"
)

// freeBytes reports the available bytes on the filesystem holding path.
func freeBytes(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("stat free space: %w", err)
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
