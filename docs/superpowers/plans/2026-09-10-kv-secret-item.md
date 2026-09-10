# KV Secret Item Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (\`- [ ]\`) syntax for tracking.

**Goal:** Add the encrypted, ordered \`secret\` vault item type with complete API, lifecycle, archive, editor, detail, clipboard, and test support.

**Architecture:** Extend the existing closed item-type union with a \`secret\` payload branch containing an ordered \`entries\` array. Reuse the existing service, repository, authorization, optimistic-lock, history, move, trash, and transfer paths; only add type-specific validation, schema branches, and UI components. Secret values are locally masked and copied without the existing audited \`SensitiveField\` endpoints.

**Tech Stack:** Go, SQLite migrations, \`encoding/json\`, OpenAPI YAML, React 19, TypeScript, Vitest, Testing Library, Playwright.

---

## File map

- Create \`migrations/0004_secret_item.sql\`: rebuild \`vault_items\` with \`secret\` in its type CHECK while preserving rows and indexes.
- Create \`internal/vault/payloads_test.go\`: unit tests for secret payload limits and exact duplicate-key semantics.
- Create \`web/src/features/vault/forms/SecretFields.tsx\`: ordered KV editor and client validation.
- Create \`web/src/features/vault/SecretValueField.tsx\`: un-audited masked value display and copy behavior.
- Modify \`internal/vault/types.go\`, \`payloads.go\`, \`validation.go\`, \`service.go\`, \`seed.go\`, and \`search.go\`: add the Go type branch everywhere payloads are dispatched.
- Modify \`internal/transfer/format.go\`: accept \`secret\` in archive manifests.
- Modify \`api/openapi.yaml\`: add the enum and payload schemas.
- Modify \`tests/integration/migrations_test.go\`, \`items_test.go\`, \`transfer_test.go\`, and \`tests/e2e/vault.spec.ts\`: cover migration, lifecycle, archive round-trip, and browser behavior.
- Modify \`web/src/features/vault/types.ts\`, \`ItemEditor.tsx\`, \`ItemDetail.tsx\`, \`VaultPage.tsx\`, and \`web/src/features/vault/vault.test.tsx\`: expose the type and UI behavior.

### Task 1: Add the backend secret payload contract with tests

**Files:**
- Create: \`internal/vault/payloads_test.go\`
- Modify: \`internal/vault/types.go\`
- Modify: \`internal/vault/payloads.go\`
- Modify: \`internal/vault/validation.go\`
- Modify: \`internal/vault/service.go\`
- Modify: \`internal/vault/seed.go\`
- Modify: \`internal/vault/search.go\`

- [ ] **Step 1: Write failing unit tests for the payload contract.**

Add table-driven tests in package \`vault\` that call \`decodePayload\` and assert the concrete \`*SecretPayload\`, preserving entry order and empty values. Cover these cases:

~~~go
func TestSecretPayloadValidation(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"valid ordered entries and empty value", \`{"name":"prod","entries":[{"key":"A","value":"1"},{"key":"B","value":""}],"notes":"n"}\`, false},
		{"blank key", \`{"name":"prod","entries":[{"key":" \t","value":"x"}]}\`, true},
		{"exact duplicate key", \`{"name":"prod","entries":[{"key":"A","value":"1"},{"key":"A","value":"2"}]}\`, true},
		{"case-sensitive keys are distinct", \`{"name":"prod","entries":[{"key":"A","value":"1"},{"key":"a","value":"2"}]}\`, false},
		{"leading whitespace participates in uniqueness", \`{"name":"prod","entries":[{"key":" A","value":"1"},{"key":"A","value":"2"}]}\`, false},
		{"zero entries", \`{"name":"prod","entries":[]}\`, true},
		{"129 entries", "generated", true},
		{"key over 256 code points", "generated", true},
		{"value over 16384 code points", "generated", true},
		{"notes over 10000 code points", "generated", true},
		{"unknown field", \`{"name":"prod","entries":[{"key":"A","value":"x"}],"extra":true}\`, true},
	}
	// For the four generated cases, construct SecretPayload values with
	// strings.Repeat and json.Marshal before calling decodePayload; assert
	// errors.Is(err, ErrPayloadInvalid) for every wantErr case.
}

func TestSecretPayloadEnvelopeRoundTripPreservesOrder(t *testing.T) {
	payload, err := decodePayload(TypeSecret, json.RawMessage(\`{"name":"prod","entries":[{"key":"FIRST","value":""},{"key":"SECOND","value":"two"}]}\`))
	if err != nil { t.Fatal(err) }
	_, raw, err := buildEnvelope(TypeSecret, nil, payload)
	if err != nil { t.Fatal(err) }
	var envelope storedPayload
	if err := json.Unmarshal(raw, &envelope); err != nil { t.Fatal(err) }
	got := typedPayloadOf(TypeSecret, &envelope).(*SecretPayload)
	if got.Entries[0].Key != "FIRST" || got.Entries[1].Key != "SECOND" || got.Entries[0].Value != "" {
		t.Fatalf("entries lost order or empty value: %+v", got.Entries)
	}
}
~~~

Generate the 129-entry and long-string inputs programmatically with \`strings.Repeat\`, then run:

~~~bash
go test ./internal/vault -run 'TestSecretPayload' -count=1
~~~

Expected: FAIL because \`TypeSecret\`, \`SecretPayload\`, and the dispatch branches do not exist.

- [ ] **Step 2: Add the minimal Go model and dispatch branches.**

Add \`TypeSecret = "secret"\` to \`internal/vault/types.go\`, include it in \`ItemTypes\`, add \`Secret\` to \`storedPayload\`, and add the \`*SecretPayload\` case to \`TitleOf\`. Define in \`payloads.go\`:

~~~go
type SecretPayload struct {
	Name    string        \`json:"name"\`
	Entries []SecretEntry \`json:"entries"\`
	Notes   string        \`json:"notes,omitempty"\`
}

type SecretEntry struct {
	Key   string \`json:"key"\`
	Value string \`json:"value"\`
}
~~~

Use constants \`MaxSecretEntries = 128\`, \`MaxSecretKeyRunes = 256\`, and \`MaxSecretValueRunes = 16384\`. Implement \`(*SecretPayload).validate()\` with \`requiredText("name", p.Name, MaxNameRunes)\`, \`len(entries)\` between 1 and 128, key non-blank after \`strings.TrimSpace\`, key/value rune limits, and a \`map[string]struct{}\` keyed by the original \`entry.Key\` for exact, case-sensitive duplicate detection. Validate \`notes\` with \`optionalText("notes", p.Notes, MaxNotesRunes)\`.

Add \`case TypeSecret\` to \`decodePayload\`, \`buildEnvelope\`, and \`typedPayloadOf\`; add \`case *SecretPayload\` to all payload type switches in \`service.go\`, including any \`typedPayload\`/import path checks, and to \`seed.go\`. Add no secret fields to \`searchableText\`, so search continues to exclude keys and values. Update comments saying “five types” to “six types”.

- [ ] **Step 3: Run the focused unit tests and refactor only after green.**

Run:

~~~bash
gofmt -w internal/vault/types.go internal/vault/payloads.go internal/vault/payloads_test.go internal/vault/validation.go internal/vault/service.go internal/vault/seed.go internal/vault/search.go
go test ./internal/vault -run 'TestSecretPayload' -count=1
~~~

Expected: PASS. Keep the test green while simplifying helpers or comments; do not add behavior beyond the documented limits.

### Task 2: Extend the database schema and public API contract

**Files:**
- Create: \`migrations/0004_secret_item.sql\`
- Modify: \`api/openapi.yaml\`
- Test: \`tests/integration/migrations_test.go\`

- [ ] **Step 1: Write migration tests before the migration.**

Add an integration test that opens a database built from \`tests/fixtures/schema-v1.sql\`, inserts a valid old \`login\` row, stamps schema version 1, runs the current migration set, and verifies the row is still present and \`secret\` is accepted by inserting a valid \`secret\` row. Verify inserting \`unknown\` into \`vault_items.item_type\` fails with a CHECK error. Also assert the existing owner/creator, favorite, payload, revision, and timestamp columns remain available.

Run:

~~~bash
go test ./tests/integration -run 'Test.*Secret|TestMigration' -count=1
~~~

Expected: FAIL because the current schema has no migration 0004.

- [ ] **Step 2: Implement the transactional schema rebuild.**

Create \`migrations/0004_secret_item.sql\` using the SQLite table-rebuild pattern: \`PRAGMA foreign_keys=OFF\`, create \`vault_items_new\` with the exact current columns and constraints except the \`item_type\` CHECK includes \`'secret'\`, copy every column from \`vault_items\`, drop the old table, rename the new table, recreate \`idx_vault_items_owner\`, \`idx_vault_items_creator\`, \`idx_vault_items_updated\`, and \`idx_vault_items_deleted\`, then restore \`PRAGMA foreign_keys=ON\`. Do not touch \`item_versions\`; its foreign key follows the renamed table. Keep the migration free of data rewriting or payload decryption.

- [ ] **Step 3: Add the OpenAPI schemas and verify the migration.**

Change \`ItemType\` to include \`secret\`. Add:

~~~yaml
SecretEntry:
  type: object
  required: [key, value]
  properties:
    key: { type: string, minLength: 1, maxLength: 256 }
    value: { type: string, maxLength: 16384 }
SecretPayload:
  type: object
  required: [name, entries]
  properties:
    name: { type: string, minLength: 1, maxLength: 256 }
    entries:
      type: array
      minItems: 1
      maxItems: 128
      items: { $ref: "#/components/schemas/SecretEntry" }
    notes: { type: string, maxLength: 10000 }
~~~

Add \`SecretPayload\` to \`ItemPayload.oneOf\` and update descriptions that enumerate item types. Run:

~~~bash
gofmt -w tests/integration/migrations_test.go
go test ./tests/integration -run 'Test.*Secret|TestMigration' -count=1
~~~

Expected: PASS.

### Task 3: Cover API lifecycle and archive round-trip

**Files:**
- Modify: \`internal/transfer/format.go\`
- Modify: \`tests/integration/items_test.go\`
- Modify: \`tests/integration/transfer_test.go\`
- Modify: \`tests/integration/migrations_test.go\`

- [ ] **Step 1: Add failing HTTP lifecycle tests.**

Add tests using the existing \`itemsHarness\` for create/get/update/history/move/trash/restore of a \`secret\` payload with at least three ordered entries, including an empty value. Assert GET returns the same array order and values, the update creates the next revision with changed order/value, history includes the prior revision, move preserves the payload, and trash/restore returns the same payload. Add permission assertions matching existing secure-note lifecycle tests: the owner can read/update, another member cannot read personal data, shared readers can read, and only the creator can update.

Add a search test that creates a secret whose key/value contain a query and whose title/tag contain another query; assert only title/tag matches are returned. Add a list type-filter test for \`type=secret\`.

Run:

~~~bash
go test ./tests/integration -run 'Test.*Secret|Test.*Item.*Lifecycle|TestSearch' -count=1
~~~

Expected: FAIL where the type is not accepted or branches are incomplete.

- [ ] **Step 2: Extend transfer manifest validation and add archive tests.**

Add \`"secret"\` to the accepted manifest type switch in \`internal/transfer/format.go\`. Extend transfer tests to export a secret with multiple entries, preview it, import it, and compare the imported payload JSON structurally and in array order. Assert the manifest counts include \`secret\`. Keep Bitwarden tests unchanged and assert Bitwarden never emits \`secret\`.

- [ ] **Step 3: Run all backend tests.**

Run:

~~~bash
gofmt -w internal/transfer/format.go tests/integration/items_test.go tests/integration/transfer_test.go tests/integration/migrations_test.go
go test ./internal/vault ./internal/transfer ./tests/integration -count=1
~~~

Expected: PASS with no new warnings. Fix implementation or tests, never weaken the contract.

### Task 4: Add the ordered KV editor and client-side validation

**Files:**
- Create: \`web/src/features/vault/forms/SecretFields.tsx\`
- Modify: \`web/src/features/vault/types.ts\`
- Modify: \`web/src/features/vault/ItemEditor.tsx\`
- Test: \`web/src/features/vault/vault.test.tsx\`

- [ ] **Step 1: Write failing component tests.**

Add tests that select \`secret\`, observe one initial key/value row, add two rows, fill keys and values, delete the middle row, and assert the submitted payload has the remaining entries in original relative order. Assert the last remaining row has no usable delete action. Assert blank keys, exact duplicate keys, 129 rows, and length overflow render inline errors and do not call \`fetch\`. Assert a value input has \`type="password"\` by default and a row’s display toggle changes only that row.

Run:

~~~bash
cd web && npm test -- --run src/features/vault/vault.test.tsx
~~~

Expected: FAIL because the type and form component do not exist.

- [ ] **Step 2: Add client types, limits, template, validator, and form.**

In \`types.ts\`, add:

~~~ts
export type SecretEntry = { key: string; value: string };
export type SecretPayload = { name: string; entries: SecretEntry[]; notes?: string };
~~~

Include \`secret\` in \`ITEM_TYPES\`, label it \`密钥\`, add it to \`ItemPayload\`, add \`secret: { name: "", entries: [{ key: "", value: "" }], notes: "" }\` to \`emptyPayload\`, and add limits \`secretEntries: 128\`, \`secretKey: 256\`, \`secretValue: 16384\`.

Implement \`validateSecret\` in \`SecretFields.tsx\` using \`Array.from(value).length\`, requiring a non-blank name, 1–128 entries, non-blank trimmed keys, exact original-string duplicate detection, key/value lengths, and notes length. Return indexed errors such as \`entries.0.key\` so each row renders its own message.

Render title and notes with existing \`Field\` conventions. Each KV row renders a labelled key input, a password input for value, a local \`显示\`/\`隐藏\` button, and a \`删除\` button. \`添加一组\` appends \`{ key: "", value: "" }\`; disable deletion when only one row remains. Use immutable array updates and preserve array order.

Register \`validateSecret\` in \`ItemEditor\`’s validator map, import/render \`SecretFields\`, and pass its \`onChange\` patch through the existing payload state path.

- [ ] **Step 3: Run focused web tests and typecheck.**

Run:

~~~bash
cd web
npm test -- --run src/features/vault/vault.test.tsx
npm run typecheck
~~~

Expected: PASS.

### Task 5: Add un-audited secret value detail and exact clipboard behavior

**Files:**
- Create: \`web/src/features/vault/SecretValueField.tsx\`
- Modify: \`web/src/features/vault/ItemDetail.tsx\`
- Modify: \`web/src/features/vault/VaultPage.tsx\`
- Modify: \`web/src/features/vault/types.ts\`
- Test: \`web/src/features/vault/vault.test.tsx\`

- [ ] **Step 1: Write failing detail and clipboard tests.**

Add an \`ItemDetail\` test with ordered entries \`A=one\`, \`B=""\`, and \`C="line1\\nline2"\`. Assert all values are masked initially, revealing one row does not reveal another, and no fetch request is made by reveal or copy. Assert single copy writes an empty string for \`B\`, sets a \`role="status"\` success/failure message, and schedules cleanup. Assert copy-all writes exactly \`A=one\\nB=\\nC=line1\\nline2\` with no final newline. Rejecting \`navigator.clipboard.writeText\` must show \`复制失败，请重试或手动复制\` without changing the UI values. Add a list/detail assertion for \`密钥 · N 个键值\` where the count is available from the detail payload; the list must not request/decrypt entries.

Run:

~~~bash
cd web && npm test -- --run src/features/vault/vault.test.tsx
~~~

Expected: FAIL because \`SecretValueField\` and the detail branch do not exist.

- [ ] **Step 2: Implement local-only masking and copying.**

\`SecretValueField\` should hold only \`revealed\` and \`copyState\` state, render a password-style masked span or a plaintext \`output\`, and use Clipboard API directly. It must not import the API request client or call \`/reveal\`/\`/copy\`. On successful copy call \`scheduleClipboardCleanup(value)\` and update status messages using the same cleanup result language as \`SensitiveField\`; on any Clipboard error show the specified failure message. Keep \`value\` in a ref so cleanup and copy use the latest value.

In \`ItemDetail\`, add \`case "secret"\`, render the title/notes and \`entries.map\` in stored order, and wire each row to \`SecretValueField\`. Add a \`复制全部\` button that joins entries with \`entries.map((entry) => entry.key + "=" + entry.value).join("\\n")\`; do not trim, quote, escape, or append a newline. Set a \`role="status"\` message for both success and failure.

Update \`describeItem\`/list rendering to show \`密钥 · N 个键值\` wherever a decrypted detail is available. Since list responses intentionally contain no payload, do not add a plaintext key/value search or make the list endpoint decrypt entries; use the existing type label in the list until a count-bearing detail is opened.

- [ ] **Step 3: Run focused tests, build the frontend, and verify no audit calls.**

Run:

~~~bash
cd web
npm test -- --run src/features/vault/vault.test.tsx
npm run typecheck
npm run build
~~~

Expected: PASS; test fetch call counts must remain unchanged during secret reveal/copy.

### Task 6: Add end-to-end coverage and complete verification

**Files:**
- Modify: \`tests/e2e/vault.spec.ts\`
- Modify: \`CHANGELOG.md\` only if this repository’s release checklist requires an entry for new user-visible types.

- [ ] **Step 1: Add the browser scenario.**

Create an E2E test that opens \`新建条目\`, selects \`secret\`, fills three ordered KV rows (including an empty value), saves, reopens the item, confirms all values are initially hidden, copies all, checks the exact clipboard text, edits one value and row order without dragging, saves, and reopens to confirm the new order/value. Also assert no request URL ends with \`/reveal\` or \`/copy\` during the scenario.

- [ ] **Step 2: Run the complete verification set.**

Run from the worktree root:

~~~bash
go test ./...
cd web && npm test -- --run && npm run typecheck && npm run build
cd .. && npm run test:e2e
git diff --check
git status --short
~~~

Expected: all Go and Vitest tests pass, TypeScript and Vite build exit 0, the relevant Playwright scenario passes in the configured environment, \`git diff --check\` prints nothing, and only the intended source, migration, API, test, and plan files are changed.

- [ ] **Step 3: Commit the completed implementation.**

After the verification commands pass, commit the implementation on \`feature/kv-secret-item\`:

~~~bash
git add api/openapi.yaml migrations/0004_secret_item.sql internal/vault internal/transfer/format.go tests/integration/items_test.go tests/integration/transfer_test.go tests/integration/migrations_test.go tests/e2e/vault.spec.ts web/src/features/vault docs/superpowers/plans/2026-09-10-kv-secret-item.md
git commit -m "feat: add ordered KV secret vault items"
~~~
