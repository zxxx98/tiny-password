package vault

import (
	"encoding/json"
	"testing"
)

func TestValidatePayloadForImportScopesEmptySecureNoteAllowance(t *testing.T) {
	raw := json.RawMessage(`{"name":"empty","body":""}`)
	if err := ValidatePayload(TypeSecureNote, raw); err == nil {
		t.Fatal("ordinary payload validation accepted an empty secure-note body")
	}
	if err := ValidatePayloadForImport(TypeSecureNote, raw, true); err != nil {
		t.Fatalf("import validation rejected empty secure-note body: %v", err)
	}
	if err := ValidatePayloadForImport(TypeSecureNote, raw, false); err == nil {
		t.Fatal("strict import validation accepted an empty secure-note body")
	}
	if err := ValidatePayloadForImport(TypeLogin, json.RawMessage(`{"name":"login"}`), true); err != nil {
		t.Fatalf("login validation unexpectedly failed: %v", err)
	}
}
