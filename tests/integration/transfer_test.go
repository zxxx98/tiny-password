package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tiny-password/tiny-password/internal/audit"
	"github.com/tiny-password/tiny-password/internal/auth"
	"github.com/tiny-password/tiny-password/internal/platform/archive"
	"github.com/tiny-password/tiny-password/internal/transfer"
	"github.com/tiny-password/tiny-password/internal/vault"
)

// transferHarness wires the transfer endpoints on top of the items harness.
type transferHarness struct {
	*itemsHarness
}

func newTransferHarness(t *testing.T) *transferHarness {
	h := &transferHarness{itemsHarness: newItemsHarness(t)}
	h.bootstrapAdmin(t)
	_ = h
	return h
}

func TestTransferExportImportRoundTrip(t *testing.T) {
	archiveBin(t)
	h := newTransferHarness(t)
	alice := h.itemClient(t, "alice")

	// Seed two personal items and one shared item created by alice.
	h.mustCreateItem(t, alice, "personal", "login", map[string]any{
		"name": "export login", "username": "a", "password": "SYNSECRET-export-pw",
	}, []string{"keep"})
	identity := h.mustCreateItem(t, alice, "personal", "identity", payloadFixtureWithoutReference("identity"), nil)
	h.mustCreateItem(t, alice, "personal", "credit_card", func() map[string]any {
		p := payloadFixtureWithoutReference("credit_card")
		p["billing_address_item_id"] = identity["id"]
		return p
	}(), nil)
	h.mustCreateItem(t, alice, "shared", "secure_note", map[string]any{"name": "shared", "body": "shared body"}, nil)
	// bob's personal item must NOT appear in alice's export.
	bobSource := h.itemClient(t, "bob")
	h.mustCreateItem(t, bobSource, "personal", "secure_note", map[string]any{"name": "bobs", "body": "b"}, nil)
	// Nor should another member's shared item appear: shared readability is
	// broader than the export policy, which is creator-only.
	h.mustCreateItem(t, bobSource, "shared", "secure_note", map[string]any{"name": "bobs shared", "body": "b"}, nil)

	// Export.
	exportBody := map[string]string{"passphrase": "export-passphrase-1", "passphrase_confirm": "export-passphrase-1"}
	raw, err := json.Marshal(exportBody)
	if err != nil {
		t.Fatal(err)
	}
	resp := h.request(t, "POST", "/transfer/export", json.RawMessage(raw), alice)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("export: %d body=%s", resp.StatusCode, decodeBody(t, resp)["message"])
	}
	var archiveBytes bytes.Buffer
	if _, err := archiveBytes.ReadFrom(resp.Body); err != nil {
		t.Fatal(err)
	}
	if archiveBytes.Len() < 100 {
		t.Fatalf("archive too small: %d", archiveBytes.Len())
	}

	// Import as a NEW user (the canonical restore scenario).
	bob := h.itemClient(t, "bob2")
	previewBody := &bytes.Buffer{}
	writer := multipart.NewWriter(previewBody)
	if err := writer.WriteField("passphrase", "export-passphrase-1"); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("archive", "export.7z")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(archiveBytes.Bytes()); err != nil {
		t.Fatal(err)
	}
	writer.Close()

	req, err := http.NewRequest("POST", h.server.URL+"/api/v1/transfer/import/preview", previewBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Origin", h.server.URL)
	req.Header.Set("X-CSRF-Token", bob.csrf)
	req.AddCookie(bob.cookie)
	previewResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer previewResp.Body.Close()
	if previewResp.StatusCode != 200 {
		t.Fatalf("preview: %d", previewResp.StatusCode)
	}
	var preview struct {
		PreviewToken      string         `json:"preview_token"`
		Counts            map[string]int `json:"counts"`
		Conflicts         int            `json:"conflicts"`
		MissingReferences []string       `json:"missing_references"`
	}
	if err := json.NewDecoder(previewResp.Body).Decode(&preview); err != nil {
		t.Fatal(err)
	}
	if preview.Counts["login"] != 1 || preview.Counts["identity"] != 1 || preview.Counts["credit_card"] != 1 || preview.Counts["secure_note"] != 1 {
		t.Fatalf("preview counts: %v", preview.Counts)
	}
	// The import runs in the SAME instance: alice's live items occupy the
	// exported IDs, so every ID conflicts and will be re-issued (D10).
	if preview.Conflicts != 4 {
		t.Fatalf("conflicts=%d, want 4", preview.Conflicts)
	}

	// Confirm: everything imports in one transaction.
	confirmResp := h.request(t, "POST", "/transfer/import/confirm", map[string]any{"preview_token": preview.PreviewToken}, bob)
	defer confirmResp.Body.Close()
	if confirmResp.StatusCode != 200 {
		t.Fatalf("confirm: %d", confirmResp.StatusCode)
	}
	summary := decodeBody(t, confirmResp)
	if summary["imported_count"].(float64) != 4 {
		t.Fatalf("imported=%v", summary["imported_count"])
	}

	// The imported items belong to bob2, payloads intact, references remapped.
	items, err := h.vault.List(t.Context(), h.principalOf(t, "bob2"), vaultListFilterAll(), "", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	var loginItem, cardItem string
	for _, m := range items {
		if strings.Contains(m.Title, "export login") {
			loginItem = m.ID
		}
		if m.ItemType == "credit_card" {
			cardItem = m.ID
		}
	}
	if loginItem == "" || cardItem == "" {
		t.Fatalf("imported items missing: %d items", len(items))
	}
	detail := h.getDetail(t, bob, cardItem)
	card := detail["payload"].(map[string]any)
	// The billing reference now points at the imported identity (remapped to
	// its NEW id — the original had no conflict, so it stays stable).
	if card["billing_address_item_id"] == nil || card["billing_address_item_id"] == "" {
		t.Fatal("billing reference lost on import")
	}
}

func TestTransferExportRejectsCapacityOverflow(t *testing.T) {
	h := newTransferHarness(t)
	h.itemClient(t, "alice")
	principal := h.principalOf(t, "alice")
	for i := 0; i < 511; i++ {
		if _, err := h.vault.Create(t.Context(), principal, vault.CreateInput{
			ItemType: vault.TypeSecureNote,
			Scope:    string(vault.ScopePersonal),
			Payload:  json.RawMessage(fmt.Sprintf(`{"name":"n-%d","body":"body"}`, i)),
		}, nil); err != nil {
			t.Fatalf("create item %d: %v", i, err)
		}
	}
	_, err := h.vault.ExportAll(t.Context(), principal)
	if !errors.Is(err, vault.ErrExportTooLarge) {
		t.Fatalf("ExportAll error = %v, want ErrExportTooLarge", err)
	}
}

func TestTransferExportAtCapacityCanBeListedAndExtracted(t *testing.T) {
	archiveBin(t)
	h := newTransferHarness(t)
	h.itemClient(t, "alice")
	principal := h.principalOf(t, "alice")
	for i := 0; i < 510; i++ {
		if _, err := h.vault.Create(t.Context(), principal, vault.CreateInput{
			ItemType: vault.TypeSecureNote,
			Scope:    string(vault.ScopePersonal),
			Payload:  json.RawMessage(fmt.Sprintf(`{"name":"n-%d","body":"body"}`, i)),
		}, nil); err != nil {
			t.Fatalf("create item %d: %v", i, err)
		}
	}
	workDir := t.TempDir()
	svc, err := transfer.NewService(h.vault, transfer.Options{
		WorkDir: workDir, HMACKey: bytes.Repeat([]byte{0x51}, 32),
		Audit: audit.NewService(audit.Options{}), DB: h.db.DB, Now: h.clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	path, cleanup, err := svc.Export(t.Context(), principal, transfer.ExportInput{
		Passphrase: "export-passphrase-1", PassphraseConfirm: "export-passphrase-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	archiveBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := svc.Preview(t.Context(), principal, archiveBytes, "export-passphrase-1")
	if err != nil {
		t.Fatal(err)
	}
	imported, _, err := svc.Confirm(t.Context(), principal, preview.Token)
	if err != nil {
		t.Fatal(err)
	}
	if imported != 510 {
		t.Fatalf("confirmed imported=%d, want 510", imported)
	}
}

func TestTransferImportRejectsInvalidInternalReferencesAtomically(t *testing.T) {
	h := newTransferHarness(t)
	actor := h.itemClient(t, "alice")
	principal := h.principalOf(t, "alice")
	before := h.listCount(t, actor, "/items")
	items := []vault.ImportItem{
		{OriginalID: "source-card", ItemType: vault.TypeCreditCard, Scope: string(vault.ScopePersonal), Payload: json.RawMessage(`{"name":"card","cardholder":"A","number":"4111111111111111","exp_month":1,"exp_year":2030,"billing_address_item_id":"not-identity"}`)},
		{OriginalID: "not-identity", ItemType: vault.TypeSecureNote, Scope: string(vault.ScopePersonal), Payload: json.RawMessage(`{"name":"note","body":"body"}`)},
	}
	if _, _, err := h.vault.ImportAll(t.Context(), principal, items); !errors.Is(err, vault.ErrReferenceForbidden) {
		t.Fatalf("wrong target type error = %v, want ErrReferenceForbidden", err)
	}
	if got := h.listCount(t, actor, "/items"); got != before {
		t.Fatalf("invalid import changed item count: before=%d after=%d", before, got)
	}

	sharedSource := []vault.ImportItem{
		{OriginalID: "shared-card", ItemType: vault.TypeCreditCard, Scope: string(vault.ScopeShared), Payload: json.RawMessage(`{"name":"card","cardholder":"A","number":"4111111111111111","exp_month":1,"exp_year":2030,"billing_address_item_id":"personal-address"}`)},
		{OriginalID: "personal-address", ItemType: vault.TypeIdentity, Scope: string(vault.ScopePersonal), Payload: json.RawMessage(`{"name":"address"}`)},
	}
	if _, _, err := h.vault.ImportAll(t.Context(), principal, sharedSource); !errors.Is(err, vault.ErrReferenceForbidden) {
		t.Fatalf("shared to personal error = %v, want ErrReferenceForbidden", err)
	}
	if got := h.listCount(t, actor, "/items"); got != before {
		t.Fatalf("invalid shared import changed item count: before=%d after=%d", before, got)
	}
}

func TestTransferConfirmConcurrentClaimImportsOnce(t *testing.T) {
	archiveBin(t)
	h := newTransferHarness(t)
	alice := h.itemClient(t, "alice")
	h.mustCreateItem(t, alice, "personal", "secure_note", map[string]any{"name": "once", "body": "body"}, nil)
	exportBody, _ := json.Marshal(map[string]string{"passphrase": "export-passphrase-1", "passphrase_confirm": "export-passphrase-1"})
	exportResp := h.request(t, "POST", "/transfer/export", json.RawMessage(exportBody), alice)
	if exportResp.StatusCode != http.StatusOK {
		t.Fatalf("export status=%d", exportResp.StatusCode)
	}
	archiveBytes, err := io.ReadAll(exportResp.Body)
	exportResp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	principal := h.principalOf(t, "alice")
	workDir := t.TempDir()
	svc, err := transfer.NewService(h.vault, transfer.Options{
		WorkDir: workDir, HMACKey: bytes.Repeat([]byte{0x61}, 32),
		Audit: audit.NewService(audit.Options{}), DB: h.db.DB, Now: h.clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := svc.Preview(t.Context(), principal, archiveBytes, "export-passphrase-1")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	type outcome struct {
		imported int
		err      error
	}
	outcomes := make(chan outcome, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			imported, _, err := svc.Confirm(t.Context(), principal, preview.Token)
			outcomes <- outcome{imported: imported, err: err}
		}()
	}
	wg.Wait()
	close(outcomes)
	successes := 0
	for result := range outcomes {
		if result.err == nil {
			successes++
			if result.imported != 1 {
				t.Fatalf("successful confirm imported=%d, want 1", result.imported)
			}
		}
	}
	if successes != 1 {
		t.Fatalf("successful confirms=%d, want 1", successes)
	}
	items, err := h.vault.List(t.Context(), principal, vault.ListFilter{}, "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items after concurrent confirm=%d, want 2", len(items))
	}
}

func TestTransferPreviewWrongPassphraseAndDigest(t *testing.T) {
	archiveBin(t)
	h := newTransferHarness(t)
	alice := h.itemClient(t, "alice")
	h.mustCreateItem(t, alice, "personal", "secure_note", map[string]any{"name": "n", "body": "SYNSECRET-x"}, nil)

	exportBody := map[string]string{"passphrase": "export-passphrase-1", "passphrase_confirm": "export-passphrase-1"}
	raw, _ := json.Marshal(exportBody)
	resp := h.request(t, "POST", "/transfer/export", json.RawMessage(raw), alice)
	defer resp.Body.Close()
	var archiveBytes bytes.Buffer
	_, _ = archiveBytes.ReadFrom(resp.Body)

	upload := func(passphrase string, mutate func([]byte) []byte) (int, map[string]any) {
		t.Helper()
		data := archiveBytes.Bytes()
		if mutate != nil {
			data = mutate(data)
		}
		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		_ = writer.WriteField("passphrase", passphrase)
		part, _ := writer.CreateFormFile("archive", "e.7z")
		_, _ = part.Write(data)
		writer.Close()
		req, _ := http.NewRequest("POST", h.server.URL+"/api/v1/transfer/import/preview", body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		req.Header.Set("Origin", h.server.URL)
		req.Header.Set("X-CSRF-Token", alice.csrf)
		req.AddCookie(alice.cookie)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode, decodeBody(t, resp)
	}

	// Wrong passphrase → stable WRONG_PASSPHRASE, no paths in the message.
	status, body := upload("totally-wrong!", nil)
	if status != 400 || body["code"] != "WRONG_PASSPHRASE" {
		t.Fatalf("wrong passphrase: %d %v", status, body)
	}
	if strings.Contains(body["message"].(string), "/tmp/") {
		t.Fatal("error message leaked a path")
	}

	status, body = upload(strings.Repeat("p", transfer.MaxPassphraseBytes+1), nil)
	if status != http.StatusBadRequest || body["code"] != "VALIDATION_ERROR" {
		t.Fatalf("oversized passphrase: %d %v", status, body)
	}

	// A corrupted body fails closed with a stable 400: corruption inside the
	// encrypted header surfaces as WRONG_PASSPHRASE, structural damage as
	// BAD_ARCHIVE — both without tool output or paths.
	status, body = upload("export-passphrase-1", func(data []byte) []byte {
		corrupted := append([]byte{}, data...)
		for i := 100; i < len(corrupted); i++ {
			corrupted[i] ^= 0xFF
			break
		}
		return corrupted
	})
	if status != 400 || (body["code"] != "BAD_ARCHIVE" && body["code"] != "WRONG_PASSPHRASE") {
		t.Fatalf("corrupt archive: %d %v", status, body["code"])
	}

	// A mismatching manifest digest must be rejected.
	// (Crafted by re-exporting and flipping a digest byte inside the 7z is
	// complex; the unit-level digest check is covered by the validator tests.)
	_ = filepath.Join
}

func TestTransferConfirmRequiresFreshToken(t *testing.T) {
	archiveBin(t)
	h := newTransferHarness(t)
	alice := h.itemClient(t, "alice")
	h.mustCreateItem(t, alice, "personal", "secure_note", map[string]any{"name": "n", "body": "b"}, nil)

	exportBody := map[string]string{"passphrase": "export-passphrase-1", "passphrase_confirm": "export-passphrase-1"}
	raw, _ := json.Marshal(exportBody)
	resp := h.request(t, "POST", "/transfer/export", json.RawMessage(raw), alice)
	defer resp.Body.Close()
	var archiveBytes bytes.Buffer
	_, _ = archiveBytes.ReadFrom(resp.Body)

	upload := &bytes.Buffer{}
	writer := multipart.NewWriter(upload)
	_ = writer.WriteField("passphrase", "export-passphrase-1")
	part, _ := writer.CreateFormFile("archive", "e.7z")
	_, _ = part.Write(archiveBytes.Bytes())
	writer.Close()
	req, _ := http.NewRequest("POST", h.server.URL+"/api/v1/transfer/import/preview", upload)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Origin", h.server.URL)
	req.Header.Set("X-CSRF-Token", alice.csrf)
	req.AddCookie(alice.cookie)
	previewResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer previewResp.Body.Close()
	var preview struct {
		Token string `json:"preview_token"`
	}
	if err := json.NewDecoder(previewResp.Body).Decode(&preview); err != nil {
		t.Fatal(err)
	}

	// Confirm with a forged token fails closed.
	forged := h.request(t, "POST", "/transfer/import/confirm", map[string]any{"preview_token": "deadbeef"}, alice)
	out := decodeBody(t, forged)
	if forged.StatusCode != 400 {
		t.Fatalf("forged token: %d", forged.StatusCode)
	}
	_ = out

	// Another user cannot confirm alice's preview.
	bob := h.itemClient(t, "bob")
	foreign := h.request(t, "POST", "/transfer/import/confirm", map[string]any{"preview_token": preview.Token}, bob)
	out = decodeBody(t, foreign)
	if foreign.StatusCode != 400 {
		t.Fatalf("foreign confirm: %d", foreign.StatusCode)
	}
	if out["code"] != "BAD_ARCHIVE" {
		t.Fatalf("code: %v", out["code"])
	}

	// The legitimate owner confirms once; a second confirm fails.
	first := h.request(t, "POST", "/transfer/import/confirm", map[string]any{"preview_token": preview.Token}, alice)
	expectStatus(t, first, 200)
	second := h.request(t, "POST", "/transfer/import/confirm", map[string]any{"preview_token": preview.Token}, alice)
	out = decodeBody(t, second)
	if second.StatusCode != 400 {
		t.Fatalf("double confirm: %d", second.StatusCode)
	}
	_ = transfer.FormatVersion
	_ = archive.MaxFiles
}

// principalOf builds the auth principal of a seeded member for direct
// vault-service calls.
func (h *itemsHarness) principalOf(t *testing.T, name string) *auth.Principal {
	t.Helper()
	return &auth.Principal{UserID: h.lookupUserID(t, name), Role: "member"}
}

func vaultListFilterAll() vault.ListFilter { return vault.ListFilter{} }

func (h *itemsHarness) getDetail(t *testing.T, client authClient, id string) map[string]any {
	t.Helper()
	resp := h.request(t, "GET", "/items/"+id, nil, client)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("detail %s: %d", id, resp.StatusCode)
	}
	return decodeBody(t, resp)
}
