# Bitwarden JSON Import Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Add a safe, previewable Bitwarden JSON importer that converts only
Login credentials from unencrypted exports into append-only personal vault
items.

**Architecture:** Decode Bitwarden JSON in a pure transfer package component.
Normalize only Login credentials into the existing vault.ImportItem contract
and send both archive and Bitwarden previews through one staging helper,
preserving the existing signed token, session binding, cleanup, and atomic
confirmation. The browser selects the source format; the server parses the
credential fields.

**Tech Stack:** Go 1.26, encoding/json, existing SQLite/vault/transfer services, httptest integration tests, React 19, TypeScript, Vitest, Testing Library, OpenAPI YAML.

---

## File map

- Create internal/transfer/bitwarden.go for Bitwarden JSON login types, validation, and conversion.
- Create internal/transfer/bitwarden_test.go for pure parser tests.
- Modify internal/vault/service.go to carry favorite through ImportItem and ImportAll.
- Modify internal/transfer/format.go and transfer.go for archive favorite compatibility and common staging.
- Modify internal/httpapi/transfer.go and tests/integration/transfer_test.go for the endpoint.
- Modify web/src/features/transfer/TransferPage.tsx and its test for the UI.
- Modify api/openapi.yaml for the public operation.

## Task 1: Define parser behavior with failing tests

**Files:** Create internal/transfer/bitwarden_test.go.

- [ ] Step 1: Write a unit test named TestParseBitwardenJSONImportsOnlyLoginCredentials.

Use one JSON fixture with encrypted false, one folder, one favorite login with
username/password/URI/notes/TOTP/custom field, a secure note, a card, and an
identity. Assert one normalized login item, personal scope, empty OriginalID,
no tags or favorite state, exact name/username/password/URL mapping, and no
notes or unsupported fields in the target payload.

Use a fixture construction that does not put secrets in assertion failure text:

~~~go
raw := []byte("{\"encrypted\":false,\"folders\":[{\"id\":\"f1\",\"name\":\"Personal\"}],\"items\":[{\"id\":\"l1\",\"folderId\":\"f1\",\"type\":1,\"name\":\"Example\",\"favorite\":true,\"login\":{\"username\":\"alice\",\"password\":\"secret\",\"uris\":[{\"uri\":\"https://example.test\"}],\"totp\":\"JBSWY3DPEHPK3PXP\"},\"fields\":[{\"name\":\"Recovery\",\"value\":\"backup\",\"type\":0}]},{\"id\":\"n1\",\"type\":2,\"name\":\"Empty\",\"notes\":null},{\"id\":\"c1\",\"type\":3,\"name\":\"Visa\",\"card\":{\"cardholderName\":\"Alice\",\"number\":\"4111111111111111\",\"expMonth\":\"02\",\"expYear\":\"2030\",\"code\":\"123\"}},{\"id\":\"i1\",\"type\":4,\"name\":\"Alice\",\"identity\":{\"firstName\":\"Alice\",\"lastName\":\"Example\",\"email\":\"alice@example.test\",\"address1\":\"One Way\",\"city\":\"Town\",\"postalCode\":\"12345\"}}]}")
items, err := ParseBitwardenJSON(raw)
if err != nil { t.Fatal("parse failed") }
if len(items) != 1 { t.Fatalf("wrong item count: %d", len(items)) }
~~~

Decode the output payload into vault.LoginPayload. Assert login URL order and
that notes, TOTP, and custom fields are absent. Add table cases rejecting
malformed JSON, missing or true encrypted, empty items, no Login items,
duplicate Login IDs, and target-limit overflow. Assert errors do not contain a
source secret. Non-Login types are skipped.

- [ ] Step 2: Run the red test.

~~~bash
go test ./internal/transfer -run TestParseBitwardenJSON -count=1
~~~

Expected: failure because ParseBitwardenJSON is undefined. Correct test setup
errors before proceeding.

- [ ] Step 3: Extend the normalized import contract.

Add Favorite bool between Tags and Payload in ImportItem:

