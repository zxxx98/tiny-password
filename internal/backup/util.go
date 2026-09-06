package backup

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"time"
)

// readRandom fills b with cryptographically random bytes.
func readRandom(b []byte) (int, error) { return rand.Read(b) }

func timeNowUnixNano() int64 { return time.Now().UnixNano() }

// cancelOrError reports whether err represents a canceled context.
func canceledOrError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// ensureDir creates dir with 0700 when missing (restricted scratch dirs).
func ensureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	return nil
}
