package integration

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tiny-password/tiny-password/internal/audit"
	"github.com/tiny-password/tiny-password/internal/platform/crypto"
)

// noteItem is a minimal valid secure_note payload.
func noteItem(name, body string) map[string]any {
	return map[string]any{"name": name, "body": body}
}

func (h *itemsHarness) updateNote(t *testing.T, client authClient, id string, revision int, body string) map[string]any {
	t.Helper()
	resp := h.request(t, "PUT", "/items/"+id, map[string]any{
		"revision": revision,
		"payload":  noteItem("note", body),
	}, client)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("update to rev %d: %d", revision, resp.StatusCode)
	}
	return decodeBody(t, resp)
}

func historyRevisions(t *testing.T, h *itemsHarness, id string) []int {
	t.Helper()
	rows, err := h.db.Query(`SELECT revision FROM item_versions WHERE item_id=? ORDER BY revision`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var r int
		if err := rows.Scan(&r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func TestHistoryRetentionKeepsLast10(t *testing.T) {
	h := newItemsHarness(t)
	h.bootstrapAdmin(t)
	alice := h.itemClient(t, "alice")

	item := h.mustCreateItem(t, alice, "personal", "secure_note", noteItem("note", "v0"), nil)
	id := item["id"].(string)
	// 12 effective updates on top of revision 1 → history holds 3..12,
	// current revision is 13.
	for i := 1; i <= 12; i++ {
		h.updateNote(t, alice, id, i, "v"+string(rune('0'+i%10))+string(rune('0'+i/10)))
	}
	var current int
	if err := h.db.QueryRow(`SELECT revision FROM vault_items WHERE id=?`, id).Scan(&current); err != nil {
		t.Fatal(err)
	}
	if current != 13 {
		t.Fatalf("current revision=%d, want 13", current)
	}
	revisions := historyRevisions(t, h, id)
	if len(revisions) != 10 || revisions[0] != 3 || revisions[len(revisions)-1] != 12 {
		t.Fatalf("history revisions=%v, want [3..12]", revisions)
	}
}

func TestHistoryListAndCrossMemberAccess(t *testing.T) {
	h := newItemsHarness(t)
	h.bootstrapAdmin(t)
	alice := h.itemClient(t, "alice")
	bob := h.itemClient(t, "bob")

	item := h.mustCreateItem(t, alice, "personal", "secure_note", noteItem("note", "v0"), nil)
	id := item["id"].(string)
	for rev, body := range map[int]string{1: "v1", 2: "v2", 3: "v3"} {
		h.updateNote(t, alice, id, rev, body)
	}

	// Creator lists history newest-first; entries carry revision + updated_at.
	resp := h.request(t, "GET", "/items/"+id+"/history", nil, alice)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("history list: %d", resp.StatusCode)
	}
	page := decodeBody(t, resp)
	entries := page["items"].([]any)
	if len(entries) != 3 {
		t.Fatalf("history entries=%d, want 3", len(entries))
	}
	first := entries[0].(map[string]any)
	if first["revision"].(float64) != 3 || first["updated_at"] == nil {
		t.Fatalf("newest entry wrong: %v", first)
	}
	if entries[2].(map[string]any)["revision"].(float64) != 1 {
		t.Fatalf("oldest entry wrong: %v", entries[2])
	}

	// Unreadable items: history is indistinguishable from unknown.
	for _, client := range []authClient{bob, h.admin} {
		resp := h.request(t, "GET", "/items/"+id+"/history", nil, client)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("cross-member history: %d, want 404", resp.StatusCode)
		}
		resp.Body.Close()
	}

	// Shared items: every member reads history; only the creator restores.
	shared := h.mustCreateItem(t, alice, "shared", "secure_note", noteItem("shared", "s0"), nil)
	sharedID := shared["id"].(string)
	h.updateNote(t, alice, sharedID, 1, "s1")
	expectStatus(t, h.request(t, "GET", "/items/"+sharedID+"/history", nil, bob), 200)
	restore := map[string]any{}
	resp = h.request(t, "POST", "/items/"+sharedID+"/history/1/restore", restore, bob)
	out := decodeBody(t, resp)
	if resp.StatusCode != http.StatusForbidden || out["code"] != "FORBIDDEN" {
		t.Fatalf("non-creator history restore: %d %v", resp.StatusCode, out["code"])
	}
	expectStatus(t, h.request(t, "POST", "/items/"+sharedID+"/history/1/restore", restore, alice), 200)
}

func TestHistoryRestoreCreatesNewRevision(t *testing.T) {
	h := newItemsHarness(t)
	h.bootstrapAdmin(t)
	alice := h.itemClient(t, "alice")

	bodies := map[int]string{0: "SYNSECRET-v0", 1: "v1", 2: "v2", 3: "v3"}
	item := h.mustCreateItem(t, alice, "personal", "secure_note", noteItem("note", bodies[0]), nil)
	id := item["id"].(string)
	for rev := 1; rev <= 3; rev++ {
		h.updateNote(t, alice, id, rev, bodies[rev])
	}

	// Restore revision 1: the archive gains revision 4 and the restored
	// content becomes current revision 5.
	resp := h.request(t, "POST", "/items/"+id+"/history/1/restore", map[string]any{}, alice)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("restore: %d", resp.StatusCode)
	}
	detail := decodeBody(t, resp)
	if detail["revision"].(float64) != 5 {
		t.Fatalf("revision after restore: %v", detail["revision"])
	}
	if detail["payload"].(map[string]any)["body"] != bodies[0] {
		t.Fatalf("restored payload wrong: %v", detail["payload"])
	}
	// Restoring archived the previous current (4): history = {1,2,3,4}.
	if got := historyRevisions(t, h, id); len(got) != 4 || got[3] != 4 {
		t.Fatalf("history after restore: %v", got)
	}

	// The restored version is re-encrypted, never a copy of the old ciphertext:
	// same plaintext, fresh nonce and fresh AAD revision binding.
	key := mustMasterKey(t)
	var curNonce, curCt []byte
	if err := h.db.QueryRow(`SELECT nonce, ciphertext FROM vault_items WHERE id=?`, id).Scan(&curNonce, &curCt); err != nil {
		t.Fatal(err)
	}
	aad5 := crypto.AAD{ItemID: id, Scope: "personal", SubjectID: h.lookupUserID(t, "alice"), PayloadVersion: 1, Revision: 5}
	plain, err := key.DecryptColumns(1, curNonce, curCt, aad5)
	if err != nil || !strings.Contains(string(plain), bodies[0]) {
		t.Fatalf("current revision not decryptable under new AAD: %v", err)
	}

	// History restore can itself be rolled back again (restore revision 2).
	resp = h.request(t, "POST", "/items/"+id+"/history/2/restore", map[string]any{}, alice)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("second restore: %d", resp.StatusCode)
	}
	if got := decodeBody(t, resp); got["revision"].(float64) != 6 ||
		got["payload"].(map[string]any)["body"] != bodies[1] {
		t.Fatalf("second restore detail: %v", got)
	}

	// Unknown history revision: 404, nothing written.
	resp = h.request(t, "POST", "/items/"+id+"/history/99/restore", map[string]any{}, alice)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown revision restore: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestHistoryTamperFailsClosed(t *testing.T) {
	h := newItemsHarness(t)
	h.bootstrapAdmin(t)
	alice := h.itemClient(t, "alice")

	item := h.mustCreateItem(t, alice, "personal", "secure_note", noteItem("note", "v0"), nil)
	id := item["id"].(string)
	h.updateNote(t, alice, id, 1, "v1")

	// Corrupt the archived version's ciphertext in place.
	if _, err := h.db.Exec(`UPDATE item_versions SET ciphertext=? WHERE item_id=? AND revision=1`,
		[]byte{9, 9, 9, 9}, id); err != nil {
		t.Fatal(err)
	}
	resp := h.request(t, "POST", "/items/"+id+"/history/1/restore", map[string]any{}, alice)
	defer resp.Body.Close()
	body := decodeBody(t, resp)
	if resp.StatusCode != http.StatusInternalServerError || body["code"] != "INTERNAL" {
		t.Fatalf("tampered restore: %d %v", resp.StatusCode, body["code"])
	}
	// Nothing was written: current revision and history rows unchanged.
	var current int
	if err := h.db.QueryRow(`SELECT revision FROM vault_items WHERE id=?`, id).Scan(&current); err != nil {
		t.Fatal(err)
	}
	if current != 2 || len(historyRevisions(t, h, id)) != 1 {
		t.Fatalf("tampered restore mutated state: rev=%d history=%v", current, historyRevisions(t, h, id))
	}
}

