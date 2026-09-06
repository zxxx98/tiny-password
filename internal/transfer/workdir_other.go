//go:build !linux

package transfer

import "fmt"

func approvedEphemeralFS(string) (bool, error) {
	return false, fmt.Errorf("tmpfs validation is unavailable on this platform")
}
