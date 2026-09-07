package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tiny-password/tiny-password/internal/audit"
	"github.com/tiny-password/tiny-password/internal/auth"
	"github.com/tiny-password/tiny-password/internal/bootstrap"
	"github.com/tiny-password/tiny-password/internal/httpapi"
	"github.com/tiny-password/tiny-password/internal/idempotency"
	"github.com/tiny-password/tiny-password/internal/platform/crypto"
	"github.com/tiny-password/tiny-password/internal/platform/ident"
	"github.com/tiny-password/tiny-password/internal/platform/sqlite"
	"github.com/tiny-password/tiny-password/internal/transfer"
	"github.com/tiny-password/tiny-password/internal/users"
	"github.com/tiny-password/tiny-password/internal/vault"
	"github.com/tiny-password/tiny-password/migrations"
)

// itemsHarness wires the vault item endpoints on top of the auth harness.
type itemsHarness struct {
	*authHarness
	vault       *vault.Service
	idem        *idempotency.Service
	usersSvc    *users.Service
	boot        *bootstrap.Service
	setupLogger *capturedHandler
	admin       authClient
	decrypts    *decryptRecorder
}

func newItemsHarness(t *testing.T) *itemsHarness {
	return newItemsHarnessWithSearchProfile(t, nil)
}

