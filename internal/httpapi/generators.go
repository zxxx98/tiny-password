package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
			Length           json.RawMessage `json:"length"`
			Lowercase        json.RawMessage `json:"lowercase"`
			Uppercase        json.RawMessage `json:"uppercase"`
			Digits           json.RawMessage `json:"digits"`
			Symbols          json.RawMessage `json:"symbols"`
			ExcludeAmbiguous json.RawMessage `json:"exclude_ambiguous"`
		}
		if !decodeJSONBody(w, r, &input, authMaxBodyBytes) {
			return
		}
		opts := generator.DefaultPasswordOptions()
		if err := applyPasswordOptions(&opts, input.Length, input.Lowercase, input.Uppercase, input.Digits, input.Symbols, input.ExcludeAmbiguous); err != nil {
			writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
			return
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
		pair, err := generator.NewSSHKeyPairContext(r.Context(), input.Algorithm, input.Passphrase, input.Comment)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				// The client disconnected; there is no response to send and
				// the context-aware generator has already stopped its work.
				return
			}
			if errors.Is(err, context.DeadlineExceeded) {
				writeError(w, r, http.StatusGatewayTimeout, "GENERATOR_TIMEOUT", "key generation timed out")
				return
			}
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

// applyPasswordOptions applies only fields present in the JSON body. Raw
// messages let the handler distinguish omitted values from explicit false or
// zero values, which must be validated instead of silently defaulted.
func applyPasswordOptions(opts *generator.PasswordOptions, length, lowercase, uppercase, digits, symbols, excludeAmbiguous json.RawMessage) error {
	if err := decodePresentPasswordOption(length, &opts.Length); err != nil {
		return fmtPasswordOptionError("length", err)
	}
	if err := decodePresentPasswordOption(lowercase, &opts.Lowercase); err != nil {
		return fmtPasswordOptionError("lowercase", err)
	}
	if err := decodePresentPasswordOption(uppercase, &opts.Uppercase); err != nil {
		return fmtPasswordOptionError("uppercase", err)
	}
	if err := decodePresentPasswordOption(digits, &opts.Digits); err != nil {
		return fmtPasswordOptionError("digits", err)
	}
	if err := decodePresentPasswordOption(symbols, &opts.Symbols); err != nil {
		return fmtPasswordOptionError("symbols", err)
	}
	if err := decodePresentPasswordOption(excludeAmbiguous, &opts.ExcludeAmbiguous); err != nil {
		return fmtPasswordOptionError("exclude_ambiguous", err)
	}
	return nil
}

func decodePresentPasswordOption(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("must not be null")
	}
	return json.Unmarshal(raw, target)
}

func fmtPasswordOptionError(name string, err error) error {
	return fmt.Errorf("%s: %w", name, err)
}
