package vault

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestSecretPayloadValidation(t *testing.T) {
	tests := []struct {
		name    string
		payload SecretPayload
		wantErr bool
	}{
		{
			name: "valid ordered entries and empty value",
			payload: SecretPayload{
				Name:    "prod",
				Entries: []SecretEntry{{Key: "A", Value: "1"}, {Key: "B", Value: ""}},
				Notes:   "n",
			},
		},
		{
			name:    "blank key",
			payload: SecretPayload{Name: "prod", Entries: []SecretEntry{{Key: " \t", Value: "x"}}},
			wantErr: true,
		},
		{
			name: "exact duplicate key",
			payload: SecretPayload{Name: "prod", Entries: []SecretEntry{
				{Key: "A", Value: "1"}, {Key: "A", Value: "2"},
			}},
			wantErr: true,
		},
		{
			name: "case-sensitive keys are distinct",
			payload: SecretPayload{Name: "prod", Entries: []SecretEntry{
				{Key: "A", Value: "1"}, {Key: "a", Value: "2"},
			}},
		},
		{
			name: "leading whitespace participates in uniqueness",
			payload: SecretPayload{Name: "prod", Entries: []SecretEntry{
				{Key: " A", Value: "1"}, {Key: "A", Value: "2"},
			}},
		},
		{
			name:    "zero entries",
			payload: SecretPayload{Name: "prod", Entries: []SecretEntry{}},
			wantErr: true,
		},
		{
			name:    "blank name",
			payload: SecretPayload{Name: " ", Entries: []SecretEntry{{Key: "A"}}},
			wantErr: true,
		},
		{
			name:    "129 entries",
			payload: SecretPayload{Name: "prod", Entries: repeatedSecretEntries(129)},
			wantErr: true,
		},
		{
			name:    "128 entries",
			payload: SecretPayload{Name: "prod", Entries: repeatedSecretEntries(128)},
		},
		{
			name:    "key over 256 code points",
			payload: SecretPayload{Name: "prod", Entries: []SecretEntry{{Key: strings.Repeat("k", 257)}}},
			wantErr: true,
		},
		{
			name:    "value over 16384 code points",
			payload: SecretPayload{Name: "prod", Entries: []SecretEntry{{Key: "A", Value: strings.Repeat("v", 16385)}}},
			wantErr: true,
		},
		{
			name:    "notes over 10000 code points",
			payload: SecretPayload{Name: "prod", Entries: []SecretEntry{{Key: "A", Value: "x"}}, Notes: strings.Repeat("n", 10001)},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal(tt.payload)
			if err != nil {
				t.Fatal(err)
			}
			_, err = decodePayload(TypeSecret, raw)
			if gotErr := errors.Is(err, ErrPayloadInvalid); gotErr != tt.wantErr {
				t.Fatalf("errors.Is(err, ErrPayloadInvalid) = %v, want %v; err = %v", gotErr, tt.wantErr, err)
			}
		})
	}
}

func TestSecretPayloadEnvelopeRoundTripPreservesOrder(t *testing.T) {
	raw := json.RawMessage(`{"name":"prod","entries":[{"key":"FIRST","value":""},{"key":"SECOND","value":"two"}]}`)
	payload, err := decodePayload(TypeSecret, raw)
	if err != nil {
		t.Fatal(err)
	}
	_, envelopeRaw, err := buildEnvelope(TypeSecret, nil, payload)
	if err != nil {
		t.Fatal(err)
	}
	var envelope storedPayload
	if err := json.Unmarshal(envelopeRaw, &envelope); err != nil {
		t.Fatal(err)
	}
	got, ok := typedPayloadOf(TypeSecret, &envelope).(*SecretPayload)
	if !ok {
		t.Fatalf("typed payload = %T, want *SecretPayload", typedPayloadOf(TypeSecret, &envelope))
	}
	if got.Name != "prod" || len(got.Entries) != 2 || got.Entries[0].Key != "FIRST" || got.Entries[1].Key != "SECOND" || got.Entries[0].Value != "" {
		t.Fatalf("entries lost order or empty value: %+v", got)
	}
	if TitleOf(got) != "prod" {
		t.Fatalf("TitleOf(secret) = %q, want prod", TitleOf(got))
	}
}

func repeatedSecretEntries(n int) []SecretEntry {
	entries := make([]SecretEntry, n)
	for i := range entries {
		entries[i] = SecretEntry{Key: "key-" + strconv.Itoa(i)}
	}
	return entries
}
