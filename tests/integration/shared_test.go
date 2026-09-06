package integration

import (
	"net/http"
	"strings"
	"testing"
)

// sharedHarness seeds the plan T17 scenario: admin A, member B (creator of a
// versioned shared login and a shared identity used as an address target)
// and member C (a plain reader).
type sharedHarness struct {
	*itemsHarness
	// admin comes from the embedded itemsHarness (set by bootstrapAdmin);
	// re-declaring it here would shadow the promoted field with a zero value.
	creator    authClient // B
	reader     authClient // C
	sharedID   string     // shared login created by B
	identityID string     // shared identity created by B
	bobID      string
}

func newSharedHarness(t *testing.T) *sharedHarness {
	h := &sharedHarness{itemsHarness: newItemsHarness(t)}
	h.bootstrapAdmin(t)
	h.creator = h.itemClient(t, "bob")
	h.reader = h.itemClient(t, "carol")
	h.bobID = h.lookupUserID(t, "bob")

	shared := h.mustCreateItem(t, h.creator, "shared", "login", map[string]any{
		"name": "team vault", "username": "ops", "password": "SYNSECRET-shared-pw",
	}, []string{"team"})
	h.sharedID = shared["id"].(string)
	identity := h.mustCreateItem(t, h.creator, "shared", "identity", payloadFixtureWithoutReference("identity"), nil)
	h.identityID = identity["id"].(string)

	// One effective update so the shared login carries history to attack.
	update := map[string]any{"revision": 1, "payload": map[string]any{
		"name": "team vault", "username": "ops2", "password": "SYNSECRET-shared-pw2",
	}}
	expectStatus(t, h.request(t, "PUT", "/items/"+h.sharedID, update, h.creator), 200)
	return h
}

// writesAgainst enumerates every write the policy reserves for the creator.
func sharedWritePaths(id string) []struct{ name, method, path string } {
	return []struct{ name, method, path string }{
		{"update", "PUT", "/items/" + id},
		{"favorite", "PUT", "/items/" + id + "/favorite"},
		{"tags", "PUT", "/items/" + id + "/tags"},
		{"trash", "DELETE", "/items/" + id},
		{"restore", "POST", "/items/" + id + "/restore"},
		{"purge", "DELETE", "/items/" + id + "/purge"},
		{"history restore", "POST", "/items/" + id + "/history/1/restore"},
	}
}

func TestSharedReadsWriteMatrix(t *testing.T) {
	h := newSharedHarness(t)

	// Every member (reader C and admin A) reads shared items and history.
	for _, client := range []authClient{h.reader, h.admin} {
		expectStatus(t, h.request(t, "GET", "/items/"+h.sharedID, nil, client), 200)
		expectStatus(t, h.request(t, "GET", "/items/"+h.sharedID+"/history", nil, client), 200)
		if got := h.listCount(t, client, "/items"); got == 0 {
			t.Fatal("shared items missing from reader list")
		}
	}

	// Non-creators: every write is rejected; readable targets answer 403.
	for _, client := range []authClient{h.reader, h.admin} {
		for _, wp := range sharedWritePaths(h.sharedID) {
			var body any
			switch wp.name {
			case "favorite":
				body = map[string]any{"favorite": true}
			case "tags":
				body = map[string]any{"tags": []string{"x"}}
			case "update":
				body = map[string]any{"revision": 2, "payload": map[string]any{"name": "x", "body": "b"}}
			default:
				body = nil
			}
			resp := h.request(t, wp.method, wp.path, body, client)
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("%s %s: %d, want 403", wp.method, wp.path, resp.StatusCode)
			}
			resp.Body.Close()
		}
	}

	// The creator performs the same writes successfully.
	expectStatus(t, h.request(t, "PUT", "/items/"+h.sharedID+"/favorite", map[string]any{"favorite": false}, h.creator), 204)
	expectStatus(t, h.request(t, "PUT", "/items/"+h.sharedID+"/tags", map[string]any{"tags": []string{"team"}}, h.creator), 204)
}

