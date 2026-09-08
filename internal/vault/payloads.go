package vault

import (
	"fmt"
	"strings"
)

// Field limits mirror the OpenAPI payload schemas. String limits count
// Unicode code points; the login password is additionally capped at 1024
// UTF-8 bytes (decision D09).
const (
	MaxNameRunes        = 256
	MaxShortTextRunes   = 256 // username, cardholder, full name, company, comment
	MaxNotesRunes       = 10000
	MaxPasswordBytes    = 1024
	MaxURLs             = 16
	MaxURLRunes         = 2048
	MaxPublicKeyRunes   = 8192
	MaxPrivateKeyRunes  = 16384
	MaxPassphraseRunes  = 1024
	MaxFingerprintRunes = 128
	MaxCardNumberRunes  = 64
	MaxCVVRunes         = 8
	MaxPINRunes         = 16
	MaxEmailRunes       = 320
	MaxPhoneRunes       = 64
	MaxRegionRunes      = 96 // country, state, city, district
	MaxAddressLineRunes = 512
	MaxPostalCodeRunes  = 32
	MaxNoteBodyRunes    = 65536
	MinExpMonth         = 1
	MaxExpMonth         = 12
	MinExpYear          = 2000
	MaxExpYear          = 9999
	MaxTagsCount        = 32
	MaxTagRunes         = 64
)

// LoginPayload is the login credential type (design §6.2).
type LoginPayload struct {
	Name              string   `json:"name"`
	Username          string   `json:"username,omitempty"`
	Password          string   `json:"password,omitempty"`
	URLs              []string `json:"urls,omitempty"`
	Notes             string   `json:"notes,omitempty"`
	PasswordUpdatedAt *string  `json:"password_updated_at,omitempty"`
	PasswordExpiresAt *string  `json:"password_expires_at,omitempty"`
}

// SSHKeyPayload is the SSH key type (design §6.2).
type SSHKeyPayload struct {
	Name          string `json:"name"`
	Algorithm     string `json:"algorithm"`
	PublicKey     string `json:"public_key,omitempty"`
	PrivateKey    string `json:"private_key,omitempty"`
	KeyPassphrase string `json:"key_passphrase,omitempty"`
	Comment       string `json:"comment,omitempty"`
	Fingerprint   string `json:"fingerprint,omitempty"`
	Notes         string `json:"notes,omitempty"`
}

// CreditCardPayload is the payment card type. BillingAddressItemID is the
// only address reference in V1; the service validates it against the policy
// before anything is stored.
type CreditCardPayload struct {
	Name                 string  `json:"name"`
	Cardholder           string  `json:"cardholder"`
	Number               string  `json:"number"`
	ExpMonth             int     `json:"exp_month"`
	ExpYear              int     `json:"exp_year"`
	CVV                  string  `json:"cvv,omitempty"`
	PIN                  string  `json:"pin,omitempty"`
	BillingAddressItemID *string `json:"billing_address_item_id,omitempty"`
	Notes                string  `json:"notes,omitempty"`
}

// IdentityPayload is the identity/address type (design §6.2).
type IdentityPayload struct {
	Name        string `json:"name"`
	FullName    string `json:"full_name,omitempty"`
	Company     string `json:"company,omitempty"`
	Phone       string `json:"phone,omitempty"`
	Email       string `json:"email,omitempty"`
	Country     string `json:"country,omitempty"`
	State       string `json:"state,omitempty"`
	City        string `json:"city,omitempty"`
	District    string `json:"district,omitempty"`
	AddressLine string `json:"address_line,omitempty"`
	PostalCode  string `json:"postal_code,omitempty"`
	Notes       string `json:"notes,omitempty"`
}

// SecureNotePayload is the free-form note type (design §6.2). V1 does not
// introduce a custom field system.
type SecureNotePayload struct {
	Name string `json:"name"`
	Body string `json:"body"`
}

var sshAlgorithms = map[string]bool{"ed25519": true, "rsa4096": true}

// validate checks one typed payload against its field limits. Error text
// names fields and limits only — never user-provided values.
func (p *LoginPayload) validate() error {
	if err := requiredText("name", p.Name, MaxNameRunes); err != nil {
		return err
	}
	if err := optionalText("username", p.Username, MaxShortTextRunes); err != nil {
		return err
	}
	if len(p.Password) > MaxPasswordBytes {
		return fmt.Errorf("payload field %q must be at most %d bytes", "password", MaxPasswordBytes)
	}
	if len(p.URLs) > MaxURLs {
		return fmt.Errorf("payload field %q must hold at most %d entries", "urls", MaxURLs)
	}
	for i, u := range p.URLs {
		if err := optionalText(fmt.Sprintf("urls[%d]", i), u, MaxURLRunes); err != nil {
			return err
		}
	}
	if err := optionalText("notes", p.Notes, MaxNotesRunes); err != nil {
		return err
	}
	for _, field := range []struct {
		name  string
		value *string
	}{
		{"password_updated_at", p.PasswordUpdatedAt},
		{"password_expires_at", p.PasswordExpiresAt},
	} {
		if err := optionalDate(field.name, field.value); err != nil {
			return err
		}
	}
	return nil
}

