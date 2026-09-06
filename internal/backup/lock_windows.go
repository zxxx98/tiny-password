//go:build windows

package backup

import "os"

// lockFileExclusive is a no-op on windows builds (deployment targets are
// linux containers; the mutual exclusion guarantee applies there).
func lockFileExclusive(*os.File) error { return nil }

func unlockFile(*os.File) error { return nil }