func TestSharedForgedRequests(t *testing.T) {
	h := newSharedHarness(t)

	// Forged ownership fields cannot move the shared item or reassign it.
	forged := map[string]any{
		"revision":    2,
		"payload":     map[string]any{"name": "hijacked", "body": "x"},
		"vault_scope": "personal",
		"owner_id":    h.lookupUserID(t, "carol"),
	}
	resp := h.request(t, "PUT", "/items/"+h.sharedID, forged, h.reader)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("forged ownership update: %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// A reader cannot restore a shared history version.
	expectStatus(t, h.request(t, "POST", "/items/"+h.sharedID+"/history/1/restore", map[string]any{}, h.reader), 403)

	// Address references follow readability + shared→shared compatibility:
	// a shared card by the creator may reference the shared identity, and a
	// reader's shared card may reference it too (readable + compatible);
	// nobody may bridge shared→personal.
	cc := func(ref string) map[string]any {
		p := payloadFixtureWithoutReference("credit_card")
		p["billing_address_item_id"] = ref
		return p
	}
	expectStatus(t, h.request(t, "POST", "/items", itemCreateBody("shared", "credit_card", cc(h.identityID), nil, false), h.creator), 201)
	expectStatus(t, h.request(t, "POST", "/items", itemCreateBody("shared", "credit_card", cc(h.identityID), nil, false), h.reader), 201)
	carolPersonal := h.mustCreateItem(t, h.reader, "personal", "identity", payloadFixtureWithoutReference("identity"), nil)
	resp = h.request(t, "POST", "/items", itemCreateBody("shared", "credit_card", cc(carolPersonal["id"].(string)), nil, false), h.creator)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("shared→personal reference: %d, want 409", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestSharedCreatorDisabledKeepsData(t *testing.T) {
	h := newSharedHarness(t)

	// Admin disables the creator: sessions die, data stays, readers read on.
	expectStatus(t, h.request(t, "POST", "/users/"+h.bobID+"/disable", nil, h.admin), 200)
	expectStatus(t, h.request(t, "GET", "/items/"+h.sharedID, nil, h.creator), 401)
	expectStatus(t, h.request(t, "GET", "/items/"+h.sharedID, nil, h.reader), 200)

	// The disabled creator cannot log back in.
	_, resp := h.login(t, "bob", validPassword)
	expectStatus(t, resp, 403)

	// Re-enable: the creator returns and can write again.
	expectStatus(t, h.request(t, "POST", "/users/"+h.bobID+"/enable", nil, h.admin), 200)
	reEnabled, resp := h.login(t, "bob", validPassword)
	expectStatus(t, resp, 200)
	expectStatus(t, h.request(t, "PUT", "/items/"+h.sharedID+"/favorite", map[string]any{"favorite": true}, reEnabled), 204)
}

func TestSharedCreatorDeletedCascades(t *testing.T) {
	h := newSharedHarness(t)

	del := h.request(t, "DELETE", "/users/"+h.bobID, map[string]string{"confirm_username": "bob"}, h.admin)
	expectStatus(t, del, 204)

	// The creator's shared items are gone for readers too.
	expectStatus(t, h.request(t, "GET", "/items/"+h.sharedID, nil, h.reader), 404)
	expectStatus(t, h.request(t, "GET", "/items/"+h.identityID, nil, h.reader), 404)
	if got := h.listCount(t, h.reader, "/items"); got != 0 {
		t.Fatalf("reader list after creator delete: %d, want 0", got)
	}
	// The reader's own data is untouched.
	carolOwn := h.mustCreateItem(t, h.reader, "personal", "secure_note", map[string]any{"name": "mine", "body": "b"}, nil)
	expectStatus(t, h.request(t, "GET", "/items/"+carolOwn["id"].(string), nil, h.reader), 200)

	// Audit rows survive with opaque ids and no username snapshot.
	var raw string
	if err := h.db.QueryRow(`SELECT GROUP_CONCAT(COALESCE(actor_id,'') || COALESCE(target_id,'')) FROM audit_events`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "bob") {
		t.Fatal("audit stores username snapshots after creator deletion")
	}
}
