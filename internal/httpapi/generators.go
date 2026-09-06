package httpapi

import (
	"net/http"

	"github.com/tiny-password/tiny-password/internal/auth"
	"github.com/tiny-password/tiny-password/internal/generator"
)

// GeneratorsDeps wires the credential generator endpoints (T18). Generated
// values are returned once and are never persisted, logged, or audited —
// the access log carries route templates only.
type GeneratorsDeps struct {
	Session *auth.Service
}

func registerGenerators(api *http.ServeMux, deps GeneratorsDeps) {
	guarded := func(handler http.HandlerFunc) http.Handler {
		return RequireSession(deps.Session, false, handler)
	}

	api.Handle("POST /api/v1/generators/password", guarded(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Length           int  `json:"length"`
			Lowercase        bool `json:"lowercase"`
			Uppercase        bool `json:"uppercase"`
			Digits           bool `json:"digits"`
			Symbols          bool `json:"symbols"`
			ExcludeAmbiguous bool `json:"exclude_ambiguous"`
		}
		if !decodeJSONBody(w, r, &input, authMaxBodyBytes) {
			return
		}
		opts := generator.DefaultPasswordOptions()
		// Zero values mean "use the defaults"; explicit false for every class
		// would be rejected by the generator itself.
		if input.Length > 0 {
			opts.Length = input.Length
		}
		if input.Lowercase || input.Uppercase || input.Digits || input.Symbols || input.ExcludeAmbiguous {
			opts.Lowercase = input.Lowercase
			opts.Uppercase = input.Uppercase
			opts.Digits = input.Digits
			opts.Symbols = input.Symbols
			opts.ExcludeAmbiguous = input.ExcludeAmbiguous
		}
		value, err := generator.Password(opts)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"value": value})
	}))

	api.Handle("POST /api/v1/generators/passphrase", guarded(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Words      *int    `json:"words"`
			Separator  *string `json:"separator"`
			Capitalize *bool   `json:"capitalize"`
		}
		if !decodeJSONBody(w, r, &input, authMaxBodyBytes) {
			return
		}
		opts := generator.DefaultPassphraseOptions()
		if input.Words != nil {
			opts.Words = *input.Words
		}
		if input.Separator != nil {
			opts.Separator = *input.Separator
		}
		if input.Capitalize != nil {
			opts.Capitalize = *input.Capitalize
		}
		value, err := generator.Passphrase(opts)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"value": value})
	}))

	api.Handle("POST /api/v1/generators/ssh-key", guarded(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Algorithm  string `json:"algorithm"`
			Passphrase string `json:"passphrase"`
			Comment    string `json:"comment"`
		}
		if !decodeJSONBody(w, r, &input, authMaxBodyBytes) {
			return
		}
		if input.Algorithm == "" {
			input.Algorithm = generator.SSHAlgoEd25519
		}
		if len([]rune(input.Passphrase)) > 1024 {
			writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "passphrase must be at most 1024 characters")
			return
		}
		if len([]rune(input.Comment)) > 256 {
			writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "comment must be at most 256 characters")
			return
		}
		pair, err := generator.NewSSHKeyPair(input.Algorithm, input.Passphrase, input.Comment)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"algorithm":   pair.Algorithm,
			"public_key":  pair.PublicKey,
			"private_key": pair.PrivateKey,
			"fingerprint": pair.Fingerprint,
		})
	}))
}
