package transfer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/tiny-password/tiny-password/internal/vault"
)

const maxBitwardenJSONBytes = 64 << 20

// ErrInvalidBitwarden is returned for any malformed or unsupported Bitwarden
// export. Callers must not expose the parser's internal details to users.
var ErrInvalidBitwarden = errors.New("transfer: invalid Bitwarden export")

type bitwardenExport struct {
	Encrypted *bool             `json:"encrypted"`
	Folders   []bitwardenFolder `json:"folders"`
	Items     []bitwardenItem   `json:"items"`
}

type bitwardenFolder struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type bitwardenItem struct {
	ID          string                `json:"id"`
	FolderID    string                `json:"folderId"`
	Type        int                   `json:"type"`
	Name        string                `json:"name"`
	Notes       *string               `json:"notes"`
	Favorite    bool                  `json:"favorite"`
	Fields      []bitwardenField      `json:"fields"`
	Login       *bitwardenLogin       `json:"login"`
	Card        *bitwardenCard        `json:"card"`
	Identity    *bitwardenIdentity    `json:"identity"`
	Attachments []bitwardenAttachment `json:"attachments"`
}

type bitwardenField struct {
	Name  string  `json:"name"`
	Value *string `json:"value"`
	Type  int     `json:"type"`
}

type bitwardenLogin struct {
	Username         string            `json:"username"`
	Password         string            `json:"password"`
	TOTP             string            `json:"totp"`
	URIs             []bitwardenURI    `json:"uris"`
	FIDO2Credentials []json.RawMessage `json:"fido2Credentials"`
}

type bitwardenURI struct {
	URI string `json:"uri"`
}

type bitwardenCard struct {
	CardholderName string          `json:"cardholderName"`
	Number         string          `json:"number"`
	ExpMonth       json.RawMessage `json:"expMonth"`
	ExpYear        json.RawMessage `json:"expYear"`
	Code           string          `json:"code"`
	Brand          string          `json:"brand"`
}

type bitwardenIdentity struct {
	Title         string `json:"title"`
	FirstName     string `json:"firstName"`
	MiddleName    string `json:"middleName"`
	LastName      string `json:"lastName"`
	Company       string `json:"company"`
	Email         string `json:"email"`
	Phone         string `json:"phone"`
	Address1      string `json:"address1"`
	Address2      string `json:"address2"`
	Address3      string `json:"address3"`
	City          string `json:"city"`
	State         string `json:"state"`
	PostalCode    string `json:"postalCode"`
	Country       string `json:"country"`
	SSN           string `json:"ssn"`
	Passport      string `json:"passportNumber"`
	LicenseNumber string `json:"licenseNumber"`
	Username      string `json:"username"`
}

type bitwardenAttachment struct {
	FileName string `json:"fileName"`
	ID       string `json:"id"`
	Size     int64  `json:"size"`
}

// bitwardenExtras is deliberately a fixed-shape record. JSON marshaling then
// gives the note block deterministic labels without interpolating source data
// into a format string.
type bitwardenExtras struct {
	TOTP        string                   `json:"TOTP,omitempty"`
	FIDO2       []json.RawMessage        `json:"fido2_credentials,omitempty"`
	Fields      []bitwardenExtraField    `json:"custom_fields,omitempty"`
	Attachments []bitwardenAttachment    `json:"attachments,omitempty"`
	Identity    *bitwardenIdentityExtras `json:"identity,omitempty"`
	CardBrand   string                   `json:"card_brand,omitempty"`
}

type bitwardenExtraField struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Type  int    `json:"type"`
}

type bitwardenIdentityExtras struct {
	SSN           string `json:"ssn,omitempty"`
	Passport      string `json:"passport_number,omitempty"`
	LicenseNumber string `json:"license_number,omitempty"`
	Username      string `json:"username,omitempty"`
}

func (e bitwardenExtras) empty() bool {
	return e.TOTP == "" && len(e.FIDO2) == 0 && len(e.Fields) == 0 &&
		len(e.Attachments) == 0 && e.Identity == nil && e.CardBrand == ""
}

// ParseBitwardenJSON converts an unencrypted Bitwarden JSON export into
// normalized vault import items. External IDs are intentionally discarded:
// imported items receive fresh local UUIDv7 IDs during confirmation.
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

	folders := make(map[string]string, len(export.Folders))
	for _, folder := range export.Folders {
		if folder.ID == "" || folder.Name == "" {
			return nil, ErrInvalidBitwarden
		}
		if _, exists := folders[folder.ID]; exists {
			return nil, ErrInvalidBitwarden
		}
		folders[folder.ID] = folder.Name
	}

	seen := make(map[string]struct{}, len(export.Items))
	items := make([]vault.ImportItem, 0, len(export.Items))
	for i, source := range export.Items {
		if source.ID == "" {
			return nil, fmt.Errorf("%w: item %d has no id", ErrInvalidBitwarden, i)
		}
		if _, exists := seen[source.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate item id at position %d", ErrInvalidBitwarden, i)
		}
		seen[source.ID] = struct{}{}
		item, ok := convertBitwardenItem(source, folders)
		if !ok {
			return nil, fmt.Errorf("%w: item %d cannot be converted", ErrInvalidBitwarden, i)
		}
		items = append(items, item)
	}
	return items, nil
}