func (p *SSHKeyPayload) validate() error {
	if err := requiredText("name", p.Name, MaxNameRunes); err != nil {
		return err
	}
	if !sshAlgorithms[p.Algorithm] {
		return fmt.Errorf("payload field %q must be one of ed25519, rsa4096", "algorithm")
	}
	if err := requiredText("public_key", p.PublicKey, MaxPublicKeyRunes); err != nil {
		return err
	}
	if err := requiredText("private_key", p.PrivateKey, MaxPrivateKeyRunes); err != nil {
		return err
	}
	if err := optionalText("key_passphrase", p.KeyPassphrase, MaxPassphraseRunes); err != nil {
		return err
	}
	if err := optionalText("comment", p.Comment, MaxShortTextRunes); err != nil {
		return err
	}
	if err := optionalText("fingerprint", p.Fingerprint, MaxFingerprintRunes); err != nil {
		return err
	}
	return optionalText("notes", p.Notes, MaxNotesRunes)
}

func (p *CreditCardPayload) validate() error {
	if err := requiredText("name", p.Name, MaxNameRunes); err != nil {
		return err
	}
	if err := requiredText("cardholder", p.Cardholder, MaxShortTextRunes); err != nil {
		return err
	}
	if err := requiredText("number", p.Number, MaxCardNumberRunes); err != nil {
		return err
	}
	if p.ExpMonth < MinExpMonth || p.ExpMonth > MaxExpMonth {
		return fmt.Errorf("payload field %q must be between %d and %d", "exp_month", MinExpMonth, MaxExpMonth)
	}
	if p.ExpYear < MinExpYear || p.ExpYear > MaxExpYear {
		return fmt.Errorf("payload field %q must be between %d and %d", "exp_year", MinExpYear, MaxExpYear)
	}
	if err := optionalText("cvv", p.CVV, MaxCVVRunes); err != nil {
		return err
	}
	if err := optionalText("pin", p.PIN, MaxPINRunes); err != nil {
		return err
	}
	return optionalText("notes", p.Notes, MaxNotesRunes)
}

func (p *IdentityPayload) validate() error {
	if err := requiredText("name", p.Name, MaxNameRunes); err != nil {
		return err
	}
	if err := optionalText("full_name", p.FullName, MaxShortTextRunes); err != nil {
		return err
	}
	if err := optionalText("company", p.Company, MaxShortTextRunes); err != nil {
		return err
	}
	if err := optionalText("phone", p.Phone, MaxPhoneRunes); err != nil {
		return err
	}
	if err := optionalText("email", p.Email, MaxEmailRunes); err != nil {
		return err
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"country", p.Country}, {"state", p.State}, {"city", p.City}, {"district", p.District},
	} {
		if err := optionalText(field.name, field.value, MaxRegionRunes); err != nil {
			return err
		}
	}
	if err := optionalText("address_line", p.AddressLine, MaxAddressLineRunes); err != nil {
		return err
	}
	if err := optionalText("postal_code", p.PostalCode, MaxPostalCodeRunes); err != nil {
		return err
	}
	return optionalText("notes", p.Notes, MaxNotesRunes)
}

func (p *SecureNotePayload) validate() error {
	if err := requiredText("name", p.Name, MaxNameRunes); err != nil {
		return err
	}
	return optionalText("body", p.Body, MaxNoteBodyRunes)
}

// requiredText enforces presence (JSON required + minLength 1) and the
// code-point limit.
func requiredText(field, value string, maxRunes int) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("payload field %q is required", field)
	}
	return optionalText(field, value, maxRunes)
}

// optionalText enforces the code-point limit when the field is present.
func optionalText(field, value string, maxRunes int) error {
	if len([]rune(value)) > maxRunes {
		return fmt.Errorf("payload field %q must be at most %d characters", field, maxRunes)
	}
	return nil
}

// optionalDate enforces the YYYY-MM-DD format for present, non-empty dates.
func optionalDate(field string, value *string) error {
	if value == nil || *value == "" {
		return nil
	}
	if len(*value) != 10 || !isDigitRunes((*value)[:4]) || (*value)[4] != '-' || !isDigitRunes((*value)[5:7]) || (*value)[7] != '-' || !isDigitRunes((*value)[8:]) {
		return fmt.Errorf("payload field %q must be a YYYY-MM-DD date", field)
	}
	month := (*value)[5:7]
	if month < "01" || month > "12" {
		return fmt.Errorf("payload field %q must be a YYYY-MM-DD date", field)
	}
	day := (*value)[8:]
	if day < "01" || day > "31" {
		return fmt.Errorf("payload field %q must be a YYYY-MM-DD date", field)
	}
	return nil
}

func isDigitRunes(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