func TestTrashLifecycleAndPolicy(t *testing.T) {
	h := newItemsHarness(t)
	h.bootstrapAdmin(t)
	alice := h.itemClient(t, "alice")
	bob := h.itemClient(t, "bob")

	personal := h.mustCreateItem(t, alice, "personal", "secure_note", noteItem("mine", "SYNSECRET-trash"), nil)
	personalID := personal["id"].(string)
	shared := h.mustCreateItem(t, alice, "shared", "secure_note", noteItem("ours", "s"), nil)
	sharedID := shared["id"].(string)

	// Trash removes the item from list/detail but keeps the rows; the shared
	// item created earlier is unaffected.
	expectStatus(t, h.request(t, "DELETE", "/items/"+personalID, nil, alice), 204)
	expectStatus(t, h.request(t, "GET", "/items/"+personalID, nil, alice), 404)
	if got := h.listCount(t, alice, "/items"); got != 1 {
		t.Fatalf("list after trash: %d, want 1 (shared only)", got)
	}
	var deletedAt any
	if err := h.db.QueryRow(`SELECT deleted_at FROM vault_items WHERE id=?`, personalID).Scan(&deletedAt); err != nil || deletedAt == nil {
		t.Fatalf("deleted_at not set: %v %v", deletedAt, err)
	}

	// The trash listing shows the trashed item with metadata only.
	resp := h.request(t, "GET", "/items/trash", nil, alice)
	page := decodeBody(t, resp)
	resp.Body.Close()
	items := page["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != personalID {
		t.Fatalf("trash list: %v", items)
	}
	if items[0].(map[string]any)["payload"] != nil {
		t.Fatal("trash list leaked payload")
	}
	// Others cannot see the trashed personal item.
	resp = h.request(t, "GET", "/items/trash", nil, bob)
	if got := len(decodeBody(t, resp)["items"].([]any)); got != 0 {
		t.Fatalf("bob trash list: %d, want 0", got)
	}
	resp.Body.Close()

	// Restore puts it back unchanged (no revision bump).
	resp = h.request(t, "POST", "/items/"+personalID+"/restore", nil, alice)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("restore: %d", resp.StatusCode)
	}
	if got := decodeBody(t, resp); got["revision"].(float64) != 1 {
		t.Fatalf("restore bumped revision: %v", got["revision"])
	}
	expectStatus(t, h.request(t, "GET", "/items/"+personalID, nil, alice), 200)

	// Policy: bob may read the shared item but not trash/restore/purge it.
	for _, spec := range []struct{ method, path string }{
		{"DELETE", "/items/" + sharedID},
		{"POST", "/items/" + sharedID + "/restore"},
		{"DELETE", "/items/" + sharedID + "/purge"},
	} {
		expectStatus(t, h.request(t, spec.method, spec.path, nil, bob), 403)
	}
	// The administrator holds no personal-vault privileges.
	for _, spec := range []struct{ method, path string }{
		{"DELETE", "/items/" + personalID},
		{"POST", "/items/" + personalID + "/restore"},
		{"DELETE", "/items/" + personalID + "/purge"},
	} {
		expectStatus(t, h.request(t, spec.method, spec.path, nil, h.admin), 404)
	}

	// Purge before the 30-day deadline removes item and history permanently.
	h.updateNote(t, alice, personalID, 1, "v1")
	expectStatus(t, h.request(t, "DELETE", "/items/"+personalID, nil, alice), 204)
	expectStatus(t, h.request(t, "DELETE", "/items/"+personalID+"/purge", nil, alice), 204)
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM vault_items WHERE id=?`, personalID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("purged item row survived")
	}
	if got := historyRevisions(t, h, personalID); len(got) != 0 {
		t.Fatalf("purged history survived: %v", got)
	}
	expectStatus(t, h.request(t, "GET", "/items/"+personalID, nil, alice), 404)
}

func TestTrashPurgeExpiredAfter30Days(t *testing.T) {
	h := newItemsHarness(t)
	h.bootstrapAdmin(t)
	alice := h.itemClient(t, "alice")

	item := h.mustCreateItem(t, alice, "personal", "secure_note", noteItem("note", "v0"), nil)
	id := item["id"].(string)
	h.updateNote(t, alice, id, 1, "v1")
	expectStatus(t, h.request(t, "DELETE", "/items/"+id, nil, alice), 204)

	ctx := context.Background()
	// 29 days: inside the retention window, nothing is purged.
	h.clock.Advance(29 * 24 * time.Hour)
	removed, err := h.vault.PurgeExpiredTrash(ctx)
	if err != nil || removed != 0 {
		t.Fatalf("purge at 29d: removed=%d err=%v", removed, err)
	}
	// Cross the 30-day deadline: the item and its history are removed.
	h.clock.Advance(2 * 24 * time.Hour)
	removed, err = h.vault.PurgeExpiredTrash(ctx)
	if err != nil || removed != 1 {
		t.Fatalf("purge at 31d: removed=%d err=%v", removed, err)
	}
	if got := historyRevisions(t, h, id); len(got) != 0 {
		t.Fatalf("expired history survived: %v", got)
	}
	// Repeating the run is safe and removes nothing.
	removed, err = h.vault.PurgeExpiredTrash(ctx)
	if err != nil || removed != 0 {
		t.Fatalf("repeat purge: removed=%d err=%v", removed, err)
	}
	// The background purge leaves an audit trail with an anonymous actor.
	var events int
	var actor string
	if err := h.db.QueryRow(`SELECT COUNT(*), COALESCE(MIN(actor_id),'') FROM audit_events WHERE event=? AND target_id=?`,
		audit.EventVaultItemPurged, id).Scan(&events, &actor); err != nil {
		t.Fatal(err)
	}
	if events != 1 || actor != audit.Anonymous {
		t.Fatalf("background purge audit: events=%d actor=%q", events, actor)
	}
}

func TestTrashAuditEvents(t *testing.T) {
	h := newItemsHarness(t)
	h.bootstrapAdmin(t)
	alice := h.itemClient(t, "alice")

	item := h.mustCreateItem(t, alice, "personal", "secure_note", noteItem("note", "v0"), nil)
	id := item["id"].(string)
	expectStatus(t, h.request(t, "DELETE", "/items/"+id, nil, alice), 204)
	expectStatus(t, h.request(t, "POST", "/items/"+id+"/restore", nil, alice), 200)
	expectStatus(t, h.request(t, "DELETE", "/items/"+id, nil, alice), 204)
	expectStatus(t, h.request(t, "DELETE", "/items/"+id+"/purge", nil, alice), 204)

	rows, err := h.db.Query(`SELECT event FROM audit_events WHERE target_id=? AND event LIKE 'vault.%' ORDER BY created_at, id`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var events []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
	}
	want := []string{
		audit.EventVaultItemCreated,
		audit.EventVaultItemTrashed,
		audit.EventVaultItemRestored,
		audit.EventVaultItemTrashed,
		audit.EventVaultItemPurged,
	}
	if len(events) != len(want) {
		t.Fatalf("events=%v want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("event[%d]=%s want %s (all: %v)", i, events[i], want[i], events)
		}
	}
}

func TestHistoryCursorPagination(t *testing.T) {
	h := newItemsHarness(t)
	h.bootstrapAdmin(t)
	alice := h.itemClient(t, "alice")

	item := h.mustCreateItem(t, alice, "personal", "secure_note", noteItem("note", "v0"), nil)
	id := item["id"].(string)
	for rev := 1; rev <= 7; rev++ {
		h.updateNote(t, alice, id, rev, "v"+string(rune('0'+rev)))
	}

	// Page through the 7 history entries two at a time.
	var seen []float64
	cursor := ""
	for {
		path := "/items/" + id + "/history?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		resp := h.request(t, "GET", path, nil, alice)
		if resp.StatusCode != 200 {
			t.Fatalf("history page: %d", resp.StatusCode)
		}
		page := decodeBody(t, resp)
		for _, e := range page["items"].([]any) {
			seen = append(seen, e.(map[string]any)["revision"].(float64))
		}
		next, _ := page["next_cursor"].(string)
		resp.Body.Close()
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seen) != 7 || seen[0] != 7 || seen[6] != 1 {
		t.Fatalf("cursor walk revisions=%v, want 7..1", seen)
	}
	// Strictly descending.
	for i := 1; i < len(seen); i++ {
		if seen[i] >= seen[i-1] {
			t.Fatalf("history order broken: %v", seen)
		}
	}
	// A tampered or foreign cursor is rejected.
	resp := h.request(t, "GET", "/items/"+id+"/history?cursor=abc.def", nil, alice)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad cursor: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