func newItemsHarnessWithSearchProfile(t *testing.T, profile vault.SearchProfileHook) *itemsHarness {
	t.Helper()
	h := newAuthHarness(t)
	ih := &itemsHarness{authHarness: h}
	key := mustMasterKey(t)

	idem, err := idempotency.NewService(h.db.DB, idempotency.Options{MACKey: newIdempotencyKeyMaterial()})
	if err != nil {
		t.Fatal(err)
	}
	ih.idem = idem

	authSvc, err := auth.NewService(h.db.DB, auth.Options{
		Now:    h.clock.Now,
		Limits: auth.Limits{Username: 1000, Source: 1000, Global: 1000},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.svc = authSvc

	auditSvc := audit.NewService(audit.Options{})
	decrypts := &decryptRecorder{}
	vsvc, err := vault.NewService(h.db.DB, key, vault.Options{
		Now: h.clock.Now, Audit: auditSvc, DecryptHook: decrypts.record,
		SearchProfile: profile,
	})
	if err != nil {
		t.Fatal(err)
	}
	ih.vault = vsvc
	ih.decrypts = decrypts
	transferSvc, err := transfer.NewService(vsvc, transfer.Options{
		WorkDir: filepath.Join(t.TempDir(), "transfer"),
		HMACKey: bytes.Repeat([]byte{0x77}, 32),
		Audit:   auditSvc,
		DB:      h.db.DB,
		Now:     h.clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	ih.usersSvc = users.NewService(h.db.DB, users.Options{Now: h.clock.Now, Audit: auditSvc})

	cursor, err := httpapi.NewCursorCodec(bytes.Repeat([]byte{0x44}, 32), 0)
	if err != nil {
		t.Fatal(err)
	}

	// A dedicated bootstrap instance whose logger captures the one-time token.
	logger := &capturedHandler{}
	boot, err := bootstrap.NewService(h.db, key, slog.New(logger))
	if err != nil {
		t.Fatal(err)
	}
	ih.boot = boot
	ih.setupLogger = logger

	csrf := httpapi.NewPreAuthCSRF(false)
	srv := httptest.NewServer(httpapi.New(httpapi.Options{
		Setup:      &httpapi.SetupDeps{Service: boot, CSRF: csrf, Logger: slogDiscard()},
		Auth:       &httpapi.AuthDeps{Service: authSvc, CSRF: csrf},
		Users:      &httpapi.UsersDeps{Service: ih.usersSvc, Session: authSvc, Cursor: cursor, Idempotency: idem},
		Items:      &httpapi.ItemsDeps{Service: vsvc, Session: authSvc, Cursor: cursor, Idempotency: idem},
		Generators: &httpapi.GeneratorsDeps{Session: authSvc},
		Transfer:   &httpapi.TransferDeps{Service: transferSvc, Session: authSvc},
	}))
	t.Cleanup(srv.Close)
	h.server = srv
	return ih
}

// bootstrapAdmin runs the real one-time setup so the harness has an admin.
func (h *itemsHarness) bootstrapAdmin(t *testing.T) {
	t.Helper()
	token := h.setupLogger.tokenValue()
	if token == "" {
		t.Fatal("no setup token logged")
	}
	if _, err := h.boot.Initialize(bootstrap.InitializeInput{Token: token, Username: "Admin", Password: validPassword}); err != nil {
		t.Fatal(err)
	}
	admin, resp := h.login(t, "Admin", validPassword)
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("admin login failed: %d body=%s", resp.StatusCode, body)
	}
	resp.Body.Close()
	h.admin = admin
}

// insertMember adds a member whose internal id is a UUID, so audit
// assertions can prove that no display username is ever stored.
func (h *itemsHarness) insertMember(t *testing.T, name string) {
	t.Helper()
	hash, err := auth.HashPassword(validPassword)
	if err != nil {
		t.Fatal(err)
	}
	id := ident.NewUUIDv7()
	at := h.clock.Now().UTC().Format(time.RFC3339Nano)
	_, err = h.db.Exec(`INSERT INTO users(id,username_norm,username_display,role,status,must_change_password,password_hash,created_at,updated_at)
		VALUES(?,?,?,'member','active',0,?,?,?)`, id, name, name, hash, at, at)
	if err != nil {
		t.Fatal(err)
	}
}

// itemClient returns a logged-in client for a directly inserted member.
func (h *itemsHarness) itemClient(t *testing.T, name string) authClient {
	t.Helper()
	h.insertMember(t, name)
	client, resp := h.login(t, name, validPassword)
	expectStatus(t, resp, 200)
	return client
}

// listCount returns the number of items a client sees on one list page.
func (h *itemsHarness) listCount(t *testing.T, client authClient, query string) int {
	t.Helper()
	resp := h.request(t, "GET", query, nil, client)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("%s: %d", query, resp.StatusCode)
	}
	return len(decodeBody(t, resp)["items"].([]any))
}

func itemCreateBody(scope, typ string, payload map[string]any, tags []string, favorite bool) map[string]any {
	body := map[string]any{
		"item_type":   typ,
		"vault_scope": scope,
		"payload":     payload,
	}
	if tags != nil {
		body["tags"] = tags
	}
	if favorite {
		body["favorite"] = true
	}
	return body
}

// createItem posts a create request without an idempotency key.
func (h *itemsHarness) createItem(t *testing.T, client authClient, body map[string]any) *http.Response {
	t.Helper()
	return h.request(t, "POST", "/items", body, client)
}

// createItemIdem posts a create request with an idempotency key.
func (h *itemsHarness) createItemIdem(t *testing.T, client authClient, body map[string]any, key string) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("POST", h.server.URL+"/api/v1/items", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", h.server.URL)
	req.Header.Set("X-CSRF-Token", client.csrf)
	req.Header.Set("Idempotency-Key", key)
	if client.cookie != nil {
		req.AddCookie(client.cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// mustCreateItem creates an item and fails the test on any non-201 response.
func (h *itemsHarness) mustCreateItem(t *testing.T, client authClient, scope, typ string, payload map[string]any, tags []string) map[string]any {
	t.Helper()
	resp := h.createItem(t, client, itemCreateBody(scope, typ, payload, tags, false))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create %s/%s: status=%d", scope, typ, resp.StatusCode)
	}
	return decodeBody(t, resp)
}

// validPayloadFixtures returns one valid fixture per item type covering every
// design §6.2 field. billing_address_item_id holds a placeholder that
// reference-sensitive tests must replace with a real identity item id.
func validPayloadFixtures() map[string]map[string]any {
	return map[string]map[string]any{
		"login": {
			"name":                "Family bank",
			"username":            "alice",
			"password":            "SYNSECRET-pw-7f3a",
			"urls":                []string{"https://bank.example", "https://alt.example"},
			"notes":               "recovery codes in the safe",
			"password_updated_at": "2026-08-01",
			"password_expires_at": "2027-08-01",
		},
		"ssh_key": {
			"name":           "homelab key",
			"algorithm":      "ed25519",
			"public_key":     "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI... alice@lab",
			"private_key":    "-----BEGIN OPENSSH PRIVATE KEY-----SYNSECRET-key-----END-----",
			"key_passphrase": "SYNSECRET-pass",
			"comment":        "alice@lab",
			"fingerprint":    "SHA256:abcd1234",
			"notes":          "generated 2026-09",
		},
		"credit_card": {
			"name":                    "joint card",
			"cardholder":              "Alice Doe",
			"number":                  "4111111111111111",
			"exp_month":               7,
			"exp_year":                2029,
			"cvv":                     "123",
			"pin":                     "1234",
			"billing_address_item_id": "ADDR-ID-PLACEHOLDER",
			"notes":                   "billing address stored as identity item",
		},
		"identity": {
			"name":         "home address",
			"full_name":    "Alice Doe",
			"company":      "Doe GmbH",
			"phone":        "+49 30 123456",
			"email":        "alice@example.org",
			"country":      "DE",
			"state":        "Berlin",
			"city":         "Berlin",
			"district":     "Mitte",
			"address_line": "Torstraße 1",
			"postal_code":  "10119",
			"notes":        "identity notes",
		},
		"secure_note": {
			"name": "2FA recovery",
			"body": "SYNSECRET-note recovery codes: 1a2b 3c4d",
		},
	}
}

func payloadFixtureWithoutReference(typ string) map[string]any {
	p := map[string]any{}
	for k, v := range validPayloadFixtures()[typ] {
		p[k] = v
	}
	delete(p, "billing_address_item_id")
	return p
}

func TestItemCreateValidFixtures(t *testing.T) {
	h := newItemsHarness(t)
	h.bootstrapAdmin(t)
	alice := h.itemClient(t, "alice")

	for _, typ := range []string{"login", "ssh_key", "identity", "secure_note", "credit_card"} {
		t.Run(typ, func(t *testing.T) {
			payload := payloadFixtureWithoutReference(typ)
			if typ == "credit_card" {
				// The address reference must point at a readable identity item.
				identity := h.mustCreateItem(t, alice, "personal", "identity", payloadFixtureWithoutReference("identity"), nil)
				payload["billing_address_item_id"] = identity["id"]
			}
			tags := []string{"family", "shared", "family"} // duplicate must be dropped
			resp := h.createItem(t, alice, itemCreateBody("personal", typ, payload, tags, true))
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("status=%d", resp.StatusCode)
			}
			detail := decodeBody(t, resp)
			if detail["id"].(string)[14] != '7' {
				t.Fatalf("id not UUIDv7: %v", detail["id"])
			}
			if detail["revision"].(float64) != 1 || detail["vault_scope"] != "personal" || detail["favorite"] != true {
				t.Fatalf("meta wrong: %v", detail)
			}
			if detail["owner_id"] == nil || detail["creator_id"] != nil {
				t.Fatalf("ownership wrong: %v", detail)
			}
			gotTags := detail["tags"].([]any)
			if len(gotTags) != 2 || gotTags[0] != "family" || gotTags[1] != "shared" {
				t.Fatalf("tags not deduped: %v", gotTags)
			}
			// The payload round-trips field by field.
			returned := detail["payload"].(map[string]any)
			for k, want := range payload {
				got, ok := returned[k]
				if !ok {
					t.Fatalf("payload field %q missing in response", k)
				}
				wantJSON, _ := json.Marshal(want)
				gotJSON, _ := json.Marshal(got)
				if !bytes.Equal(wantJSON, gotJSON) {
					t.Fatalf("field %q: got %s want %s", k, gotJSON, wantJSON)
				}
			}
			// Plaintext metadata row exists exactly once with the right shape;
			// business fields live only in the encrypted columns.
			var revision, payloadVersion, favorite int
			var nonce, ciphertext []byte
			var deletedAt any
			id := detail["id"].(string)
			if err := h.db.QueryRow(`SELECT revision, payload_version, favorite, nonce, ciphertext, deleted_at FROM vault_items WHERE id=?`, id).
				Scan(&revision, &payloadVersion, &favorite, &nonce, &ciphertext, &deletedAt); err != nil {
				t.Fatal(err)
			}
			if revision != 1 || payloadVersion != 1 || favorite != 1 || deletedAt != nil {
				t.Fatalf("row wrong: rev=%d pv=%d fav=%d deleted=%v", revision, payloadVersion, favorite, deletedAt)
			}
			if len(nonce) != 24 || len(ciphertext) == 0 {
				t.Fatalf("encrypted columns wrong: nonce=%d ct=%d", len(nonce), len(ciphertext))
			}
		})
	}

	// Neither the database nor the WAL ever hold the plaintext secrets.
	for _, marker := range []string{"SYNSECRET-pw-7f3a", "SYNSECRET-key", "SYNSECRET-note"} {
		for _, suffix := range []string{"", "-wal"} {
			raw, err := os.ReadFile(h.db.Path() + suffix)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(raw, []byte(marker)) {
				t.Fatalf("plaintext %q found in db sidecar %q", marker, suffix)
			}
		}
	}
}

func TestItemCreateInvalidFixtures(t *testing.T) {
	h := newItemsHarness(t)
	h.bootstrapAdmin(t)
	alice := h.itemClient(t, "alice")

	cases := []struct {
		name     string
		typ      string
		mutate   func(map[string]any)
		topLevel func(map[string]any)
	}{
		{"login missing name", "login", func(p map[string]any) { delete(p, "name") }, nil},
		{"login empty name", "login", func(p map[string]any) { p["name"] = "" }, nil},
		{"login name over 256 runes", "login", func(p map[string]any) { p["name"] = strings.Repeat("长", 257) }, nil},
		{"login password over 1024 bytes", "login", func(p map[string]any) { p["password"] = strings.Repeat("p", 1025) }, nil},
		{"login 17 urls", "login", func(p map[string]any) {
			urls := []string{}
			for i := 0; i < 17; i++ {
				urls = append(urls, "https://x.example")
			}
			p["urls"] = urls
		}, nil},
		{"login bad date", "login", func(p map[string]any) { p["password_updated_at"] = "2026/08/01" }, nil},
		{"login unknown field", "login", func(p map[string]any) { p["totp"] = "x" }, nil},
		{"ssh bad algorithm", "ssh_key", func(p map[string]any) { p["algorithm"] = "rsa2048" }, nil},
		{"ssh missing private key", "ssh_key", func(p map[string]any) { delete(p, "private_key") }, nil},
		{"card month 13", "credit_card", func(p map[string]any) { p["exp_month"] = 13 }, nil},
		{"card year 1999", "credit_card", func(p map[string]any) { p["exp_year"] = 1999 }, nil},
		{"card missing number", "credit_card", func(p map[string]any) { delete(p, "number") }, nil},
		{"identity name missing", "identity", func(p map[string]any) { delete(p, "name") }, nil},
		{"note body over limit", "secure_note", func(p map[string]any) { p["body"] = strings.Repeat("b", 65537) }, nil},
		{"note body missing", "secure_note", func(p map[string]any) { delete(p, "body") }, nil},
		{"33 tags", "secure_note", nil, func(b map[string]any) {
			tags := []string{}
			for i := 0; i < 33; i++ {
				tags = append(tags, strings.Repeat("t", i+1))
			}
			b["tags"] = tags
		}},
		{"empty tag", "secure_note", nil, func(b map[string]any) { b["tags"] = []string{"ok", ""} }},
		{"unknown item type", "bank_note", nil, nil},
		{"unknown scope", "login", nil, func(b map[string]any) { b["vault_scope"] = "team" }},
		{"unknown top-level field", "login", nil, func(b map[string]any) { b["owner_id"] = "someone-else" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := payloadFixtureWithoutReference(tc.typ)
			if tc.mutate != nil {
				tc.mutate(payload)
			}
			body := itemCreateBody("personal", tc.typ, payload, nil, false)
			if tc.topLevel != nil {
				tc.topLevel(body)
			}
			resp := h.createItem(t, alice, body)
			defer resp.Body.Close()
			out := decodeBody(t, resp)
			if resp.StatusCode != http.StatusBadRequest || out["code"] != "VALIDATION_ERROR" {
				t.Fatalf("status=%d code=%v want 400/VALIDATION_ERROR", resp.StatusCode, out["code"])
			}
		})
	}

	// Missing or non-object payload bodies are rejected.
	for name, body := range map[string]map[string]any{
		"payload absent":  {"item_type": "login", "vault_scope": "personal"},
		"payload array":   {"item_type": "login", "vault_scope": "personal", "payload": []any{}},
		"payload string":  {"item_type": "login", "vault_scope": "personal", "payload": "x"},
		"item type unset": {"vault_scope": "personal", "payload": payloadFixtureWithoutReference("login")},
	} {
		t.Run(name, func(t *testing.T) {
			resp := h.createItem(t, alice, body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status=%d", resp.StatusCode)
			}
		})
	}

	// A request body beyond the D09 envelope budget is 413.
	huge := itemCreateBody("personal", "login", map[string]any{
		"name": "big", "username": "u", "password": "p",
		"notes": strings.Repeat("n", 300_000),
	}, nil, false)
	resp := h.createItem(t, alice, huge)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("huge payload: %d, want 413", resp.StatusCode)
	}
}

func TestItemCreateIdempotent(t *testing.T) {
	h := newItemsHarness(t)
	h.bootstrapAdmin(t)
	alice := h.itemClient(t, "alice")
	bob := h.itemClient(t, "bob")

	body := itemCreateBody("personal", "secure_note", map[string]any{"name": "note", "body": "SYNSECRET-body"}, []string{"a"}, false)
	const key = "item-create-key-00001"

	resp := h.createItemIdem(t, alice, body, key)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first create: %d", resp.StatusCode)
	}
	first := decodeBody(t, resp)

	// Same key + same content: replay the original item.
	resp = h.createItemIdem(t, alice, body, key)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("replay: %d", resp.StatusCode)
	}
	replay := decodeBody(t, resp)
	if replay["id"] != first["id"] {
		t.Fatalf("replay id %v != original %v", replay["id"], first["id"])
	}

	// Same key, different content: conflict.
	other := itemCreateBody("personal", "secure_note", map[string]any{"name": "other", "body": "x"}, nil, false)
	resp = h.createItemIdem(t, alice, other, key)
	out := decodeBody(t, resp)
	if resp.StatusCode != http.StatusConflict || out["code"] != "IDEMPOTENCY_KEY_CONFLICT" {
		t.Fatalf("conflict: %d %v", resp.StatusCode, out["code"])
	}

	// A different member with the same key never hits alice's claim.
	resp = h.createItemIdem(t, bob, body, key)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("cross-user create: %d", resp.StatusCode)
	}
	cross := decodeBody(t, resp)
	if cross["id"] == first["id"] {
		t.Fatal("cross-user replay hit another member's claim")
	}

	// Concurrent identical creations: exactly one item stored.
	const raceKey = "item-create-key-00002"
	raceBody := itemCreateBody("personal", "secure_note", map[string]any{"name": "race", "body": "x"}, nil, false)
	var wg sync.WaitGroup
	codes := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := h.createItemIdem(t, alice, raceBody, raceKey)
			codes <- resp.StatusCode
			resp.Body.Close()
		}()
	}
	wg.Wait()
	close(codes)
	created, conflict := 0, 0
	for code := range codes {
		switch code {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflict++
		default:
			t.Fatalf("unexpected concurrent status %d", code)
		}
	}
	if created < 1 || created+conflict != 8 {
		t.Fatalf("created=%d conflict=%d", created, conflict)
	}
	var rows int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM vault_items WHERE vault_scope='personal'
		AND owner_user_id=(SELECT id FROM users WHERE username_norm='alice')`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	// alice holds the first item and the race item: exactly 2 personal rows.
	if rows != 2 {
		t.Fatalf("personal items=%d, want 2", rows)
	}
}

func TestItemDetailAuthorizationAndRoundTrip(t *testing.T) {
	h := newItemsHarness(t)
	h.bootstrapAdmin(t)
	alice := h.itemClient(t, "alice")
	bob := h.itemClient(t, "bob")

	personal := h.mustCreateItem(t, alice, "personal", "login", payloadFixtureWithoutReference("login"), []string{"private"})
	personalID := personal["id"].(string)

	// Owner reads the full detail with payload.
	resp := h.request(t, "GET", "/items/"+personalID, nil, alice)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("owner read: %d", resp.StatusCode)
	}
	detail := decodeBody(t, resp)
	if detail["payload"].(map[string]any)["password"] != "SYNSECRET-pw-7f3a" {
		t.Fatalf("payload round trip broken: %v", detail["payload"])
	}
	if detail["tags"].([]any)[0] != "private" {
		t.Fatalf("tags lost: %v", detail["tags"])
	}

	// Another member and the administrator get a bare 404 (no existence oracle).
	for _, client := range []authClient{bob, h.admin} {
		resp := h.request(t, "GET", "/items/"+personalID, nil, client)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("unauthorized read: %d, want 404", resp.StatusCode)
		}
		resp.Body.Close()
	}

	// Unknown and malformed ids 404; unauthenticated 401.
	expectStatus(t, h.request(t, "GET", "/items/00000000-0000-7111-8000-000000000000", nil, alice), 404)
	expectStatus(t, h.request(t, "GET", "/items/"+personalID, nil, authClient{}), 401)

	// Writes against an unreadable item also 404 (no oracle through writes).
	update := map[string]any{"revision": 1, "payload": map[string]any{"name": "x", "body": "y"}}
	expectStatus(t, h.request(t, "PUT", "/items/"+personalID, update, bob), 404)

	// Shared items: every member reads, only the creator writes.
	shared := h.mustCreateItem(t, alice, "shared", "secure_note", map[string]any{"name": "wifi", "body": "SYNSECRET-wifi"}, nil)
	sharedID := shared["id"].(string)
	expectStatus(t, h.request(t, "GET", "/items/"+sharedID, nil, bob), 200)
	expectStatus(t, h.request(t, "GET", "/items/"+sharedID, nil, h.admin), 200)
	resp = h.request(t, "PUT", "/items/"+sharedID, update, bob)
	out := decodeBody(t, resp)
	if resp.StatusCode != http.StatusForbidden || out["code"] != "FORBIDDEN" {
		t.Fatalf("non-creator shared write: %d %v", resp.StatusCode, out["code"])
	}
	expectStatus(t, h.request(t, "PUT", "/items/"+sharedID, update, h.admin), 403)

	// CSRF is mandatory on item writes.
	noCSRF := alice
	noCSRF.csrf = ""
	resp = h.request(t, "PUT", "/items/"+sharedID, update, noCSRF)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("write without CSRF: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestUnauthorizedDecrypt(t *testing.T) {
	h := newItemsHarness(t)
	h.bootstrapAdmin(t)
	alice := h.itemClient(t, "alice")

	item := h.mustCreateItem(t, alice, "personal", "secure_note",
		map[string]any{"name": "secret note", "body": "SYNSECRET-tamper-target"}, nil)
	id := item["id"].(string)

	// Corrupt the stored ciphertext in place; decryption must fail closed
	// with the stable 500 envelope and never leak plaintext.
	if _, err := h.db.Exec(`UPDATE vault_items SET ciphertext = ? WHERE id=?`, []byte{0, 1, 2, 3}, id); err != nil {
		t.Fatal(err)
	}
	resp := h.request(t, "GET", "/items/"+id, nil, alice)
	defer resp.Body.Close()
	body := decodeBody(t, resp)
	if resp.StatusCode != http.StatusInternalServerError || body["code"] != "INTERNAL" {
		t.Fatalf("tampered read: %d %v", resp.StatusCode, body["code"])
	}
	raw, _ := json.Marshal(body)
	if bytes.Contains(raw, []byte("SYNSECRET")) {
		t.Fatal("plaintext leaked in error response")
	}
}

func TestItemListPaginationAndFilters(t *testing.T) {
	h := newItemsHarness(t)
	h.bootstrapAdmin(t)
	alice := h.itemClient(t, "alice")
	bob := h.itemClient(t, "bob")

	note := func(name string) map[string]any { return map[string]any{"name": name, "body": "b-" + name} }
	// alice: 5 personal (2 favorites: login×2 + note×3) + 2 shared.
	// bob: 3 personal + 1 shared. Shared items are visible to everyone.
	for i, spec := range []struct {
		typ string
		fav bool
	}{
		{"login", true}, {"login", false}, {"secure_note", true}, {"secure_note", false}, {"secure_note", false},
	} {
		payload := payloadFixtureWithoutReference(spec.typ)
		payload["name"] = string(rune('A'+i)) + "-alice-" + spec.typ
		resp := h.createItem(t, alice, itemCreateBody("personal", spec.typ, payload, nil, spec.fav))
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("seed item %d: %d", i, resp.StatusCode)
		}
		resp.Body.Close()
	}
	h.mustCreateItem(t, alice, "shared", "secure_note", note("shared-a1"), nil)
	h.mustCreateItem(t, alice, "shared", "secure_note", note("shared-a2"), nil)
	for _, spec := range []string{"login", "secure_note", "identity"} {
		resp := h.createItem(t, bob, itemCreateBody("personal", spec, payloadFixtureWithoutReference(spec), nil, false))
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("bob seed %s: %d", spec, resp.StatusCode)
		}
		resp.Body.Close()
	}
	h.mustCreateItem(t, bob, "shared", "secure_note", note("shared-b1"), nil)

	// Default list: alice's 5 personal + all 3 shared; bob's personal invisible.
	resp := h.request(t, "GET", "/items", nil, alice)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("list: %d", resp.StatusCode)
	}
	page := decodeBody(t, resp)
	items := page["items"].([]any)
	if len(items) != 8 {
		t.Fatalf("alice list len=%d, want 8", len(items))
	}
	defaultNext := page["next_cursor"]
	if defaultNext != nil {
		t.Fatalf("unpaged list must not have a cursor, got %v", defaultNext)
	}
	// Newest first: (updated_at, id) strictly descending; metadata only.
	var lastCreated, lastID string
	for i, it := range items {
		m := it.(map[string]any)
		if m["payload"] != nil || m["tags"] != nil {
			t.Fatalf("list leaked payload/tags: %v", m)
		}
		created, id := m["updated_at"].(string), m["id"].(string)
		if lastCreated != "" {
			if created > lastCreated || (created == lastCreated && id >= lastID) {
				t.Fatalf("order broken at %d: %s/%s after %s/%s", i, created, id, lastCreated, lastID)
			}
		}
		lastCreated, lastID = created, id
	}

	// Follow the cursor from a small first page to the end (8 items, 3/page).
	collected := 0
	var cursorAny any = "start"
	first := true
	for cursorAny != nil {
		var resp *http.Response
		if first {
			resp = h.request(t, "GET", "/items?limit=3", nil, alice)
		} else {
			resp = h.request(t, "GET", "/items?limit=3&cursor="+cursorAny.(string), nil, alice)
		}
		first = false
		if resp.StatusCode != 200 {
			t.Fatalf("cursor page: %d", resp.StatusCode)
		}
		p := decodeBody(t, resp)
		collected += len(p["items"].([]any))
		cursorAny = p["next_cursor"]
	}
	if collected != 8 {
		t.Fatalf("cursor walk collected %d, want 8", collected)
	}

	count := func(query string, client authClient) int {
		t.Helper()
		resp := h.request(t, "GET", query, nil, client)
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d", query, resp.StatusCode)
		}
		return len(decodeBody(t, resp)["items"].([]any))
	}
	if got := count("/items?scope=personal", alice); got != 5 {
		t.Fatalf("personal filter: %d, want 5", got)
	}
	if got := count("/items?scope=shared", alice); got != 3 {
		t.Fatalf("shared filter: %d, want 3", got)
	}
	if got := count("/items?type=login", alice); got != 2 {
		t.Fatalf("type filter: %d, want 2", got)
	}
	if got := count("/items?scope=shared&favorite=true", alice); got != 0 {
		t.Fatalf("combo filter: %d, want 0", got)
	}
	if got := count("/items?favorite=true", alice); got != 2 {
		t.Fatalf("favorite filter: %d, want 2", got)
	}
	if got := count("/items", bob); got != 6 {
		t.Fatalf("bob list (3 personal + 3 shared): %d, want 6", got)
	}

	// Cursor is actor- and filter-bound.
	resp = h.request(t, "GET", "/items?limit=2", nil, alice)
	alicePage := decodeBody(t, resp)
	resp.Body.Close()
	aliceCursor := alicePage["next_cursor"].(string)
	resp = h.request(t, "GET", "/items?limit=2&cursor="+aliceCursor, nil, bob)
	out := decodeBody(t, resp)
	if resp.StatusCode != http.StatusBadRequest || out["code"] != "VALIDATION_ERROR" {
		t.Fatalf("cross-user cursor: %d %v", resp.StatusCode, out["code"])
	}
	resp = h.request(t, "GET", "/items?scope=shared&limit=2&cursor="+aliceCursor, nil, alice)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("filter-mismatched cursor: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Limit bounds and unknown scope/type values.
	for _, q := range []string{"?limit=0", "?limit=101", "?scope=team", "?type=vault"} {
		resp := h.request(t, "GET", "/items"+q, nil, alice)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: %d, want 400", q, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestItemUpdateRevisionHistoryAndImmutability(t *testing.T) {
	h := newItemsHarness(t)
	h.bootstrapAdmin(t)
	alice := h.itemClient(t, "alice")

	v1 := map[string]any{"name": "bank", "username": "alice", "password": "SYNSECRET-v1", "notes": "v1"}
	item := h.mustCreateItem(t, alice, "personal", "login", v1, []string{"one", "two"})
	id := item["id"].(string)
	createdAt := item["created_at"].(string)

	// Effective update: revision bumps and the old version is archived.
	v2 := map[string]any{"name": "bank", "username": "alice", "password": "SYNSECRET-v2", "notes": "v2"}
	resp := h.request(t, "PUT", "/items/"+id, map[string]any{"revision": 1, "payload": v2, "tags": []string{"one", "three"}}, alice)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("update: %d", resp.StatusCode)
	}
	detail := decodeBody(t, resp)
	if detail["revision"].(float64) != 2 {
		t.Fatalf("revision after update: %v", detail["revision"])
	}
	tags := detail["tags"].([]any)
	if len(tags) != 2 || tags[0] != "one" || tags[1] != "three" {
		t.Fatalf("tags not replaced: %v", tags)
	}

	// Exactly one history row with the v1 ciphertext, bound to revision 1.
	var oldRevision, oldPayloadVersion int
	var oldNonce, oldCiphertext []byte
	var historyCreated string
	if err := h.db.QueryRow(`SELECT revision, payload_version, nonce, ciphertext, created_at FROM item_versions WHERE item_id=?`, id).
		Scan(&oldRevision, &oldPayloadVersion, &oldNonce, &oldCiphertext, &historyCreated); err != nil {
		t.Fatal(err)
	}
	if oldRevision != 1 || oldPayloadVersion != 1 || historyCreated != createdAt {
		t.Fatalf("history row wrong: rev=%d created=%s want %s", oldRevision, historyCreated, createdAt)
	}
	key := mustMasterKey(t)
	oldAAD := crypto.AAD{ItemID: id, Scope: "personal", SubjectID: h.lookupUserID(t, "alice"), PayloadVersion: 1, Revision: 1}
	oldPlain, err := key.DecryptColumns(uint16(oldPayloadVersion), oldNonce, oldCiphertext, oldAAD)
	if err != nil || !bytes.Contains(oldPlain, []byte("SYNSECRET-v1")) {
		t.Fatalf("old version not decryptable under old AAD: %v", err)
	}

	// Stale revision: 409 with the current revision, no overwrite.
	resp = h.request(t, "PUT", "/items/"+id, map[string]any{"revision": 1, "payload": v1}, alice)
	out := decodeBody(t, resp)
	if resp.StatusCode != http.StatusConflict || out["code"] != "REVISION_CONFLICT" || out["current_revision"].(float64) != 2 {
		t.Fatalf("stale update: %d %v", resp.StatusCode, out)
	}
	resp.Body.Close()

	// No-change update: same payload content and tags → no new revision/history.
	resp = h.request(t, "PUT", "/items/"+id, map[string]any{"revision": 2, "payload": v2, "tags": []string{"one", "three"}}, alice)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("no-change update: %d", resp.StatusCode)
	}
	same := decodeBody(t, resp)
	if same["revision"].(float64) != 2 {
		t.Fatalf("no-change bumped revision: %v", same["revision"])
	}
	var historyCount int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM item_versions WHERE item_id=?`, id).Scan(&historyCount); err != nil {
		t.Fatal(err)
	}
	if historyCount != 1 {
		t.Fatalf("history rows after no-change: %d", historyCount)
	}

	// Favorite-only change is effective.
	resp = h.request(t, "PUT", "/items/"+id, map[string]any{"revision": 2, "payload": v2, "favorite": true}, alice)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("favorite update: %d", resp.StatusCode)
	}
	if got := decodeBody(t, resp); got["revision"].(float64) != 3 || got["favorite"] != true {
		t.Fatalf("favorite update detail: %v", got)
	}

	// Tags absent from the request are preserved; a real content change
	// bumps the revision.
	v3 := map[string]any{"name": "bank", "username": "alice", "password": "SYNSECRET-v2", "notes": "v3"}
	resp = h.request(t, "PUT", "/items/"+id, map[string]any{"revision": 3, "payload": v3}, alice)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("absent-tags update: %d", resp.StatusCode)
	}
	if got := decodeBody(t, resp); len(got["tags"].([]any)) != 2 {
		t.Fatalf("absent tags dropped: %v", got["tags"])
	}

	// Ownership, scope and type cannot travel through an update.
	for name, extra := range map[string]map[string]any{
		"scope":   {"vault_scope": "shared"},
		"owner":   {"owner_id": "someone"},
		"creator": {"creator_id": "someone"},
	} {
		body := map[string]any{"revision": 4, "payload": v2}
		for k, v := range extra {
			body[k] = v
		}
		resp := h.request(t, "PUT", "/items/"+id, body, alice)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s smuggling: %d", name, resp.StatusCode)
		}
		resp.Body.Close()
	}
	// A payload of the wrong type is rejected (type is immutable).
	resp = h.request(t, "PUT", "/items/"+id, map[string]any{"revision": 4, "payload": map[string]any{"name": "n", "body": "b"}}, alice)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("type-changing payload: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Concurrent updates from the same revision: exactly one succeeds.
	item2 := h.mustCreateItem(t, alice, "personal", "secure_note", map[string]any{"name": "race", "body": "x"}, nil)
	id2 := item2["id"].(string)
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			payload := map[string]any{"name": "race", "body": "from-goroutine"}
			resp := h.request(t, "PUT", "/items/"+id2, map[string]any{"revision": 1, "payload": payload}, alice)
			statuses <- resp.StatusCode
			resp.Body.Close()
		}(i)
	}
	wg.Wait()
	close(statuses)
	var ok200, conflict409 int
	for s := range statuses {
		switch s {
		case 200:
			ok200++
		case 409:
			conflict409++
		default:
			t.Fatalf("unexpected concurrent update status %d", s)
		}
	}
	if ok200 != 1 || conflict409 != 1 {
		t.Fatalf("concurrent updates: 200=%d 409=%d", ok200, conflict409)
	}
}

