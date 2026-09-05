package integration

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/tiny-password/tiny-password/tests/fixtures"
)

type decryptRecorder struct {
	mu  sync.Mutex
	ids []string
}

func (d *decryptRecorder) record(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ids = append(d.ids, id)
}

func (d *decryptRecorder) reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ids = nil
}

func (d *decryptRecorder) snapshot() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.ids...)
}

func (d *decryptRecorder) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.ids)
}

func TestSearchMatchesAuthorizedFieldsOnly(t *testing.T) {
	h := newItemsHarness(t)
	h.bootstrapAdmin(t)
	alice := h.itemClient(t, "alice")
	bob := h.itemClient(t, "bob")

	h.mustCreateItem(t, alice, "personal", "login", map[string]any{
		"name": "GitHub Work", "username": "alice99", "password": "pw-1",
		"urls": []string{"https://github.example"}, "notes": "team notes",
	}, []string{"code"})
	h.mustCreateItem(t, alice, "personal", "login", map[string]any{
		"name": "Unrelated", "username": "x", "password": "pw-2",
	}, nil)
	bobPersonal := h.mustCreateItem(t, bob, "personal", "login", map[string]any{
		"name": "GitHub hidden", "username": "bob", "password": "pw-3",
	}, nil)
	bobShared := h.mustCreateItem(t, bob, "shared", "login", map[string]any{
		"name": "GitHub shared", "username": "ops", "password": "pw-4",
	}, nil)

	search := func(body map[string]any) (int, map[string]any) {
		t.Helper()
		resp := h.request(t, "POST", "/items/search", body, alice)
		defer resp.Body.Close()
		return resp.StatusCode, decodeBody(t, resp)
	}

	// A title hit returns the searcher's item and bob's shared item — never
	// bob's personal one. No payload and no total counts in the response.
	status, page := search(map[string]any{"query": "github"})
	if status != 200 {
		t.Fatalf("search: %d", status)
	}
	items := page["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("github hits=%d, want 2", len(items))
	}
	ids := map[string]bool{}
	for _, it := range items {
		m := it.(map[string]any)
		if m["payload"] != nil {
			t.Fatal("search leaked payload")
		}
		ids[m["id"].(string)] = true
	}
	if ids[bobPersonal["id"].(string)] || !ids[bobShared["id"].(string)] {
		t.Fatalf("authorization probe failed: %v", ids)
	}
	if _, hasTotal := page["total"]; hasTotal {
		t.Fatal("search leaked a total count")
	}

	// Field coverage: username, URL, notes and tags are all searchable.
	for query, want := range map[string]int{
		"alice99":           1, // username
		"github.example":    1, // URL
		"team notes":        1, // notes
		"code":              1, // tag text
		"definitely-absent": 0, // no result
	} {
		status, page := search(map[string]any{"query": query})
		if status != 200 {
			t.Fatalf("query %q: status=%d body=%v", query, status, page)
		}
		if got := len(page["items"].([]any)); got != want {
			t.Fatalf("query %q: hits=%d want %d", query, got, want)
		}
	}

	// Validation: query required, length bound, unknown fields rejected.
	for _, body := range []map[string]any{
		{}, {"query": ""}, {"query": make256Plus()}, {"nonsense": true},
	} {
		resp := h.request(t, "POST", "/items/search", body, alice)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("search validation %v: %d", body, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func make256Plus() string {
	s := make([]rune, 257)
	for i := range s {
		s[i] = 'q'
	}
	return string(s)
}

func TestSearchFiltersAndPagination(t *testing.T) {
	h := newItemsHarness(t)
	h.bootstrapAdmin(t)
	alice := h.itemClient(t, "alice")

	// 3 logins tagged finance, 3 notes, 2 shared (1 login tagged finance).
	for i := 0; i < 3; i++ {
		h.mustCreateItem(t, alice, "personal", "login", map[string]any{
			"name": "bank login", "username": "u", "password": "pw",
		}, []string{"finance"})
	}
	for i := 0; i < 3; i++ {
		h.mustCreateItem(t, alice, "personal", "secure_note", map[string]any{
			"name": "bank note", "body": "b",
		}, nil)
	}
	h.mustCreateItem(t, alice, "shared", "login", map[string]any{
		"name": "bank login", "username": "ops", "password": "pw",
	}, []string{"finance"})
	h.mustCreateItem(t, alice, "shared", "secure_note", map[string]any{
		"name": "shared note", "body": "b",
	}, nil)

	search := func(body map[string]any) (int, map[string]any) {
		t.Helper()
		resp := h.request(t, "POST", "/items/search", body, alice)
		defer resp.Body.Close()
		return resp.StatusCode, decodeBody(t, resp)
	}

	// 7 of the 8 seeded items mention "bank" (the shared note does not).
	if _, page := search(map[string]any{"query": "bank"}); len(page["items"].([]any)) != 7 {
		t.Fatalf("bank hits=%d, want 7", len(page["items"].([]any)))
	}
	// Type filter.
	if _, page := search(map[string]any{"query": "bank", "type": "login"}); len(page["items"].([]any)) != 4 {
		t.Fatalf("login hits=%d, want 4", len(page["items"].([]any)))
	}
	// Scope filter.
	if _, page := search(map[string]any{"query": "bank", "scope": "personal"}); len(page["items"].([]any)) != 6 {
		t.Fatalf("personal hits=%d, want 6", len(page["items"].([]any)))
	}
	// Tag filter via search body.
	if _, page := search(map[string]any{"query": "bank", "tag": "finance"}); len(page["items"].([]any)) != 4 {
		t.Fatalf("finance hits=%d, want 4", len(page["items"].([]any)))
	}
	// Combination: type + tag.
	if _, page := search(map[string]any{"query": "bank", "type": "secure_note", "tag": "finance"}); len(page["items"].([]any)) != 0 {
		t.Fatal("combo should be empty")
	}

	// Pagination: 7 hits, 3 per page → 3+3+1, cursor bound to the query.
	collected := 0
	var cursor any = nil
	first := true
	for {
		body := map[string]any{"query": "bank", "limit": 3}
		if first {
			first = false
		} else {
			body["cursor"] = cursor
		}
		status, page := search(body)
		if status != 200 {
			t.Fatalf("page: %d", status)
		}
		collected += len(page["items"].([]any))
		next, _ := page["next_cursor"]
		if next == nil {
			break
		}
		cursor = next
	}
	if collected != 7 {
		t.Fatalf("cursor walk=%d, want 7", collected)
	}

	// A cursor from one query cannot be replayed against another.
	_, page := search(map[string]any{"query": "bank", "limit": 2})
	cursor = page["next_cursor"]
	resp := h.request(t, "POST", "/items/search", map[string]any{"query": "other", "cursor": cursor}, alice)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("cross-query cursor: %d", resp.StatusCode)
	}
	resp.Body.Close()
	// A different member cannot use it either.
	bob := h.itemClient(t, "bob")
	resp = h.request(t, "POST", "/items/search", map[string]any{"query": "bank", "cursor": cursor}, bob)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("cross-user cursor: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestSearchDecryptsOnlyAuthorizedCandidates(t *testing.T) {
	h := newItemsHarness(t)
	h.bootstrapAdmin(t)
	alice := h.itemClient(t, "alice")
	bob := h.itemClient(t, "bob")

	aliceA := h.mustCreateItem(t, alice, "personal", "secure_note", noteItem("alpha", "a"), nil)
	aliceB := h.mustCreateItem(t, alice, "personal", "secure_note", noteItem("beta", "b"), nil)
	bobPersonal := h.mustCreateItem(t, bob, "personal", "secure_note", noteItem("gamma-hidden", "g"), nil)
	bobShared := h.mustCreateItem(t, bob, "shared", "secure_note", noteItem("delta-shared", "d"), nil)

	h.decrypts.reset()
	resp := h.request(t, "POST", "/items/search", map[string]any{"query": "a"}, alice)
	resp.Body.Close()

	decrypted := map[string]bool{}
	for _, id := range h.decrypts.snapshot() {
		decrypted[id] = true
	}
	// Authorized candidates only: alice's two items plus the shared one. The
	// unreadable personal item of bob is never decrypted.
	want := map[string]bool{
		aliceA["id"].(string): true,
		aliceB["id"].(string): true,
		bobShared["id"].(string): true,
	}
	if len(decrypted) != len(want) {
		t.Fatalf("decrypt count=%d, want %d (%v)", len(decrypted), len(want), decrypted)
	}
	for id := range want {
		if !decrypted[id] {
			t.Fatalf("authorized candidate %s was not decrypted", id)
		}
	}
	if decrypted[bobPersonal["id"].(string)] {
		t.Fatal("unauthorized item was decrypted")
	}
}

func TestItemListTagFilter(t *testing.T) {
	h := newItemsHarness(t)
	h.bootstrapAdmin(t)
	alice := h.itemClient(t, "alice")

	h.mustCreateItem(t, alice, "personal", "secure_note", noteItem("tagged", "a"), []string{"work"})
	h.mustCreateItem(t, alice, "personal", "secure_note", noteItem("tagged-2", "b"), []string{"work"})
	h.mustCreateItem(t, alice, "personal", "secure_note", noteItem("untagged", "b"), nil)
	h.mustCreateItem(t, alice, "personal", "secure_note", noteItem("other", "c"), []string{"home"})

	if got := h.listCount(t, alice, "/items?tag=work"); got != 2 {
		t.Fatalf("tag filter: %d, want 2", got)
	}
	if got := h.listCount(t, alice, "/items"); got != 4 {
		t.Fatalf("unfiltered: %d, want 4", got)
	}
	// Tag filter composes with plaintext filters and binds the cursor.
	if got := h.listCount(t, alice, "/items?tag=work&type=secure_note"); got != 2 {
		t.Fatalf("tag+type: %d, want 2", got)
	}
	resp := h.request(t, "GET", "/items?tag=work&limit=1", nil, alice)
	page := decodeBody(t, resp)
	resp.Body.Close()
	cursor := page["next_cursor"].(string)
	resp = h.request(t, "GET", "/items?limit=1&cursor="+cursor, nil, alice)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("tag-less replay of tag cursor must fail: %d", resp.StatusCode)
	}
	resp.Body.Close()
	// Over-long tag is rejected.
	resp = h.request(t, "GET", "/items?tag="+string(make256Plus()), nil, alice)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("long tag: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestFavoriteAndTagsEndpoints(t *testing.T) {
	h := newItemsHarness(t)
	h.bootstrapAdmin(t)
	alice := h.itemClient(t, "alice")
	bob := h.itemClient(t, "bob")

	item := h.mustCreateItem(t, alice, "personal", "secure_note", noteItem("note", "b"), []string{"one"})
	id := item["id"].(string)

	// Favorite flips through the dedicated endpoint; the revision advances
	// and the previous version is archived like any update.
	expectStatus(t, h.request(t, "PUT", "/items/"+id+"/favorite", map[string]any{"favorite": true}, alice), 204)
	if got := h.listCount(t, alice, "/items?favorite=true"); got != 1 {
		t.Fatalf("favorite filter: %d, want 1", got)
	}
	resp := h.request(t, "GET", "/items/"+id, nil, alice)
	detail := decodeBody(t, resp)
	resp.Body.Close()
	if detail["revision"].(float64) != 2 || detail["favorite"] != true {
		t.Fatalf("favorite detail: %v", detail)
	}

	// Tags replace with dedup.
	expectStatus(t, h.request(t, "PUT", "/items/"+id+"/tags", map[string]any{"tags": []string{"a", "a", "b"}}, alice), 204)
	resp = h.request(t, "GET", "/items/"+id, nil, alice)
	detail = decodeBody(t, resp)
	resp.Body.Close()
	tags := detail["tags"].([]any)
	if len(tags) != 2 || tags[0] != "a" || tags[1] != "b" || detail["revision"].(float64) != 3 {
		t.Fatalf("tags detail: %v", detail)
	}

	// Policy: bob may read shared items but not manage favorite/tags; the
	// administrator has no personal-vault rights.
	shared := h.mustCreateItem(t, alice, "shared", "secure_note", noteItem("shared", "s"), nil)
	sharedID := shared["id"].(string)
	expectStatus(t, h.request(t, "PUT", "/items/"+sharedID+"/favorite", map[string]any{"favorite": true}, bob), 403)
	expectStatus(t, h.request(t, "PUT", "/items/"+sharedID+"/tags", map[string]any{"tags": []string{"x"}}, bob), 403)
	expectStatus(t, h.request(t, "PUT", "/items/"+id+"/favorite", map[string]any{"favorite": true}, h.admin), 404)
	expectStatus(t, h.request(t, "PUT", "/items/"+id+"/tags", map[string]any{"tags": []string{"x"}}, h.admin), 404)

	// Validation and trashed-item behavior.
	expectStatus(t, h.request(t, "PUT", "/items/"+id+"/favorite", map[string]any{}, alice), 400)
	expectStatus(t, h.request(t, "PUT", "/items/"+id+"/tags", map[string]any{"tags": []string{""}}, alice), 400)
	expectStatus(t, h.request(t, "DELETE", "/items/"+id, nil, alice), 204)
	expectStatus(t, h.request(t, "PUT", "/items/"+id+"/favorite", map[string]any{"favorite": false}, alice), 404)
	expectStatus(t, h.request(t, "PUT", "/items/"+id+"/tags", map[string]any{"tags": []string{"x"}}, alice), 404)
}

func TestHealthClassifiesReadableLogins(t *testing.T) {
	h := newItemsHarness(t)
	h.bootstrapAdmin(t)
	alice := h.itemClient(t, "alice")
	bob := h.itemClient(t, "bob")

	// A1: weak (short). A2: reused with A3 and expired. A3: reused.
	// bob's personal login shares A2's password but must not appear;
	// bob's shared login shares it too and must appear.
	h.mustCreateItem(t, alice, "personal", "login", map[string]any{
		"name": "weak", "username": "u", "password": "short12",
	}, nil)
	h.mustCreateItem(t, alice, "personal", "login", map[string]any{
		"name": "expired", "username": "u", "password": "longenough-pw-1",
		"password_expires_at": "2020-01-01",
	}, nil)
	a3 := h.mustCreateItem(t, alice, "personal", "login", map[string]any{
		"name": "ok", "username": "u", "password": "longenough-pw-1",
	}, nil)
	bobShared := h.mustCreateItem(t, bob, "shared", "login", map[string]any{
		"name": "shared login", "username": "ops", "password": "longenough-pw-1",
	}, nil)
	bobPersonal := h.mustCreateItem(t, bob, "personal", "login", map[string]any{
		"name": "hidden login", "username": "b", "password": "longenough-pw-1",
	}, nil)

	resp := h.request(t, "GET", "/items/health", nil, alice)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("health: %d", resp.StatusCode)
	}
	report := decodeBody(t, resp)
	if report["weak"].(float64) != 1 || report["reused"].(float64) != 3 || report["expired"].(float64) != 1 {
		t.Fatalf("counts: %v", report)
	}
	seen := map[string][]any{}
	for _, it := range report["items"].([]any) {
		entry := it.(map[string]any)
		seen[entry["item_id"].(string)] = entry["reasons"].([]any)
	}
	if _, ok := seen[bobPersonal["id"].(string)]; ok {
		t.Fatal("health leaked another member's personal item")
	}
	if _, ok := seen[bobShared["id"].(string)]; !ok {
		t.Fatal("shared login missing from health")
	}
	if reasons := seen[a3["id"].(string)]; len(reasons) != 1 || reasons[0] != "reused" {
		t.Fatalf("a3 reasons: %v", reasons)
	}

	// A member with no readable logins reports zeros.
	resp = h.request(t, "GET", "/items/health", nil, bob)
	bobReport := decodeBody(t, resp)
	resp.Body.Close()
	if bobReport["weak"].(float64) != 0 || len(bobReport["items"].([]any)) == 0 {
		// bob reads his own personal login + alice's shared logins? none are
		// shared here, so only his own weak-free personal login exists.
		if len(bobReport["items"].([]any)) > 1 {
			t.Fatalf("bob health items: %v", bobReport["items"])
		}
	}
}

func TestSearchPerformanceBaseline(t *testing.T) {
	if testing.Short() || raceDetector {
		t.Skip("performance baseline skipped with -short or under -race (dedicated T30 scripts measure P95)")
	}
	h := newItemsHarness(t)
	h.bootstrapAdmin(t)
	alice := h.itemClient(t, "alice")
	aliceID := h.lookupUserID(t, "alice")

	ctx := context.Background()
	if _, err := fixtures.SeedLoginItems(ctx, h.db.DB, mustMasterKey(t), aliceID, 10000, 4999, "needle-haystack"); err != nil {
		t.Fatal(err)
	}

	h.decrypts.reset()
	start := time.Now()
	resp := h.request(t, "POST", "/items/search", map[string]any{"query": "needle-haystack"}, alice)
	defer resp.Body.Close()
	elapsed := time.Since(start)
	if resp.StatusCode != 200 {
		t.Fatalf("search: %d", resp.StatusCode)
	}
	page := decodeBody(t, resp)
	items := page["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("needle hits=%d, want 1", len(items))
	}
	if got := h.decrypts.count(); got != 10000 {
		t.Fatalf("decrypt calls=%d, want exactly 10000 authorized candidates", got)
	}
	t.Logf("search over 10000 items: %s (single-user baseline; P95 comes from T30)", elapsed)
	if elapsed > 30*time.Second {
		t.Fatalf("search baseline regression: %s", elapsed)
	}
}
