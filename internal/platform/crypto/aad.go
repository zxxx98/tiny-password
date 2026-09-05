package crypto

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// AAD binds an encrypted payload to its row identity: item ID, vault scope,
// owner/creator, payload format version, and revision. Any change to any
// field makes decryption fail, preventing payload relocation between items
// or history versions.
type AAD struct {
	ItemID         string
	Scope          string // "personal" or "shared"
	SubjectID      string // owner_user_id for personal, created_by_user_id for shared
	PayloadVersion uint16
	Revision       uint64
}

// aadDomain separates payload AAD from every other AEAD use of the master key.
var aadDomain = []byte("tiny-password/vault-payload/aad/v1")

// encode produces an unambiguous byte string:
//
//	domain || u32(len(itemID)) itemID || u32(len(scope)) scope ||
//	u32(len(subjectID)) subjectID || u16(payloadVersion) || u64(revision)
//
// Every variable-length field is length-prefixed so concatenation can never
// alias another field combination.
func (a AAD) encode() []byte {
	var b strings.Builder
	b.Grow(len(aadDomain) + len(a.ItemID) + len(a.Scope) + len(a.SubjectID) + 14)
	b.Write(aadDomain)

	lenPrefix := make([]byte, 4)
	for _, field := range []string{a.ItemID, a.Scope, a.SubjectID} {
		binary.LittleEndian.PutUint32(lenPrefix, uint32(len(field)))
		b.Write(lenPrefix)
		b.WriteString(field)
	}

	ver := make([]byte, 2)
	binary.LittleEndian.PutUint16(ver, a.PayloadVersion)
	b.Write(ver)

	rev := make([]byte, 8)
	binary.LittleEndian.PutUint64(rev, a.Revision)
	b.Write(rev)

	return []byte(b.String())
}

// ValidateAAD enforces the field invariants before an AAD is used.
func ValidateAAD(a AAD) error {
	if a.ItemID == "" {
		return fmt.Errorf("aad: item id required")
	}
	switch a.Scope {
	case "personal":
		if a.SubjectID == "" {
			return fmt.Errorf("aad: personal payload requires owner id")
		}
	case "shared":
		if a.SubjectID == "" {
			return fmt.Errorf("aad: shared payload requires creator id")
		}
	default:
		return fmt.Errorf("aad: unknown vault scope %q", a.Scope)
	}
	if a.PayloadVersion == 0 {
		return fmt.Errorf("aad: payload version required")
	}
	if a.Revision == 0 {
		return fmt.Errorf("aad: revision must be >= 1")
	}
	return nil
}
