package transfer

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tiny-password/tiny-password/internal/vault"
)

func TestParseBitwardenJSONImportsOnlyLoginCredentials(t *testing.T) {
	raw := []byte(`{
  "encrypted": false,
  "folders": [{"id":"folder-1","name":"Personal"}],
  "items": [
    {"id":"login-1","folderId":"folder-1","type":1,"name":"Example","favorite":true,
     "notes":"keep","login":{"username":"alice","password":"secret",
       "uris":[{"uri":"https://example.test"}],"totp":"JBSWY3DPEHPK3PXP"},
     "fields":[{"name":"Recovery","value":"backup","type":0}]},
    {"id":"note-1","type":2,"name":"Empty note","notes":null},
    {"id":"card-1","type":3,"name":"Visa",
     "card":{"cardholderName":"Alice","number":"4111111111111111","expMonth":"02","expYear":"2030","code":"123"}},
    {"id":"identity-1","type":4,"name":"Alice",
     "identity":{"firstName":"Alice","lastName":"Example","email":"alice@example.test",
       "address1":"One Way","city":"Town","postalCode":"12345"}}
  ]
}`)

	items, err := ParseBitwardenJSON(raw)
	if err != nil {
		t.Fatal("parse failed")
	}
	if len(items) != 1 {
		t.Fatalf("items=%d, want 1 login item", len(items))
	}
	if items[0].ItemType != vault.TypeLogin || items[0].Favorite || len(items[0].Tags) != 0 {
		t.Fatalf("login metadata: %#v", items[0])
	}
	var login vault.LoginPayload
	if err := json.Unmarshal(items[0].Payload, &login); err != nil {
		t.Fatal(err)
	}
	if login.Username != "alice" || login.Password != "secret" ||
		len(login.URLs) != 1 || login.URLs[0] != "https://example.test" ||
		login.Name != "Example" || login.Notes != "" {
		t.Fatalf("login payload: %#v", login)
	}
	for _, item := range items {
		if item.OriginalID != "" || item.Scope != string(vault.ScopePersonal) {
			t.Fatalf("external id/scope leaked: %#v", item)
		}
	}
}

func TestParseBitwardenJSONRejectsInvalidExportsWithoutSecrets(t *testing.T) {
	valid := `{"encrypted":false,"items":[{"id":"item-1","type":1,"name":"name","login":{"username":"user","password":"DO_NOT_LEAK"}}]}`
	tooLongName := `{"encrypted":false,"items":[{"id":"item-1","type":1,"name":"` + strings.Repeat("x", vault.MaxNameRunes+1) + `","login":{"username":"user","password":"DO_NOT_LEAK"}}]}`
	cases := []struct {
		name   string
		raw    string
		secret string
	}{
		{name: "malformed json", raw: "{", secret: "DO_NOT_LEAK"},
		{name: "missing encrypted flag", raw: `{"items":[{"id":"item-1","type":1,"name":"name","login":{}}]}`, secret: "DO_NOT_LEAK"},
		{name: "encrypted export", raw: strings.Replace(valid, "false", "true", 1), secret: "DO_NOT_LEAK"},
		{name: "empty items", raw: `{"encrypted":false,"items":[]}`, secret: "DO_NOT_LEAK"},
		{name: "no login items", raw: `{"encrypted":false,"items":[{"id":"card-1","type":3,"name":"card"}]}`, secret: "DO_NOT_LEAK"},
		{name: "duplicate ids", raw: `{"encrypted":false,"items":[{"id":"same","type":1,"name":"one","login":{}},{"id":"same","type":1,"name":"two","login":{}}]}`, secret: "DO_NOT_LEAK"},
		{name: "only unsupported types", raw: `{"encrypted":false,"items":[{"id":"item-1","type":5,"name":"key"},{"id":"item-2","type":99,"name":"unknown"}]}`, secret: "DO_NOT_LEAK"},
		{name: "target limit", raw: tooLongName, secret: "DO_NOT_LEAK"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseBitwardenJSON([]byte(tc.raw)); err == nil {
				t.Fatal("invalid export was accepted")
			} else if strings.Contains(err.Error(), tc.secret) {
				t.Fatal("parser error leaked a source secret")
			}
		})
	}
}