func TestItemAddressReferences(t *testing.T) {
	h := newItemsHarness(t)
	h.bootstrapAdmin(t)
	alice := h.itemClient(t, "alice")
	bob := h.itemClient(t, "bob")

	personalIdentity := h.mustCreateItem(t, alice, "personal", "identity", payloadFixtureWithoutReference("identity"), nil)
	identityID := personalIdentity["id"].(string)

	ccWithRef := func(ref any) map[string]any {
		p := payloadFixtureWithoutReference("credit_card")
		p["billing_address_item_id"] = ref
		return p
	}

	// Own readable identity reference is accepted.
	resp := h.createItem(t, alice, itemCreateBody("personal", "credit_card", ccWithRef(identityID), nil, false))
	expectStatus(t, resp, 201)
	resp.Body.Close()

	// Another member's personal identity is not readable → reference forbidden.
	resp = h.createItem(t, bob, itemCreateBody("personal", "credit_card", ccWithRef(identityID), nil, false))
	out := decodeBody(t, resp)
	if resp.StatusCode != http.StatusConflict || out["code"] != "REFERENCE_FORBIDDEN" {
		t.Fatalf("cross-user reference: %d %v", resp.StatusCode, out["code"])
	}

	// Shared items may only reference shared targets.
	resp = h.createItem(t, alice, itemCreateBody("shared", "credit_card", ccWithRef(identityID), nil, false))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("shared→personal reference: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Shared→shared is fine.
	sharedIdentity := h.mustCreateItem(t, alice, "shared", "identity", payloadFixtureWithoutReference("identity"), nil)
	resp = h.createItem(t, alice, itemCreateBody("shared", "credit_card", ccWithRef(sharedIdentity["id"]), nil, false))
	expectStatus(t, resp, 201)
	resp.Body.Close()

	// Unknown target, empty reference or wrong target type is rejected
	// with the same code (no existence oracle).
	for _, bad := range []any{"00000000-0000-7111-8000-000000000000", ""} {
		resp := h.createItem(t, alice, itemCreateBody("personal", "credit_card", ccWithRef(bad), nil, false))
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("bad reference %q: %d", bad, resp.StatusCode)
		}
		resp.Body.Close()
	}
	loginItem := h.mustCreateItem(t, alice, "personal", "login", payloadFixtureWithoutReference("login"), nil)
	resp = h.createItem(t, alice, itemCreateBody("personal", "credit_card", ccWithRef(loginItem["id"]), nil, false))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("non-identity reference: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// JSON null clears the reference (nullable field).
	nullRef := ccWithRef(nil)
	resp = h.createItem(t, alice, itemCreateBody("personal", "credit_card", nullRef, nil, false))
	expectStatus(t, resp, 201)
	resp.Body.Close()

	// Updates enforce the same reference policy as creates.
	card := h.mustCreateItem(t, bob, "personal", "credit_card", payloadFixtureWithoutReference("credit_card"), nil)
	resp = h.request(t, "PUT", "/items/"+card["id"].(string), map[string]any{
		"revision": 1, "payload": ccWithRef(identityID),
	}, bob)
	out = decodeBody(t, resp)
	if resp.StatusCode != http.StatusConflict || out["code"] != "REFERENCE_FORBIDDEN" {
		t.Fatalf("cross-user reference on update: %d %v", resp.StatusCode, out["code"])
	}
}

func TestItemAuditTrail(t *testing.T) {
	h := newItemsHarness(t)
	h.bootstrapAdmin(t)
	alice := h.itemClient(t, "alice")

	item := h.mustCreateItem(t, alice, "personal", "secure_note",
		map[string]any{"name": "SYNSECRET-title", "body": "SYNSECRET-body"}, nil)
	id := item["id"].(string)
	resp := h.request(t, "PUT", "/items/"+id, map[string]any{"revision": 1, "payload": map[string]any{"name": "n2", "body": "b2"}}, alice)
	resp.Body.Close()
	resp = h.request(t, "GET", "/items/"+id, nil, alice)
	resp.Body.Close()

	aliceID := h.lookupUserID(t, "alice")
	var events []string
	rows, err := h.db.Query(`SELECT event FROM audit_events WHERE target_id=? ORDER BY created_at, id`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
	}
	want := []string{audit.EventVaultItemCreated, audit.EventVaultItemUpdated, audit.EventVaultItemViewed}
	if len(events) != len(want) {
		t.Fatalf("events=%v want %v", events, want)
	}
	for i, w := range want {
		if events[i] != w {
			t.Fatalf("event[%d]=%s want %s", i, events[i], w)
		}
	}

	// Audit rows carry opaque ids only; no secret or title ever lands there.
	var raw string
	if err := h.db.QueryRow(`SELECT GROUP_CONCAT(event || target_type || COALESCE(actor_id,'') || COALESCE(target_id,'')) FROM audit_events`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "SYNSECRET") || strings.Contains(raw, "alice") {
		t.Fatalf("audit contains sensitive content: %s", raw)
	}
	var actor string
	if err := h.db.QueryRow(`SELECT actor_id FROM audit_events WHERE target_id=? AND event=?`, id, audit.EventVaultItemCreated).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	if actor != aliceID {
		t.Fatalf("actor %q != member internal id", actor)
	}
}

func TestUserDeleteCascadeWithRealItems(t *testing.T) {
	h := newItemsHarness(t)
	h.bootstrapAdmin(t)

	// A directly inserted member builds personal and shared items.
	doomed := h.itemClient(t, "doomed")
	personal := h.mustCreateItem(t, doomed, "personal", "login", payloadFixtureWithoutReference("login"), nil)
	shared := h.mustCreateItem(t, doomed, "shared", "secure_note", map[string]any{"name": "family", "body": "SYNSECRET-shared"}, nil)
	// One update so a history row exists for the personal item.
	update := map[string]any{"revision": 1, "payload": map[string]any{"name": "n", "username": "u", "password": "p2"}}
	expectStatus(t, h.request(t, "PUT", "/items/"+personal["id"].(string), update, doomed), 200)

	// Capture the internal id before deletion; the member row is gone after.
	doomedID := h.lookupUserID(t, "doomed")

	// Admin deletes the member through the real API with exact confirmation.
	del := h.request(t, "DELETE", "/users/"+doomedID, map[string]string{"confirm_username": "doomed"}, h.admin)
	expectStatus(t, del, 204)
	del.Body.Close()

	var personalItems, sharedItems, versions int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM vault_items WHERE id=?`, personal["id"]).Scan(&personalItems); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM vault_items WHERE id=?`, shared["id"]).Scan(&sharedItems); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM item_versions WHERE item_id=?`, personal["id"]).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if personalItems != 0 || sharedItems != 0 || versions != 0 {
		t.Fatalf("cascade left rows: personal=%d shared=%d versions=%d", personalItems, sharedItems, versions)
	}

	// The deleted member's items are gone for readers too.
	alice := h.itemClient(t, "alice")
	expectStatus(t, h.request(t, "GET", "/items/"+shared["id"].(string), nil, alice), 404)

	// Audit rows survive with the opaque internal id; no username snapshot.
	var events int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE actor_id=?`, doomedID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events == 0 {
		t.Fatal("audit events were cascaded away")
	}
	var raw string
	if err := h.db.QueryRow(`SELECT GROUP_CONCAT(COALESCE(actor_id,'') || COALESCE(target_id,'')) FROM audit_events`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "doomed") {
		t.Fatal("audit stores username snapshots")
	}
}

