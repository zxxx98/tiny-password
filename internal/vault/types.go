// Package vault implements the encrypted item lifecycle (design §6):
// typed payloads sealed under the master key, authorization before any
// decryption, and optimistic-locking updates that archive the previous
// version in the same transaction. Business fields never exist as
// plaintext columns — the database stores only policy and lifecycle
// metadata plus the ciphertext.
package vault

import "errors"

// Item types (design §6.2). The set is fixed for V1 and mirrored by the
// database CHECK constraint and the OpenAPI enum.
const (
	TypeLogin      = "login"
	TypeSSHKey     = "ssh_key"
	TypeCreditCard = "credit_card"
	TypeIdentity   = "identity"
	TypeSecureNote = "secure_note"
	TypeSecret     = "secret"
)

// ItemTypes is the closed set of valid item types.
var ItemTypes = map[string]bool{
	TypeLogin:      true,
	TypeSSHKey:     true,
	TypeCreditCard: true,
	TypeIdentity:   true,
	TypeSecureNote: true,
	TypeSecret:     true,
}

// PayloadSchemaVersion is the version of the JSON envelope inside the
// ciphertext. It is independent of the AEAD wire format version and lets
// the payload grammar evolve without re-encrypting.
const PayloadSchemaVersion = 1

// TimestampFormat is the fixed-width UTC format used for every stored
// timestamp; keyset pagination compares them as strings, which is only
// sound for uniform widths.
const TimestampFormat = "2006-01-02T15:04:05.000000000Z"

// MaxPayloadBytes caps the marshaled plaintext envelope (payload fields
// plus tags) at 256 KiB before encryption (decision D09).
const MaxPayloadBytes = 256 << 10

// Scopes accepted on create and cross-vault moves.
var Scopes = map[string]bool{"personal": true, "shared": true}

// Errors surfaced to the HTTP layer, which maps each to a stable code.
var (
	// ErrNotFound reports an unknown item or one the caller cannot read.
	// Callers must not learn whether an unreadable id exists.
	ErrNotFound = errors.New("vault: item not found")
	// ErrForbidden reports an item the caller can read but not modify.
	ErrForbidden = errors.New("vault: item is read-only for this caller")
	// ErrInvalidItemType reports an item type outside the closed set.
	ErrInvalidItemType = errors.New("vault: unknown item type")
	// ErrInvalidScope reports a vault scope outside the closed set.
	ErrInvalidScope = errors.New("vault: unknown vault scope")
	// ErrPayloadInvalid reports a payload failing type or limit validation.
	ErrPayloadInvalid = errors.New("vault: invalid payload")
	// ErrPayloadTooLarge reports a plaintext envelope beyond D09's budget.
	ErrPayloadTooLarge = errors.New("vault: payload exceeds the allowed size")
	// ErrReferenceForbidden reports an address reference the policy rejects:
	// unknown target, unreadable target, wrong target type or visibility
	// mismatch. One code for all causes, so probing reveals nothing.
	ErrReferenceForbidden = errors.New("vault: address reference not allowed")
	// ErrRevisionConflict reports an optimistic-locking mismatch.
	ErrRevisionConflict = errors.New("vault: revision conflict")
	// ErrExportTooLarge reports an export whose item count would exceed the
	// archive entry limit. Export callers must fail before producing a partial
	// archive rather than silently truncating the result.
	ErrExportTooLarge = errors.New("vault: export exceeds the allowed size")
	// ErrImportCommitUnknown marks a transaction whose Commit returned an
	// error after SQLite may have durably applied it. Callers must not retry
	// the corresponding one-shot operation automatically.
	ErrImportCommitUnknown = errors.New("vault: import commit outcome unknown")
)

// RevisionConflictError carries the current revision so the HTTP layer can
// render the REVISION_CONFLICT envelope's current_revision field.
type RevisionConflictError struct{ CurrentRevision uint64 }

func (e *RevisionConflictError) Error() string {
	return "vault: item was updated concurrently"
}

// Meta is the plaintext metadata view of an item plus the decrypted title.
// The database columns only ever hold policy metadata and ciphertext; the
// title is decrypted per page at request time and never stored in the clear.
type Meta struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	ItemType  string  `json:"item_type"`
	Scope     string  `json:"vault_scope"`
	OwnerID   *string `json:"owner_id,omitempty"`
	CreatorID *string `json:"creator_id,omitempty"`
	// CreatorName is the shared creator's display username, resolved for
	// shared items so the workspace can sign them (design §12). Personal
	// items never carry a name: only their owner can read them.
	CreatorName string  `json:"creator_name,omitempty"`
	Favorite    bool    `json:"favorite"`
	Revision    uint64  `json:"revision"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	DeletedAt   *string `json:"deleted_at"`
}

// Detail adds the decrypted payload and the tag list. Tags live inside the
// encrypted envelope (design §6.1) and are therefore only available after
// decryption.
type Detail struct {
	Meta
	Tags    []string `json:"tags"`
	Payload any      `json:"payload"`
}

// storedPayload is the plaintext JSON sealed inside the ciphertext. Exactly
// one type branch is set, matching the item's plaintext item_type column.
// Marshaling is deterministic (fixed field order), which lets the service
// detect no-change updates by comparing envelope bytes.
type storedPayload struct {
	Version    int                `json:"v"`
	Tags       []string           `json:"tags,omitempty"`
	Login      *LoginPayload      `json:"login,omitempty"`
	SSHKey     *SSHKeyPayload     `json:"ssh_key,omitempty"`
	CreditCard *CreditCardPayload `json:"credit_card,omitempty"`
	Identity   *IdentityPayload   `json:"identity,omitempty"`
	SecureNote *SecureNotePayload `json:"secure_note,omitempty"`
	Secret     *SecretPayload     `json:"secret,omitempty"`
}

// TitleOf extracts the display title from a decrypted payload. Every type
// carries a Name field (design §6.2).
func TitleOf(payload any) string {
	switch p := payload.(type) {
	case *LoginPayload:
		return p.Name
	case *SSHKeyPayload:
		return p.Name
	case *CreditCardPayload:
		return p.Name
	case *IdentityPayload:
		return p.Name
	case *SecureNotePayload:
		return p.Name
	case *SecretPayload:
		return p.Name
	default:
		return ""
	}
}

// policyItem renders the ownership view the authorization policy decides on.
func policyItem(scope, ownerID, creatorID string) Item {
	return Item{Scope: Scope(scope), OwnerID: ownerID, CreatorID: creatorID}
}
