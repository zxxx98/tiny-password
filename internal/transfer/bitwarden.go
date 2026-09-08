package transfer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/tiny-password/tiny-password/internal/vault"
)

const maxBitwardenJSONBytes = 64 << 20

// ErrInvalidBitwarden is returned for any malformed or unsupported Bitwarden
// export. Callers must not expose the parser's internal details to users.
var ErrInvalidBitwarden = errors.New("transfer: invalid Bitwarden export")

type bitwardenExport struct {
	Encrypted *bool           `json:"encrypted"`
	Items     []bitwardenItem `json:"items"`
}

type bitwardenItem struct {
	ID    string          `json:"id"`
	Type  int             `json:"type"`
	Name  string          `json:"name"`
	Login *bitwardenLogin `json:"login"`
}

type bitwardenLogin struct {
	Username string         `json:"username"`
	Password string         `json:"password"`
	URIs     []bitwardenURI `json:"uris"`
}

type bitwardenURI struct {
	URI string `json:"uri"`
}

// ParseBitwardenJSON converts an unencrypted Bitwarden JSON export into login
// credential items. All non-login Bitwarden records are intentionally skipped;
// external IDs are discarded and imported items receive fresh local UUIDv7 IDs
// during confirmation.
func ParseBitwardenJSON(raw []byte) ([]vault.ImportItem, error) {
	if len(raw) == 0 || len(raw) > maxBitwardenJSONBytes {
		return nil, ErrInvalidBitwarden
	}
	var export bitwardenExport
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&export); err != nil {
		return nil, ErrInvalidBitwarden
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, ErrInvalidBitwarden
	}
	if export.Encrypted == nil || *export.Encrypted || len(export.Items) == 0 {
		return nil, ErrInvalidBitwarden
	}

	seen := make(map[string]struct{}, len(export.Items))
	items := make([]vault.ImportItem, 0, len(export.Items))
	for i, source := range export.Items {
		if source.Type != 1 {
			continue
		}
		if source.ID == "" {
			return nil, fmt.Errorf("%w: item %d has no id", ErrInvalidBitwarden, i)
		}
		if _, exists := seen[source.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate item id at position %d", ErrInvalidBitwarden, i)
		}
		seen[source.ID] = struct{}{}
		item, ok := convertBitwardenItem(source)
		if !ok {
			return nil, fmt.Errorf("%w: item %d cannot be converted", ErrInvalidBitwarden, i)
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		return nil, ErrInvalidBitwarden
	}
	return items, nil
}

func convertBitwardenItem(source bitwardenItem) (vault.ImportItem, bool) {
	if source.Type != 1 || source.Login == nil {
		return vault.ImportItem{}, false
	}

	payload := convertBitwardenLogin(source)
	raw, err := json.Marshal(payload)
	if err != nil || !validateBitwardenPayload(vault.TypeLogin, raw) {
		return vault.ImportItem{}, false
	}
	return vault.ImportItem{
		ItemType: vault.TypeLogin, Scope: string(vault.ScopePersonal), Payload: raw,
	}, true
}

func validateBitwardenPayload(itemType string, raw []byte) bool {
	return vault.ValidatePayload(itemType, raw) == nil
}

func convertBitwardenLogin(source bitwardenItem) vault.LoginPayload {
	payload := vault.LoginPayload{Name: source.Name, Username: source.Login.Username, Password: source.Login.Password}
	for _, uri := range source.Login.URIs {
		if uri.URI != "" {
			payload.URLs = append(payload.URLs, uri.URI)
		}
	}
	return payload
}