func convertBitwardenItem(source bitwardenItem, folders map[string]string) (vault.ImportItem, bool) {
	tags := []string{}
	if source.FolderID != "" {
		name, ok := folders[source.FolderID]
		if !ok {
			return vault.ImportItem{}, false
		}
		tags = append(tags, name)
	}
	if _, err := vault.ValidateTags(tags); err != nil {
		return vault.ImportItem{}, false
	}

	var payload any
	switch source.Type {
	case 1:
		payload = convertBitwardenLogin(source)
	case 2:
		payload = convertBitwardenSecureNote(source)
	case 3:
		var ok bool
		payload, ok = convertBitwardenCard(source)
		if !ok {
			return vault.ImportItem{}, false
		}
	case 4:
		payload = convertBitwardenIdentity(source)
	default:
		return vault.ImportItem{}, false
	}

	raw, err := json.Marshal(payload)
	if err != nil || !validateBitwardenPayload(payloadTypeOf(source.Type), raw) {
		return vault.ImportItem{}, false
	}
	return vault.ImportItem{
		ItemType: payloadTypeOf(source.Type), Scope: string(vault.ScopePersonal),
		Tags: tags, Favorite: source.Favorite, Payload: raw,
	}, true
}

func payloadTypeOf(sourceType int) string {
	switch sourceType {
	case 1:
		return vault.TypeLogin
	case 2:
		return vault.TypeSecureNote
	case 3:
		return vault.TypeCreditCard
	case 4:
		return vault.TypeIdentity
	default:
		return ""
	}
}

func validateBitwardenPayload(itemType string, raw []byte) bool {
	return itemType != "" && vault.ValidatePayload(itemType, raw) == nil
}

func convertBitwardenLogin(source bitwardenItem) vault.LoginPayload {
	payload := vault.LoginPayload{Name: source.Name, Notes: stringValue(source.Notes)}
	extras := bitwardenExtras{Fields: extraFields(source.Fields), Attachments: source.Attachments}
	if source.Login != nil {
		payload.Username = source.Login.Username
		payload.Password = source.Login.Password
		for _, uri := range source.Login.URIs {
			if uri.URI != "" {
				payload.URLs = append(payload.URLs, uri.URI)
			}
		}
		extras.TOTP = source.Login.TOTP
		extras.FIDO2 = source.Login.FIDO2Credentials
	}
	payload.Notes = appendBitwardenExtras(payload.Notes, extras)
	return payload
}

func convertBitwardenSecureNote(source bitwardenItem) vault.SecureNotePayload {
	notes := appendBitwardenExtras(stringValue(source.Notes), bitwardenExtras{
		Fields: extraFields(source.Fields), Attachments: source.Attachments,
	})
	return vault.SecureNotePayload{Name: source.Name, Body: notes}
}

func convertBitwardenCard(source bitwardenItem) (vault.CreditCardPayload, bool) {
	if source.Card == nil {
		return vault.CreditCardPayload{}, false
	}
	month, monthOK := parseBitwardenInt(source.Card.ExpMonth)
	year, yearOK := parseBitwardenInt(source.Card.ExpYear)
	if !monthOK || !yearOK {
		return vault.CreditCardPayload{}, false
	}
	card := source.Card
	notes := appendBitwardenExtras(stringValue(source.Notes), bitwardenExtras{
		Fields: extraFields(source.Fields), Attachments: source.Attachments,
		CardBrand: card.Brand,
	})
	return vault.CreditCardPayload{
		Name: source.Name, Cardholder: card.CardholderName, Number: card.Number,
		ExpMonth: month, ExpYear: year, CVV: card.Code, Notes: notes,
	}, true
}

func convertBitwardenIdentity(source bitwardenItem) vault.IdentityPayload {
	identity := source.Identity
	if identity == nil {
		identity = &bitwardenIdentity{}
	}
	fullName := strings.Join(nonEmpty(identity.Title, identity.FirstName, identity.MiddleName, identity.LastName), " ")
	address := strings.Join(nonEmpty(identity.Address1, identity.Address2, identity.Address3), "\n")
	var extras *bitwardenIdentityExtras
	if identity.SSN != "" || identity.Passport != "" || identity.LicenseNumber != "" || identity.Username != "" {
		extras = &bitwardenIdentityExtras{
			SSN: identity.SSN, Passport: identity.Passport,
			LicenseNumber: identity.LicenseNumber, Username: identity.Username,
		}
	}
	notes := appendBitwardenExtras(stringValue(source.Notes), bitwardenExtras{
		Fields: extraFields(source.Fields), Attachments: source.Attachments,
		Identity: extras,
	})
	return vault.IdentityPayload{
		Name: source.Name, FullName: fullName, Company: identity.Company,
		Phone: identity.Phone, Email: identity.Email, Country: identity.Country,
		State: identity.State, City: identity.City, AddressLine: address,
		PostalCode: identity.PostalCode, Notes: notes,
	}
}

func extraFields(fields []bitwardenField) []bitwardenExtraField {
	if len(fields) == 0 {
		return nil
	}
	out := make([]bitwardenExtraField, 0, len(fields))
	for _, field := range fields {
		value := ""
		if field.Value != nil {
			value = *field.Value
		}
		out = append(out, bitwardenExtraField{Name: field.Name, Value: value, Type: field.Type})
	}
	return out
}

func appendBitwardenExtras(notes string, extras bitwardenExtras) string {
	if extras.empty() {
		return notes
	}
	raw, err := json.Marshal(extras)
	if err != nil {
		return notes
	}
	block := "--- Bitwarden extra fields ---\n" + string(raw)
	if notes == "" {
		return block
	}
	return notes + "\n\n" + block
}

func parseBitwardenInt(raw json.RawMessage) (int, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	var stringValue string
	if json.Unmarshal(raw, &stringValue) == nil {
		value, err := strconv.Atoi(stringValue)
		return value, err == nil
	}
	var number json.Number
	if json.Unmarshal(raw, &number) != nil {
		return 0, false
	}
	value, err := strconv.Atoi(string(number))
	return value, err == nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func nonEmpty(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}
