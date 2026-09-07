package transfer

import "github.com/tiny-password/tiny-password/internal/platform/ephemeral"

// PrepareWorkDir selects or validates a tmpfs parent and creates a private
// per-process child below it. The caller owns the returned child cleanup; the
// parent is never removed or chmod-ed. This keeps a custom TP_TRANSFER_WORK_DIR
// useful as an operator-managed mount while ensuring one process cannot sweep
// another process's previews. The tmpfs rules are shared with backup/restore
// staging via platform/ephemeral (D11).
func PrepareWorkDir(configured string) (string, func(), error) {
	return ephemeral.PrepareWorkDir(configured, "tiny-password-transfer-")
}

// isLinuxEphemeralFS is intentionally kept small for tests without exposing
// the platform-specific statfs implementation as public API.
func isLinuxEphemeralFS(path string) bool {
	return ephemeral.IsTmpfs(path)
}
