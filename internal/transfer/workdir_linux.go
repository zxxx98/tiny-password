//go:build linux

package transfer

import "golang.org/x/sys/unix"

const tmpfsMagic = 0x01021994

func approvedEphemeralFS(path string) (bool, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return false, err
	}
	return uint64(stat.Type) == tmpfsMagic, nil
}
