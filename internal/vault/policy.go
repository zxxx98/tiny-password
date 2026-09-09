// Package vault owns the vault authorization policy (design §5.2). The
// policy is the single authority for what a principal may do with an item;
// database queries narrow the candidate set first, services re-check here,
// and only then is anything decrypted.
package vault

import "errors"

// Role of the acting principal.
type Role string

const (
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
)

// Scope of an item's vault.
type Scope string

const (
	ScopePersonal Scope = "personal"
	ScopeShared   Scope = "shared"
)

// Action enumerates the operations the policy decides. M2 fixes the set;
// M3 handlers must map each endpoint to exactly one of these.
type Action string

const (
	ActionRead           Action = "read"
	ActionUpdate         Action = "update"
	ActionDelete         Action = "delete"  // move to trash
	ActionRestore        Action = "restore" // from trash
	ActionPurge          Action = "purge"   // permanent delete
	ActionHistory        Action = "history" // read encrypted history versions
	ActionHistoryRestore Action = "history_restore"
	ActionFavorite       Action = "favorite"
	ActionTag            Action = "tag"
	ActionExport         Action = "export"
)

// Item is the ownership view of a vault item the policy decides on. Exactly
// one of OwnerID (personal) or CreatorID (shared) is set, mirroring the
// database CHECK constraint.
type Item struct {
	Scope     Scope
	OwnerID   string // personal: the owning member
	CreatorID string // shared: the creating member
}

// ErrOwnershipImmutable rejects an update that attempts to change scope or
// owner/creator outside the dedicated, server-controlled move path.
var ErrOwnershipImmutable = errors.New("vault: scope and owner/creator cannot be changed")

// IsSelf reports whether the actor holds the write identity of the item:
// the owner of a personal item or the creator of a shared item.
func IsSelf(actorID string, it Item) bool {
	switch it.Scope {
	case ScopePersonal:
		return it.OwnerID != "" && it.OwnerID == actorID
	case ScopeShared:
		return it.CreatorID != "" && it.CreatorID == actorID
	default:
		return false
	}
}

// CanReadItem is the read rule shared by item reads, search candidate sets,
// history reads and reference-target checks: every member may read shared
// items; personal items are readable by their owner only. The administrator
// deliberately has no personal-vault privilege (design §5.2).
func CanReadItem(actorID string, it Item) bool {
	if it.Scope == ScopeShared {
		return true
	}
	return it.OwnerID != "" && it.OwnerID == actorID
}

// Can decides whether the actor may perform action on an existing item.
//
// Rules (design §5.2, decisions D06 and D10):
//   - Personal items: the owner may do everything; everyone else — including
//     administrators — is denied.
//   - Shared items: reading and reading history extend to every member;
//     every mutation (update, trash, restore, purge, favorite, tag, history
//     restore) and the export belongs to the creator alone (D06: favorites
//     and tags are part of the item; D10: exports carry own-created data).
func Can(role Role, actorID string, it Item, action Action) bool {
	_ = role // admin and member hold identical vault-item rights; role is
	// accepted so call sites stay explicit about the acting principal.
	if actorID == "" {
		return false
	}
	switch action {
	case ActionRead, ActionHistory:
		return CanReadItem(actorID, it)
	case ActionUpdate, ActionDelete, ActionRestore, ActionPurge,
		ActionHistoryRestore, ActionFavorite, ActionTag, ActionExport:
		return IsSelf(actorID, it)
	default:
		return false
	}
}

// CanCreate validates an item creation claim. The server must set the
// ownership fields itself; a request claiming another member's ownership is
// denied rather than silently rewritten.
func CanCreate(role Role, actorID string, scope Scope, ownerID, creatorID string) bool {
	_ = role
	if actorID == "" {
		return false
	}
	switch scope {
	case ScopePersonal:
		return ownerID == actorID && creatorID == ""
	case ScopeShared:
		return creatorID == actorID && ownerID == ""
	default:
		return false
	}
}

// ValidateImmutableOwnership compares the stored ownership view against the
// values an update request carries. Nil pointers mean "field absent";
// present-and-different is rejected so ownership cannot change through an
// unapproved update path.
func ValidateImmutableOwnership(current Item, scope *Scope, ownerID, creatorID *string) error {
	if scope != nil && *scope != current.Scope {
		return ErrOwnershipImmutable
	}
	if ownerID != nil && *ownerID != current.OwnerID {
		return ErrOwnershipImmutable
	}
	if creatorID != nil && *creatorID != current.CreatorID {
		return ErrOwnershipImmutable
	}
	return nil
}

// CanReference validates an address reference from one item to another
// (design §6.2): the target must be readable by the actor and visibility
// compatible — a shared item may only reference shared targets, because
// other readers could not resolve a personal reference.
func CanReference(actorID string, source, target Item) bool {
	if !CanReadItem(actorID, target) {
		return false
	}
	if source.Scope == ScopeShared && target.Scope != ScopeShared {
		return false
	}
	return true
}
