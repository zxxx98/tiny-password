package integration

import (
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestBrowseSortsFiltersAndPaginatesAuthorizedMetadata(t *testing.T) {
	h := newItemsHarness(t)
	h.bootstrapAdmin(t)
	alice, bob := h.itemClient(t, "alice"), h.itemClient(t, "bob")
	create := func(client authClient, scope, name string, tags []string) map[string]any {
		return h.mustCreateItem(t, client, scope, "login", map[string]any{"name": name, "username": "account", "password": "short-secret"}, tags)
	}
	a := create(alice, "personal", "Alpha", []string{"work"})
	b := create(alice, "personal", "beta", []string{"home", "work"})
	c := create(bob, "shared", "Gamma", []string{"home"})
	hidden := create(bob, "personal", "Hidden", []string{"hidden-tag"})
	fav := h.request(t, "PUT", "/items/"+b["id"].(string)+"/favorite", map[string]any{"favorite": true}, alice)
	if fav.StatusCode != http.StatusNoContent {
		t.Fatalf("favorite: %d", fav.StatusCode)
	}
	fav.Body.Close()
	browse := func(body map[string]any, wantStatus int) map[string]any {
		t.Helper()
		resp := h.request(t, "POST", "/items/browse", body, alice)
		defer resp.Body.Close()
		page := decodeBody(t, resp)
		if resp.StatusCode != wantStatus {
			t.Fatalf("browse status=%d body=%v", resp.StatusCode, page)
		}
		return page
	}
	// Available tags come from a separate authorized scan, not every page.
	tagResp := h.request(t, "POST", "/items/browse/tags", map[string]any{}, alice)
	tagPage := decodeBody(t, tagResp)
	tagResp.Body.Close()
	if tagResp.StatusCode != 200 || !reflect.DeepEqual(tagPage["tags"], []any{"home", "work"}) {
		t.Fatalf("tag options: status=%d body=%v", tagResp.StatusCode, tagPage)
	}
	for _, order := range []string{"title_asc", "title_desc", "updated_desc", "updated_asc", "created_desc"} {
		t.Run(order, func(t *testing.T) {
			seen := map[string]bool{}
			cursor := ""
			titles := []string{}
			for i := 0; i < 4; i++ {
				page := browse(map[string]any{"sort": order, "limit": 1, "cursor": cursor}, 200)
				for _, raw := range page["items"].([]any) {
					item := raw.(map[string]any)
					id := item["id"].(string)
					if seen[id] || id == hidden["id"] {
						t.Fatalf("duplicate or unauthorized item: %v", item)
					}
					if _, ok := item["payload"]; ok {
						t.Fatal("browse must not return credential payloads")
					}
					seen[id] = true
					titles = append(titles, item["title"].(string))
				}
				for _, tag := range tagPage["tags"].([]any) {
					if tag == "hidden-tag" {
						t.Fatal("unauthorized tag disclosed")
					}
				}
				if page["next_cursor"] == nil {
					break
				}
				cursor = page["next_cursor"].(string)
			}
			if len(seen) != 3 || !seen[a["id"].(string)] || !seen[b["id"].(string)] || !seen[c["id"].(string)] {
				t.Fatalf("incomplete pages: %v", seen)
			}
			if order == "title_asc" && !reflect.DeepEqual(titles, []string{"Alpha", "beta", "Gamma"}) {
				t.Fatalf("title order: %v", titles)
			}
			if order == "title_desc" && !reflect.DeepEqual(titles, []string{"Gamma", "beta", "Alpha"}) {
				t.Fatalf("title order: %v", titles)
			}
		})
	}
	page := browse(map[string]any{"query": "account", "tag": "work", "favorite": true, "scope": "personal", "type": "login", "sort": "title_asc"}, 200)
	items := page["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != b["id"] {
		t.Fatalf("combined filters: %v", page)
	}
	first := browse(map[string]any{"sort": "title_asc", "limit": 1}, 200)
	browse(map[string]any{"sort": "title_desc", "cursor": first["next_cursor"]}, 400)
	browse(map[string]any{"sort": "title_asc", "tag": "work", "cursor": first["next_cursor"]}, 400)
	browse(map[string]any{"sort": "invalid"}, 400)
	browse(map[string]any{"limit": 101}, 400)
	resp := h.request(t, "POST", "/items/browse", map[string]any{"sort": "title_asc", "cursor": first["next_cursor"]}, bob)
	if resp.StatusCode != 400 {
		t.Fatalf("foreign cursor accepted: %d", resp.StatusCode)
	}
	resp.Body.Close()
	healthResp := h.request(t, "GET", "/items/health", nil, alice)
	report := decodeBody(t, healthResp)
	healthResp.Body.Close()
	for _, raw := range report["items"].([]any) {
		finding := raw.(map[string]any)
		if finding["title"] == nil || finding["item_id"] == hidden["id"] {
			t.Fatalf("invalid health finding: %v", finding)
		}
	}
	// Names and filters near their Unicode limits still produce usable cursors.
	longName, longTag := strings.Repeat("密", 256), strings.Repeat("签", 64)
	create(alice, "personal", longName, []string{longTag})
	create(alice, "personal", longName, []string{longTag})
	longFirst := browse(map[string]any{"query": longName, "tag": longTag, "sort": "title_asc", "limit": 1}, 200)
	longCursor := longFirst["next_cursor"].(string)
	if len(longCursor) > 4096 {
		t.Fatalf("generated cursor too large: %d", len(longCursor))
	}
	longNext := browse(map[string]any{"query": longName, "tag": longTag, "sort": "title_asc", "limit": 1, "cursor": longCursor}, 200)
	if len(longNext["items"].([]any)) != 1 || longNext["next_cursor"] != nil {
		t.Fatalf("long-title pagination: %v", longNext)
	}
}

func TestBrowsePageSizedDecryptionAndGlobalOrdering(t *testing.T) {
	h := newItemsHarness(t)
	h.bootstrapAdmin(t)
	alice, bob := h.itemClient(t, "alice"), h.itemClient(t, "bob")
	type expectedItem struct{ id, title, updated, created string }
	expected := []expectedItem{}
	for i := 0; i < 120; i++ {
		name := fmt.Sprintf("Name %03d", 119-i)
		if i%5 == 0 {
			name = strings.Repeat("密", 256)
		}
		if i%7 == 0 {
			name = "equal NAME"
		}
		item := h.mustCreateItem(t, alice, "personal", "login", map[string]any{"name": name, "username": "matching-account", "password": "secret"}, []string{"work"})
		id := item["id"].(string)
		updated := fmt.Sprintf("2026-09-%02dT00:00:00Z", 1+i%3)
		created := fmt.Sprintf("2026-08-%02dT00:00:00Z", 1+i%4)
		if _, err := h.db.Exec(`UPDATE vault_items SET updated_at=?, created_at=? WHERE id=?`, updated, created, id); err != nil {
			t.Fatal(err)
		}
		expected = append(expected, expectedItem{id, name, updated, created})
	}
	hidden := h.mustCreateItem(t, bob, "personal", "login", map[string]any{"name": "hidden", "password": "hidden-secret"}, []string{"hidden-tag"})
	// A corrupt unauthorized row proves policy filtering precedes decryption.
	if _, err := h.db.Exec(`UPDATE vault_items SET ciphertext=? WHERE id=?`, []byte{1}, hidden["id"]); err != nil {
		t.Fatal(err)
	}
	browse := func(body map[string]any) map[string]any {
		t.Helper()
		resp := h.request(t, "POST", "/items/browse", body, alice)
		defer resp.Body.Close()
		page := decodeBody(t, resp)
		if resp.StatusCode != 200 {
			t.Fatalf("browse: %d %v", resp.StatusCode, page)
		}
		if _, exists := page["tags"]; exists {
			t.Fatal("page must not independently scan tag options")
		}
		return page
	}
	for _, order := range []string{"", "updated_desc", "updated_asc", "created_desc", "title_asc", "title_desc"} {
		t.Run(order, func(t *testing.T) {
			sorted := append([]expectedItem{}, expected...)
			key := func(item expectedItem) string {
				switch order {
				case "title_asc", "title_desc":
					return strings.ToLower(item.title)
				case "created_desc":
					return item.created
				default:
					return item.updated
				}
			}
			sort.Slice(sorted, func(i, j int) bool {
				a, b := key(sorted[i]), key(sorted[j])
				if a == b {
					return sorted[i].id < sorted[j].id
				}
				if order == "title_asc" || order == "updated_asc" {
					return a < b
				}
				return a > b
			})
			byTitle := strings.HasPrefix(order, "title_")
			h.decrypts.reset()
			first := browse(map[string]any{"sort": order, "limit": 1})
			wantDecrypts := 2
			if byTitle {
				wantDecrypts = 120
			}
			if got := h.decrypts.count(); got != wantDecrypts {
				t.Fatalf("first page decrypts=%d want=%d", got, wantDecrypts)
			}
			if first["items"].([]any)[0].(map[string]any)["id"] != sorted[0].id {
				t.Fatal("wrong first item")
			}
			var cursor any = ""
			seen := []string{}
			for pageIndex := 0; pageIndex < 18; pageIndex++ {
				h.decrypts.reset()
				page := browse(map[string]any{"sort": order, "limit": 7, "cursor": cursor})
				for _, raw := range page["items"].([]any) {
					item := raw.(map[string]any)
					if _, ok := item["payload"]; ok {
						t.Fatal("credential payload disclosed")
					}
					seen = append(seen, item["id"].(string))
				}
				if !byTitle && h.decrypts.count() > 8 {
					t.Fatalf("page decrypted %d candidates", h.decrypts.count())
				}
				for _, id := range h.decrypts.snapshot() {
					if id == hidden["id"] {
						t.Fatal("unauthorized decryption")
					}
				}
				if page["next_cursor"] == nil {
					break
				}
				cursor = page["next_cursor"]
			}
			wantIDs := []string{}
			for _, item := range sorted {
				wantIDs = append(wantIDs, item.id)
			}
			if !reflect.DeepEqual(seen, wantIDs) {
				t.Fatalf("cross-page order/integrity: got %v want %v", seen, wantIDs)
			}

			wantFiltered := []string{}
			for _, item := range sorted {
				if strings.Contains(strings.ToLower(item.title), "name") {
					wantFiltered = append(wantFiltered, item.id)
				}
			}
			seenFiltered := []string{}
			cursor = ""
			for pageIndex := 0; pageIndex < 18; pageIndex++ {
				page := browse(map[string]any{"sort": order, "query": "name", "tag": "work", "scope": "personal", "type": "login", "favorite": false, "limit": 7, "cursor": cursor})
				for _, raw := range page["items"].([]any) {
					seenFiltered = append(seenFiltered, raw.(map[string]any)["id"].(string))
				}
				if page["next_cursor"] == nil {
					break
				}
				cursor = page["next_cursor"]
			}
			if !reflect.DeepEqual(seenFiltered, wantFiltered) {
				t.Fatalf("combined-filter cross-page order/integrity: got %v want %v", seenFiltered, wantFiltered)
			}
		})
	}
	// Filtered timestamp scans also stop at the page boundary, with global order.
	h.decrypts.reset()
	filtered := browse(map[string]any{"query": "matching", "tag": "work", "scope": "personal", "type": "login", "sort": "updated_asc", "limit": 1})
	if len(filtered["items"].([]any)) != 1 || h.decrypts.count() != 2 {
		t.Fatalf("filtered timestamp page: %v decrypts=%d", filtered, h.decrypts.count())
	}
	h.decrypts.reset()
	tagResp := h.request(t, "POST", "/items/browse/tags", map[string]any{"scope": "personal", "type": "login"}, alice)
	tags := decodeBody(t, tagResp)
	tagResp.Body.Close()
	if tagResp.StatusCode != 200 || !reflect.DeepEqual(tags["tags"], []any{"work"}) || h.decrypts.count() != 120 {
		t.Fatalf("independent tags: %v decrypts=%d", tags, h.decrypts.count())
	}
	h.decrypts.reset()
	emptyTagsResp := h.request(t, "POST", "/items/browse/tags", map[string]any{"favorite": true}, alice)
	emptyTags := decodeBody(t, emptyTagsResp)
	emptyTagsResp.Body.Close()
	if emptyTagsResp.StatusCode != 200 || len(emptyTags["tags"].([]any)) != 0 || h.decrypts.count() != 0 {
		t.Fatalf("tag SQL filters: %v decrypts=%d", emptyTags, h.decrypts.count())
	}
}
