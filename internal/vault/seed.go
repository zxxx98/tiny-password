package vault

import (
	"context"
	"database/sql"
	"time"

	"github.com/tiny-password/tiny-password/internal/platform/crypto"
	"github.com/tiny-password/tiny-password/internal/platform/ident"
)

// SyntheticItem is one pre-validated item for benchmark and load-test seeds.
type SyntheticItem struct {
	ItemType  string
	Scope     string // "personal" or "shared"
	OwnerID   string // personal: the owning member
	CreatorID string // shared: the creating member
	Tags      []string
	Favorite  bool
	Payload   any // one of the six typed payload structs
}

// InsertSynthetic bypasses policy and audit: it exists exclusively for
// performance seeds and load tests (plan T13) that must generate thousands
// of records without HTTP round-trips. It is not reachable from the API and
// must never be called on a request path.
func InsertSynthetic(ctx context.Context, q Queryer, key *crypto.MasterKey, in SyntheticItem) (string, error) {
	payloadType := ""
	switch in.Payload.(type) {
	case *LoginPayload:
		payloadType = TypeLogin
	case *SSHKeyPayload:
		payloadType = TypeSSHKey
	case *CreditCardPayload:
		payloadType = TypeCreditCard
	case *IdentityPayload:
		payloadType = TypeIdentity
	case *SecureNotePayload:
		payloadType = TypeSecureNote
	case *SecretPayload:
		payloadType = TypeSecret
	default:
		return "", ErrInvalidItemType
	}
	tags, err := validateTags(in.Tags)
	if err != nil {
		return "", err
	}
	_, plaintext, err := buildEnvelope(payloadType, tags, in.Payload)
	if err != nil {
		return "", err
	}
	id := ident.NewUUIDv7()
	enc, err := key.Encrypt(plaintext, AADFor(id, in.Scope, in.OwnerID, in.CreatorID, crypto.PayloadVersion, 1))
	if err != nil {
		return "", err
	}
	at := time.Now().UTC().Format(TimestampFormat)
	row := itemRow{
		ID: id, Scope: in.Scope,
		OwnerID:   sql.NullString{String: in.OwnerID, Valid: in.OwnerID != ""},
		CreatorID: sql.NullString{String: in.CreatorID, Valid: in.CreatorID != ""},
		ItemType:  payloadType, Favorite: in.Favorite,
		PayloadVersion: enc.Version, Nonce: enc.Nonce[:], Ciphertext: enc.Ciphertext,
		Revision: 1, CreatedAt: at, UpdatedAt: at,
	}
	return id, (repository{}).insertItem(ctx, q, row)
}