// TestItemRestartDecryption proves the M3 exit gate: a freshly opened
// process (new SQLite handle, new vault service, same database file and
// master key) decrypts every payload written by the previous one.
func TestItemRestartDecryption(t *testing.T) {
	h := newItemsHarness(t)
	h.bootstrapAdmin(t)
	alice := h.itemClient(t, "alice")

	personal := h.mustCreateItem(t, alice, "personal", "login", payloadFixtureWithoutReference("login"), nil)
	shared := h.mustCreateItem(t, alice, "shared", "secure_note", map[string]any{"name": "shared", "body": "SYNSECRET-after-restart"}, nil)
	// One update so history exists too.
	update := map[string]any{"revision": 1, "payload": map[string]any{"name": "n", "username": "u", "password": "p-after-restart"}}
	expectStatus(t, h.request(t, "PUT", "/items/"+personal["id"].(string), update, alice), 200)

	// "Restart": reopen the same database file and rebuild the service.
	reopened, err := sqlite.Open(h.db.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := sqlite.Migrate(reopened.DB, migrations.FS); err != nil {
		t.Fatal(err)
	}
	restarted, err := vault.NewService(reopened.DB, mustMasterKey(t), vault.Options{Audit: audit.NewService(audit.Options{})})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	aliceID := h.lookupUserID(t, "alice")
	for _, id := range []string{personal["id"].(string), shared["id"].(string)} {
		detail, err := restarted.Get(ctx, &auth.Principal{UserID: aliceID, Role: "member"}, id)
		if err != nil {
			t.Fatalf("restart read %s: %v", id, err)
		}
		if detail.Payload == nil {
			t.Fatalf("restart read %s: empty payload", id)
		}
	}
	// The updated login decrypts to the new content under the new revision.
	detail, err := restarted.Get(ctx, &auth.Principal{UserID: aliceID, Role: "member"}, personal["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if got := detail.Payload.(*vault.LoginPayload).Password; got != "p-after-restart" {
		t.Fatalf("post-restart payload: %q", got)
	}
	// History survives and decrypts after the restart too.
	entries, err := restarted.ListHistory(ctx, &auth.Principal{UserID: aliceID, Role: "member"}, personal["id"].(string), "", 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("post-restart history: %v %v", entries, err)
	}
}

// TestItemDetailWithoutTagsReturnsEmptyArray pins the contract that a
// tag-less item renders `"tags":[]` (never null) in detail responses.
func TestItemDetailWithoutTagsReturnsEmptyArray(t *testing.T) {
	h := newItemsHarness(t)
	h.bootstrapAdmin(t)
	alice := h.itemClient(t, "alice")

	item := h.mustCreateItem(t, alice, "personal", "secure_note",
		map[string]any{"name": "no tags", "body": "b"}, nil)
	id := item["id"].(string)

	var tagsJSON string
	if err := h.db.QueryRow(`SELECT json_extract(payload_ciphertext, '$') FROM vault_items WHERE id=?`, id).Scan(&tagsJSON); err == nil {
		// modernc has no json1 guarantee; ignore — the HTTP assertion below is
		// the contract.
		_ = tagsJSON
	}
	resp := h.request(t, "GET", "/items/"+id, nil, alice)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("detail: %d", resp.StatusCode)
	}
	detail := decodeBody(t, resp)
	tags, ok := detail["tags"].([]any)
	if !ok || len(tags) != 0 {
		t.Fatalf("tags must be [] (got %v)", detail["tags"])
	}
}