~~~go
type ImportItem struct {
    OriginalID string
    ItemType string
    Scope string
    Tags []string
    Favorite bool
    Payload json.RawMessage
}
~~~

- [ ] Step 4: Re-run the focused test, confirm it still fails for the parser, and commit the test contract.

~~~bash
go test ./internal/transfer -run TestParseBitwardenJSON -count=1
git add internal/transfer/bitwarden_test.go internal/vault/service.go
git commit -m "test: define Bitwarden import mappings"
~~~

## Task 2: Implement the pure converter

**Files:** Create internal/transfer/bitwarden.go; test internal/transfer/bitwarden_test.go.

- [ ] Step 1: Add bounded root decoding.

Define pointer-backed Encrypted and the Bitwarden item/login records needed for
credential extraction.
Do not reject unknown JSON fields because Bitwarden exports evolve. Require one
JSON value with no trailing value, a present false encrypted flag, and at least
one item. Select type 1 Login records, reject empty/duplicate Login IDs and
exports with no Login records, and skip every other item type.

Expose:

~~~go
const maxBitwardenJSONBytes = 64 << 20
var ErrInvalidBitwarden = errors.New("transfer: invalid Bitwarden export")
func ParseBitwardenJSON(raw []byte) ([]vault.ImportItem, error)
~~~

The parser must never place source names, IDs, or secret values in an error.

- [ ] Step 2: Implement the normalized credential mapping.

Set Scope to personal and leave OriginalID, Tags, and Favorite empty. Map only
the Login item's name, username, password, and non-empty URIs in source order.
Do not copy notes, TOTP, custom fields, attachments, folders, or favorites.
Marshal the target payload and validate it with the same vault payload
validator used by archive imports. A malformed or over-limit Login rejects the
whole preview; non-Login records remain ignored.

- [ ] Step 3: Run green focused tests and commit.

~~~bash
gofmt -w internal/transfer/bitwarden.go internal/transfer/bitwarden_test.go
go test ./internal/transfer ./internal/vault -run 'TestParseBitwardenJSON|Test.*SecureNote|Test.*Payload' -count=1
git add internal/transfer/bitwarden.go internal/transfer/bitwarden_test.go
git commit -m "feat: parse Bitwarden JSON exports"
~~~

## Task 3: Reuse transfer preview staging

**Files:** Modify internal/transfer/format.go, internal/transfer/transfer.go,
internal/vault/service.go, and internal/transfer/transfer_test.go.

- [ ] Step 1: Write failing service tests.

Extend the archive round-trip fixture with a favorite and assert the flag
survives export, preview, and confirmation. Add a Bitwarden service test that
calls PreviewBitwarden with one login, asserts count/token/zero conflicts/no
missing references, and verifies the database row count is unchanged before
confirm.

~~~go
before := countVaultRows(t, db)
result, err := svc.PreviewBitwarden(ctx, actor, validBitwardenJSON)
if err != nil { t.Fatal(err) }
if result.Counts["login"] != 1 || result.Token == "" ||
   result.Conflicts != 0 || len(result.MissingReferences) != 0 {
    t.Fatalf("unexpected preview: %#v", result)
}
if got := countVaultRows(t, db); got != before { t.Fatalf("preview wrote rows") }
~~~

- [ ] Step 2: Run the red tests.

~~~bash
go test ./internal/transfer ./tests/integration -run 'TestPreviewBitwarden|TestTransfer.*Favorite' -count=1
~~~

Expected: failure because PreviewBitwarden and favorite propagation are absent.

- [ ] Step 3: Make archive favorite backward-compatible.

Add Favorite bool json:"favorite,omitempty" to itemPayloadFile. Set it from
item.Meta.Favorite during export and copy it into ImportItem during archive
preview. Old archives omit the field and decode false.

- [ ] Step 4: Extract previewItems.

Move the existing post-extraction work that counts normalized items, checks
internal billing references and external gaps, checks ID conflicts, signs the
token, writes meta.json/payloads.json, and cleans failed stages into:

~~~go
func (s *Service) previewItems(ctx context.Context, actor *auth.Principal,
    staged []vault.ImportItem) (PreviewResult, error)
~~~

Preserve TTL, HMAC inputs, session binding, restricted modes, cleanup, and
existing archive semantics. Check IDExists only for non-empty OriginalID.
Bitwarden records always receive new UUIDv7 IDs.

- [ ] Step 5: Add PreviewBitwarden and route archive preview through the helper.

Implement:

~~~go
func (s *Service) PreviewBitwarden(ctx context.Context, actor *auth.Principal,
    raw []byte) (PreviewResult, error) {
    items, err := ParseBitwardenJSON(raw)
    if err != nil {
        return PreviewResult{MissingReferences: []string{}},
            fmt.Errorf("%w: %s", archive.ErrBadArchive, err.Error())
    }
    return s.previewItems(ctx, actor, items)
}
~~~

Archive Preview keeps extraction, manifest, digest, and payload validation, then
passes normalized items to previewItems.

- [ ] Step 6: Preserve favorites in ImportAllWithHook.

Change Favorite: false to Favorite: items[i].Favorite and make no other vault
transaction changes.

- [ ] Step 7: Run green tests and commit.

~~~bash
gofmt -w internal/transfer/format.go internal/transfer/transfer.go internal/transfer/transfer_test.go internal/vault/service.go
go test ./internal/transfer ./internal/vault ./tests/integration -run 'TestTransfer|TestPreviewBitwarden' -count=1
git add internal/transfer/format.go internal/transfer/transfer.go internal/transfer/transfer_test.go internal/vault/service.go
git commit -m "feat: stage Bitwarden imports through transfer"
~~~

## Task 4: Add the authenticated multipart API

**Files:** Modify internal/httpapi/transfer.go and tests/integration/transfer_test.go.

- [ ] Step 1: Write a failing endpoint test.

Use the existing integration session helper and a multipart file part named file.
Assert the new endpoint returns a preview with counts, that encrypted JSON
returns HTTP 400 code BAD_ARCHIVE, and that confirm creates personal rows
without changing any existing row.

- [ ] Step 2: Run the red test.

~~~bash
go test ./tests/integration -run TestBitwardenPreviewAndConfirm -count=1
~~~

Expected: stable 404 because the route is not registered.

- [ ] Step 3: Implement the bounded route.

Inside registerTransfer add authenticated POST
/api/v1/transfer/import/bitwarden/preview. Apply the existing
MaxTransferRequestBytes body cap and ParseMultipartForm archive limit. Read
FormFile file using io.LimitReader with max archive bytes plus one, reject
overflow as PAYLOAD_TOO_LARGE, call PreviewBitwarden, and return PreviewResult.
Do not accept a passphrase.

- [ ] Step 4: Map parser errors safely.

Map ErrInvalidBitwarden to HTTP 400, BAD_ARCHIVE, with a generic message. Do not
include source names, IDs, or secrets in the response.

- [ ] Step 5: Run and commit.

~~~bash
gofmt -w internal/httpapi/transfer.go tests/integration/transfer_test.go
go test ./tests/integration -run 'TestBitwarden|TestTransfer' -count=1
git add internal/httpapi/transfer.go tests/integration/transfer_test.go
git commit -m "feat: expose Bitwarden import preview API"
~~~

## Task 5: Add the transfer-page flow

**Files:** Modify web/src/features/transfer/TransferPage.tsx and
web/src/features/transfer/TransferPage.test.tsx.

- [ ] Step 1: Write the failing UI test.

Render the page, select Bitwarden JSON from 导入格式, upload data.json to
Bitwarden JSON 文件, and click 预览 Bitwarden 导入. Assert the request URL,
FormData body, file part, and absence of passphrase. Return a preview, click
确认导入, and assert the neutral confirm endpoint gets the token.

