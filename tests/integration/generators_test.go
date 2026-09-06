package integration

import (
	"strings"
	"testing"
)

// TestGeneratorEndpoints covers the T18 HTTP contract: authenticated access,
// defaults, option handling, and one-shot values.
func TestGeneratorEndpoints(t *testing.T) {
	h := newItemsHarness(t)
	h.bootstrapAdmin(t)
	alice := h.itemClient(t, "alice")

	post := func(path string, body any, client authClient) (int, map[string]any) {
		t.Helper()
		resp := h.request(t, "POST", path, body, client)
		defer resp.Body.Close()
		return resp.StatusCode, decodeBody(t, resp)
	}

	// Password defaults produce a 20-char value; the value never persists.
	status, body := post("/generators/password", map[string]any{}, alice)
	if status != 200 || len(body["value"].(string)) != 20 {
		t.Fatalf("password default: %d %v", status, body)
	}
	// Length bounds and class handling.
	status, body = post("/generators/password", map[string]any{"length": 8, "lowercase": true, "uppercase": false, "digits": false, "symbols": false}, alice)
	if status != 200 || strings.ContainsAny(body["value"].(string), "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#") {
		t.Fatalf("lowercase-only: %d %v", status, body)
	}
	status, _ = post("/generators/password", map[string]any{"length": 7, "lowercase": true}, alice)
	if status != 400 {
		t.Fatalf("short password: %d", status)
	}
	status, _ = post("/generators/password", map[string]any{"length": 129, "lowercase": true}, alice)
	if status != 400 {
		t.Fatalf("long password: %d", status)
	}
	// Unauthenticated callers are rejected before anything generates.
	status, _ = post("/generators/password", map[string]any{}, authClient{})
	if status != 401 {
		t.Fatalf("unauthenticated generator: %d", status)
	}

	// Passphrase: 5 words joined by '-' by default; options apply.
	status, body = post("/generators/passphrase", map[string]any{}, alice)
	if status != 200 || strings.Count(body["value"].(string), "-") != 4 {
		t.Fatalf("passphrase default: %d %v", status, body)
	}
	status, body = post("/generators/passphrase", map[string]any{"words": 3, "separator": "_", "capitalize": true}, alice)
	if status != 200 || strings.Count(body["value"].(string), "_") != 2 {
		t.Fatalf("passphrase options: %d %v", status, body)
	}
	status, _ = post("/generators/passphrase", map[string]any{"words": 11}, alice)
	if status != 400 {
		t.Fatalf("too many words: %d", status)
	}

	// SSH keys: ed25519 by default with an OpenSSH private key and a
	// SHA256 fingerprint; the key pair is generated but never stored.
	status, body = post("/generators/ssh-key", map[string]any{}, alice)
	if status != 200 {
		t.Fatalf("ssh default: %d %v", status, body)
	}
	if body["algorithm"] != "ed25519" || !strings.HasPrefix(body["fingerprint"].(string), "SHA256:") {
		t.Fatalf("ssh payload: %v", body)
	}
	if !strings.Contains(body["private_key"].(string), "OPENSSH PRIVATE KEY") {
		t.Fatalf("private key format: %v", body)
	}
	status, _ = post("/generators/ssh-key", map[string]any{"algorithm": "rsa2048"}, alice)
	if status != 400 {
		t.Fatalf("bad algorithm: %d", status)
	}

	// Nothing the generator produced may appear in the database.
	var rows int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM vault_items`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("generator leaked %d rows into storage", rows)
	}
}
