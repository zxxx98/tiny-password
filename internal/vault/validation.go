package vault

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// decodePayload parses the client's payload object into the struct the item
// type demands. Unknown fields and non-object bodies are rejected: a typo'd
// field must never silently vanish from an encrypted record.
func decodePayload(itemType string, raw json.RawMessage) (any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("%w: payload is required", ErrPayloadInvalid)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var (
		payload any
		err     error
	)
	switch itemType {
	case TypeLogin:
		p := &LoginPayload{}
		err = dec.Decode(p)
		payload = p
	case TypeSSHKey:
		p := &SSHKeyPayload{}
		err = dec.Decode(p)
		payload = p
	case TypeCreditCard:
		p := &CreditCardPayload{}
		err = dec.Decode(p)
		payload = p
	case TypeIdentity:
		p := &IdentityPayload{}
		err = dec.Decode(p)
		payload = p
	case TypeSecureNote:
		p := &SecureNotePayload{}
		err = dec.Decode(p)
		payload = p
	default:
		return nil, ErrInvalidItemType
	}
	if err != nil {
		return nil, fmt.Errorf("%w: payload does not match item type %q", ErrPayloadInvalid, itemType)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("%w: payload must be a single JSON object", ErrPayloadInvalid)
	}
	switch p := payload.(type) {
	case *LoginPayload:
		err = p.validate()
	case *SSHKeyPayload:
		err = p.validate()
	case *CreditCardPayload:
		err = p.validate()
	case *IdentityPayload:
		err = p.validate()
	case *SecureNotePayload:
		err = p.validate()
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrPayloadInvalid, err.Error())
	}
	return payload, nil
}

// validateTags normalizes the tag list: duplicates collapse to their first
// occurrence, order is preserved, and every tag must be 1..64 code points
// with at most 32 tags in total (design §6.1, OpenAPI limits).
func validateTags(tags []string) ([]string, error) {
	if len(tags) > MaxTagsCount {
		return nil, fmt.Errorf("%w: at most %d tags are allowed", ErrPayloadInvalid, MaxTagsCount)
	}
	seen := make(map[string]bool, len(tags))
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		if n := len([]rune(tag)); n < 1 || n > MaxTagRunes {
			return nil, fmt.Errorf("%w: tags must be 1..%d characters", ErrPayloadInvalid, MaxTagRunes)
		}
		if seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	return out, nil
}

// ValidateTags exposes the same normalization and limits to trusted format
// converters before their items enter the transactional import path.
func ValidateTags(tags []string) ([]string, error) {
	return validateTags(tags)
}

// buildEnvelope assembles the encrypted envelope for one item and returns
// its canonical marshaled form. The whole plaintext is bounded by D09's
// 256 KiB budget: a larger envelope is rejected before any encryption.
func buildEnvelope(itemType string, tags []string, payload any) (*storedPayload, []byte, error) {
	env := &storedPayload{Version: PayloadSchemaVersion, Tags: tags}
	switch p := payload.(type) {
	case *LoginPayload:
		env.Login = p
	case *SSHKeyPayload:
		env.SSHKey = p
	case *CreditCardPayload:
		env.CreditCard = p
	case *IdentityPayload:
		env.Identity = p
	case *SecureNotePayload:
		env.SecureNote = p
	default:
		return nil, nil, ErrInvalidItemType
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return nil, nil, err
	}
	if len(raw) > MaxPayloadBytes {
		return nil, nil, ErrPayloadTooLarge
	}
	return env, raw, nil
}

// referenceTarget extracts the address reference carried by a payload. Only
// the credit card's billing address is an item reference in V1; login URLs
// are plain strings. A present-but-empty value counts as a reference attempt
// so it fails closed in reference validation instead of being dropped.
func referenceTarget(payload any) (string, bool) {
	if cc, ok := payload.(*CreditCardPayload); ok && cc.BillingAddressItemID != nil {
		return *cc.BillingAddressItemID, true
	}
	return "", false
}