~~~tsx
it("previews and confirms a Bitwarden JSON file", async () => {
  const user = userEvent.setup();
  sessionStore.set({ principal, csrfToken: "csrf" });
  const fetchMock = vi.fn(async (url: string | URL) => {
    if (String(url).includes("bitwarden/preview")) {
      return jsonResponse(200, {
        preview_token: "bw-preview", counts: { login: 1 },
        conflicts: 0, missing_references: []
      });
    }
    return jsonResponse(200, { imported_count: 1 });
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<TransferPage />);
  await user.selectOptions(screen.getByLabelText("导入格式"), "bitwarden");
  await user.upload(screen.getByLabelText("Bitwarden JSON 文件"),
    new File(["{}"], "data.json", { type: "application/json" }));
  await user.click(screen.getByRole("button", { name: "预览 Bitwarden 导入" }));
  await screen.findByRole("region", { name: "导入预览" });
  const init = fetchMock.mock.calls[0][1] as RequestInit;
  expect(init.body).toBeInstanceOf(FormData);
  expect((init.body as FormData).get("file")).toBeInstanceOf(File);
  expect((init.body as FormData).get("passphrase")).toBeNull();
  await user.click(screen.getByRole("button", { name: "确认导入" }));
  await waitFor(() =>
    expect(fetchMock.mock.calls[1][0]).toBe("/api/v1/transfer/import/confirm"));
});
~~~

- [ ] Step 2: Run the red test.

~~~bash
npm --prefix web test -- --run src/features/transfer/TransferPage.test.tsx
~~~

Expected: failure because the selector and Bitwarden controls do not exist.

- [ ] Step 3: Add conditional controls and request construction.

Add importFormat state defaulting to archive and render a labeled select 导入格式.
When bitwarden is selected, use a JSON input accepting .json and
application/json, hide passphrase, append file, and call the Bitwarden endpoint.
Keep archive labels and request shape unchanged. Cancel any existing token on
format/file change. Retain abort, unmount, session-expiry, and shared confirm
behavior.

- [ ] Step 4: Update copy and run green checks.

Explain that only unencrypted Bitwarden JSON is accepted, CSV/encrypted exports
are unsupported, and records become personal. Then run:

~~~bash
npm --prefix web test -- --run src/features/transfer/TransferPage.test.tsx
npm --prefix web run typecheck
~~~

Commit:

~~~bash
git add web/src/features/transfer/TransferPage.tsx web/src/features/transfer/TransferPage.test.tsx
git commit -m "feat: add Bitwarden import controls"
~~~

## Task 6: Document the public operation

**Files:** Modify api/openapi.yaml near the existing transfer preview operation.

- [ ] Step 1: Add /transfer/import/bitwarden/preview as authenticated POST with
multipart file binary input, no passphrase, 64 MiB file and 96 MiB request limits,
and ImportPreview response. State that only unencrypted JSON Login records are
imported; non-Login types are skipped and an export with no Login record is
rejected.

- [ ] Step 2: Validate and commit.

~~~bash
git diff --check
go test ./tests/integration -run TestBitwarden -count=1
git add api/openapi.yaml
git commit -m "docs: document Bitwarden import API"
~~~

## Task 7: Verify before completion

- [ ] Step 1: Run focused suites.

~~~bash
go test ./internal/transfer ./internal/vault ./internal/httpapi ./tests/integration -run 'Test(Bitwarden|Transfer|Import|Preview)|Test.*SecureNote' -count=1
npm --prefix web test -- --run
npm --prefix web run typecheck
~~~

- [ ] Step 2: Run full Go verification.

~~~bash
go vet ./...
go test ./... -count=1
~~~

- [ ] Step 3: Build frontend.

~~~bash
npm --prefix web run build
~~~

- [ ] Step 4: Audit the committed design against files and tests. Verify server
parser, encrypted rejection, Login-only filtering, omission of unsupported
fields, fresh IDs, no-write preview, atomic confirmation, bounded API, UI,
OpenAPI, and tests. Run git diff --check and inspect git status --short.

- [ ] Step 5: Report only after reading fresh exit statuses and pass counts from
all commands. Fix and rerun full affected commands for any failure.
