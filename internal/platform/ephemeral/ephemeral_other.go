//go:build !linux

package ephemeral

import "fmt"

func approvedEphemeralFS(string) (bool, error) {
	return false, fmt.Errorf("tmpfs validation is unavailable on this platform")
}
