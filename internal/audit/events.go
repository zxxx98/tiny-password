// Package audit implements the redacted audit trail (design §7.3): stable
// event constants, a strict field allowlist, and writes that share the
// caller's transaction so a successful change never loses its audit record.
// Audit rows carry opaque ids only — never usernames, titles, addresses,
// tags, search terms, secrets, or raw underlying errors.
package audit

import "errors"

// Event names are stable API-level constants; the allowlist below is the only
// set Record accepts, so a typo'd or injected event name fails closed.
const (
	EventSetupSuccess        = "setup.success"
	EventSetupFailure        = "setup.failure"
	EventLoginSuccess        = "auth.login.success"
	EventLoginFailure        = "auth.login.failure"
	EventLogout              = "auth.logout"
	EventSessionRevoked      = "auth.session.revoked"
	EventPasswordChanged     = "auth.password.changed"
	EventUserCreated         = "user.created"
	EventUserDisabled        = "user.disabled"
	EventUserEnabled         = "user.enabled"
	EventUserDeleted         = "user.deleted"
	EventUserSessionsRevoked = "user.sessions.revoked"
	// Item lifecycle events (design §7.3). Field reveals and copies are
	// recorded by the workspace milestone.
	EventVaultItemCreated         = "vault.item.created"
	EventVaultItemUpdated         = "vault.item.updated"
	EventVaultItemViewed          = "vault.item.viewed"
	EventVaultItemTrashed         = "vault.item.trashed"
	EventVaultItemRestored        = "vault.item.restored"
	EventVaultItemPurged          = "vault.item.purged"
	EventVaultItemHistoryRestored = "vault.item.history_restored"
	// Sensitive-field interaction events (design §6.4, §7.3): the field
	// category is recorded, never the value.
	EventVaultSecretRevealed = "vault.secret.revealed"
	EventVaultSecretCopied   = "vault.secret.copied"
	// Personal import/export (design §7.3): success events only; the archive
	// passphrase never appears anywhere.
	EventTransferExported = "transfer.exported"
	EventTransferImported = "transfer.imported"
	// Instance backup lifecycle (design §7.3, T24): retention deletions
	// carry the opaque object identifier only (run id / object key), never
	// passphrases or contents.
	EventBackupRetentionDeleted = "backup.retention.deleted"
)

var allowlist = map[string]bool{
	EventSetupSuccess:             true,
	EventSetupFailure:             true,
	EventLoginSuccess:             true,
	EventLoginFailure:             true,
	EventLogout:                   true,
	EventSessionRevoked:           true,
	EventPasswordChanged:          true,
	EventUserCreated:              true,
	EventUserDisabled:             true,
	EventUserEnabled:              true,
	EventUserDeleted:              true,
	EventUserSessionsRevoked:      true,
	EventVaultItemCreated:         true,
	EventVaultItemUpdated:         true,
	EventVaultItemViewed:          true,
	EventVaultItemTrashed:         true,
	EventVaultItemRestored:        true,
	EventVaultItemPurged:          true,
	EventVaultItemHistoryRestored: true,
	EventVaultSecretRevealed:      true,
	EventVaultSecretCopied:        true,
	EventTransferExported:         true,
	EventTransferImported:         true,
	EventBackupRetentionDeleted:   true,
}

// Results are restricted to the database CHECK constraint's domain.
const (
	ResultSuccess = "success"
	ResultFailure = "failure"
)

// Target types kept deliberately coarse; target_id stays opaque.
const (
	TargetUser    = "user"
	TargetSession = "session"
	TargetItem    = "item"
	TargetBackup  = "backup"
)

// Anonymous marks an unresolved actor (e.g. failed login for an unknown
// username). It is the only literal allowed in actor_id besides user ids.
const Anonymous = "anonymous"

// Field length caps mirror the schema and the OpenAPI AuditEvent schema.
const (
	MaxEventLength      = 64
	MaxTargetTypeLength = 32
	MaxIDLength         = 64
)

// ValidEventName reports whether name is in the allowlist; query filters and
// writers both funnel through it.
func ValidEventName(name string) bool { return allowlist[name] }

// ErrEventNotAllowed reports an event name outside the allowlist.
var ErrEventNotAllowed = errors.New("audit: event not in the allowed list")

// ErrFieldTooLong reports a field exceeding its cap; callers pass server
// controlled values, so this indicates a bug, not user input.
var ErrFieldTooLong = errors.New("audit: field exceeds the allowed length")

// ErrInvalidResult reports a result outside success/failure.
var ErrInvalidResult = errors.New("audit: invalid result")
