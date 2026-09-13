# Vault Bidirectional Move Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Add a transactional, idempotent, bidirectional personal/shared vault move that creates a new revision-1 item from the current draft, removes the source item and its history, and exposes the behavior through the editor and API contract.

**Architecture:** Keep PUT /api/v1/items/{itemId} as the update endpoint. Omitted or equal vault_scope uses the existing optimistic-lock update; a changed scope dispatches to a move path that decrypts the source, inserts a server-owned target, records creation and move audit events, deletes the source, and completes an optional idempotency claim in one SQLite transaction. The editor owns the warning confirmation and sends the returned target detail through the existing refresh/navigation path.

**Tech Stack:** Go, database/sql with SQLite migrations, XChaCha20 payload encryption, the existing HMAC idempotency service, React/TypeScript, Vitest/Testing Library, and OpenAPI YAML.

---

### Task 1: Define move contract and repository/audit primitives

**Files:**
- Create: migrations/0003_vault_move_audit.sql
- Modify: internal/audit/events.go
- Modify: internal/audit/service.go
- Modify: internal/vault/repository.go
- Test: internal/audit/service_test.go
- Test: internal/vault/policy_test.go

- [ ] Step 1: Write failing unit tests

Add an audit test that records EventVaultItemMoved with SourceID, SourceScope, and TargetScope, reads the row, and asserts all three values. Add a policy test proving update permission remains true only for the personal owner/shared creator and false for an administrator or another member.

~~~go
audit.Event{
    Name: audit.EventVaultItemMoved, ActorID: "actor-id",
    TargetType: audit.TargetItem, TargetID: "target-id",
    Result: audit.ResultSuccess, SourceID: "source-id",
    SourceScope: "personal", TargetScope: "shared",
}
~~~

- [ ] Step 2: Run tests to verify failure

~~~bash
go test ./internal/audit ./internal/vault
~~~

Expected: compile failure for the missing event/fields, or a failing assertion.

- [ ] Step 3: Add the migration

Create migrations/0003_vault_move_audit.sql:

~~~sql
ALTER TABLE audit_events ADD COLUMN source_id TEXT;
ALTER TABLE audit_events ADD COLUMN source_scope TEXT;
ALTER TABLE audit_events ADD COLUMN target_scope TEXT;
~~~

The columns are nullable so existing audit rows remain compatible.

- [ ] Step 4: Extend audit encoding

Add EventVaultItemMoved = "vault.item.moved" and its allowlist entry. Extend audit.Event and audit.Entry with SourceID, SourceScope, and TargetScope; render entry fields as source_id, source_scope, and target_scope with omitempty. Update Record, scanEntry, and query to insert/select/scan the columns. Validate non-empty scope values against personal and shared; preserve the existing opaque ID length checks.

- [ ] Step 5: Add revision-guarded deletion

In internal/vault/repository.go, add this separate from purge’s unguarded deleteItemCompletely:

~~~go
func (repository) deleteItemAtRevision(ctx context.Context, q Queryer, itemID string, revision uint64) (bool, error) {
    res, err := q.ExecContext(ctx,
        "DELETE FROM vault_items WHERE id=? AND revision=? AND deleted_at IS NULL",
        itemID, revision)
    if err != nil {
        return false, err
    }
    affected, err := res.RowsAffected()
    if err != nil {
        return false, err
    }
    return affected == 1, nil
}
~~~

- [ ] Step 6: Verify and commit

~~~bash
gofmt -w internal/audit/events.go internal/audit/service.go internal/vault/repository.go internal/audit/service_test.go internal/vault/policy_test.go
go test ./internal/audit ./internal/vault
go test ./tests/integration -run 'Test(Upgrade|Migration|Audit)'
git diff --check
~~~

Expected: PASS. Commit only these primitives:

~~~bash
git add migrations/0003_vault_move_audit.sql internal/audit/events.go internal/audit/service.go internal/audit/service_test.go internal/vault/repository.go internal/vault/policy_test.go
git commit -m "feat: add move audit and revision delete primitives"
~~~

### Task 2: Implement the transactional service move

**Files:**
- Modify: internal/vault/service.go
- Modify: internal/vault/types.go
- Test: tests/integration/items_test.go

- [ ] Step 1: Write failing integration tests

Add table-driven TestItemMoveRoundTripPreservesCurrentState for login, ssh_key, credit_card, identity, and secure_note. For each, create a personal item, perform one ordinary update to create revision 2 and one history row, then PUT the current payload/tags/favorite with vault_scope: "shared". Assert the returned ID differs, scope is shared, revision is 1, and payload/tags/favorite equal the submitted current values. Query the database and assert the source row and all source item_versions are gone, the target has no history, and exactly one vault.item.moved row has source and target IDs. Move the target back and assert a third ID, revision 1, personal ownership, preserved current state, and removal of the shared source.

Use a credit-card payload without billing_address_item_id for the round trip and add a focused case with a present valid reference to assert the payload field is copied unchanged. Include the existing identity payload fixture unchanged.

- [ ] Step 2: Write failing tests for rejection and rollback

Add cases asserting admin and a non-creator shared member cannot move; invalid scope returns 400 VALIDATION_ERROR; stale revision returns 409 REVISION_CONFLICT; trashed source returns 404 and is unchanged. Inject an audit failure and assert the 500 response leaves the source at its original revision/history and leaves no target row. Also assert old source IDs are not readable after a successful move.

- [ ] Step 3: Run the tests to verify failure

~~~bash
go test ./tests/integration -run 'TestItemMove'
~~~

Expected: failures because UpdateInput has no scope and no move path exists.

- [ ] Step 4: Add the optional scope to the service input

Change UpdateInput to:

~~~go
type UpdateInput struct {
    Revision    uint64
    Tags        *[]string
    Favorite    *bool
    Payload     json.RawMessage
    VaultScope  *string
    Claim       *idempotency.Claim
}
~~~

Validate a present scope against Scopes in updateOnce. After source authorization and revision matching, dispatch only when the pointer is non-nil and differs from the stored scope. Omitted and equal scope must retain the existing no-op detection, history archive, and revision behavior.

- [ ] Step 5: Implement moveOnce

Add:

~~~go
func (s *Service) moveOnce(ctx context.Context, tx *sql.Tx, actor *auth.Principal, source itemRow, input UpdateInput) (Detail, error)
~~~

The helper must decode/validate the submitted payload, decrypt the source envelope with the source AAD, reuse current tags/favorite when their fields are omitted, derive target ownership from actor and target scope, validate any existing address reference using current rules, build/encrypt a new envelope under a fresh UUID and target AAD at revision 1, insert the target with move-time timestamps, record target creation and vault.item.moved, complete input.Claim if present, then delete the source using deleteItemAtRevision(source.ID, source.Revision). A failed guarded delete must return the current revision conflict or not-found result. Commit only after every operation succeeds and return complete target detail.

Use the same ownership derivation as Create:

~~~go
targetID := ident.NewUUIDv7()
targetScope := *input.VaultScope
owner, creator := "", ""
if targetScope == string(ScopePersonal) {
    owner = actor.UserID
} else {
    creator = actor.UserID
}
targetAt := s.now().UTC().Format(TimestampFormat)
targetAAD := AADFor(targetID, targetScope, owner, creator, crypto.PayloadVersion, 1)
~~~

Do not copy the UUID, timestamps, deleted state, or item_versions; do not call updateItem in this branch.

- [ ] Step 6: Verify and commit

~~~bash
gofmt -w internal/vault/service.go internal/vault/types.go tests/integration/items_test.go
go test ./tests/integration -run 'TestItemMove|TestItemUpdate|TestItemAddressReferences'
~~~

Expected: PASS. Commit:

~~~bash
git add internal/vault/service.go internal/vault/types.go tests/integration/items_test.go
git commit -m "feat: move vault items across scopes transactionally"
~~~

### Task 3: Wire HTTP idempotency and update the API contract

**Files:**
- Modify: internal/httpapi/items.go
- Modify: api/openapi.yaml
- Test: tests/integration/items_test.go
- Test: tests/integration/idempotency_test.go

- [ ] Step 1: Write failing HTTP tests

Repeat an exact successful move PUT with a valid Idempotency-Key; both responses must return the same target ID, while the database has one target and no source. Change the request under the same key and assert 409 IDEMPOTENCY_KEY_CONFLICT. Add a malformed-key assertion for 400 VALIDATION_ERROR. Confirm scope is forwarded and the old source is not re-created.

- [ ] Step 2: Run tests to verify failure

~~~bash
go test ./tests/integration -run 'TestItemMove|Test.*Idempotency'
~~~

Expected: scope may be ignored and repeated moves create conflicting outcomes because PUT has no claim/replay path.

- [ ] Step 3: Add request and fingerprint support

Change itemUpdateRequest to carry VaultScope *string with JSON name vault_scope. Add itemsMoveIdempotencyScope = "items.move" and fingerprint the source ID, revision, target scope, payload, and presence/value of tags and favorite. Bind the claim scope with ScopeFor(itemsMoveIdempotencyScope, principal.UserID).

- [ ] Step 4: Implement claim/replay

For a changed-scope update with an idempotency header, claim before calling the service. Handle OutcomeFresh, OutcomeInFlight, OutcomeConflict, and replay exactly as the existing create endpoint does. Replay must call Service.Get to re-authorize and render the stored target ID. Release a claim on all failed handler paths. Pass the fresh claim to the service, which completes it inside the move transaction; same-scope updates stay unchanged.

- [ ] Step 5: Update OpenAPI

Document optional vault_scope on ItemUpdate; omission/equality means ordinary update. Document a changed scope as creating a new revision-1 item, deleting the source and all source history atomically, and returning the new ID. Add the idempotency parameter to PUT. Add vault.item.moved and optional source/target audit context fields to AuditEvent.

- [ ] Step 6: Verify and commit

~~~bash
gofmt -w internal/httpapi/items.go tests/integration/items_test.go tests/integration/idempotency_test.go
go test ./...
git diff --check
~~~

Expected: PASS. Commit:

~~~bash
git add internal/httpapi/items.go api/openapi.yaml tests/integration/items_test.go tests/integration/idempotency_test.go
git commit -m "feat: expose idempotent vault moves over the item API"
~~~

### Task 4: Add editor position control and move confirmation

**Files:**
- Modify: web/src/features/vault/ItemEditor.tsx
- Modify: web/src/features/vault/ItemDialog.tsx
- Modify: web/src/features/vault/VaultPage.tsx
- Test: web/src/features/vault/vault.test.tsx

- [ ] Step 1: Write failing component tests

Extend the existing ItemEditor tests with personal and shared details. Assert edit mode shows “保存位置” and both radio labels, changing scope makes the editor dirty, and submitting a changed scope opens no network request before confirmation. Assert exact warnings:

~~~text
移动后将创建一个新的共享条目并删除当前条目，历史记录不会保留。仍要继续吗？
移动后将创建一个新的个人条目并删除当前条目，历史记录不会保留。仍要继续吗？
~~~

Assert cancel closes the warning while leaving the selected radio and dirty draft. Assert confirmation sends scope, current revision, payload, tags, favorite, and an idempotency header. Assert the returned new ID reaches onSaved and the page shows 已移至共享 or 已移至个人保险库. Assert a failed list refresh after a successful PUT leaves the success detail/status visible and displays only the list error.

- [ ] Step 2: Run frontend tests to verify failure

~~~bash
npm --prefix web test -- --run src/features/vault/vault.test.tsx
~~~

Expected: edit mode lacks the scope control/confirmation/status behavior.

- [ ] Step 3: Render scope in both modes

Move the existing 保存位置 fieldset outside the create-only fragment in ItemEditor.tsx. Keep type selection create-only. Disable scope controls while submitting. Keep scope in editorSnapshot, and set the authoritative snapshot to fresh.vault_scope after conflict reload.

- [ ] Step 4: Confirm before sending a move

Add onMoveConfirm?: (from: "personal" | "shared", to: "personal" | "shared") => Promise<boolean> to ItemEditorProps. Before a changed-scope edit PUT, await it; false preserves every draft value and sends no request. True sends vault_scope and a stable editor move idempotency key through request options. Keep create’s existing key separate. On success, update the snapshot from returned type/scope/payload/tags/favorite before onSaved.

- [ ] Step 5: Add page confirmation/status

Add move confirmation state and a resolver in VaultPage; pass the callback through ItemDialog to ItemEditor. Resolve it from a danger ConfirmDialog with the exact direction text. Clear the resolver on cancel and unmount. Add transient statusMessage; when refreshAfterChange sees a changed scope, set the matching Chinese success message. Keep returned-ID navigation and list reload. A list reload error must go through failList without replacing the successful detail/status.

- [ ] Step 6: Preserve retry safety

Use one stable move idempotency key for an edit draft. On an uncertain network result, retain the draft and key; a later retry repeats the same PUT/key. If the source is gone, refresh and use idempotency replay/target detail before reporting failure. Never silently issue a second create.

- [ ] Step 7: Verify and commit

~~~bash
npm --prefix web run typecheck
npm --prefix web test -- --run
npm --prefix web run build
~~~

Expected: PASS. Commit:

~~~bash
git add web/src/features/vault/ItemEditor.tsx web/src/features/vault/ItemDialog.tsx web/src/features/vault/VaultPage.tsx web/src/features/vault/vault.test.tsx
git commit -m "feat: confirm and display bidirectional vault moves"
~~~

### Task 5: Synchronize documentation and perform final verification

**Files:**
- Modify: docs/design/2026-09-09-vault-usability-improvements.md
- Modify: docs/superpowers/specs/2026-09-09-vault-move-and-remove-address-design.md

- [ ] Step 1: Locate stale move statements

~~~bash
rg -n '保留条目 ID|保留的历史|历史版本随条目|重新加密历史|反向引用|先将地址共享|IdentityPayload|billing_address_item_id' docs/design/2026-09-09-vault-usability-improvements.md docs/superpowers/specs/2026-09-09-vault-move-and-remove-address-design.md
~~~

- [ ] Step 2: Update the development document

Rewrite move sections 1 and 4.1–4.6 to state: moving creates a completely new target item from the current submitted payload, generates a new ID, fixes revision at 1, copies no history, permanently deletes the source in the same transaction (source history is cascade-deleted), and retains audit rows. Require a second confirmation explicitly saying a new item will be created, the current item deleted, and history not retained. State that the UI switches to the returned target ID.

Remove claims about preserving ID, incrementing revision, retaining/re-encrypting history, or reverse-reference blocking. Keep IdentityPayload, billing_address_item_id, and current reference validation untouched; explicitly state the current payload is copied as submitted and no extra move reference handling is added.

- [ ] Step 3: Align the move spec

Rename the spec to docs/superpowers/specs/2026-09-09-vault-bidirectional-move-design.md only if no links depend on its old name; otherwise update its title/status in place. It must not require retaining the old ID/history or deleting existing identity/address fields.

- [ ] Step 4: Run final verification

~~~bash
go test ./...
npm --prefix web run typecheck
npm --prefix web test -- --run
npm --prefix web run build
git diff --check
git status --short
~~~

If available, also run ./scripts/test-browser-e2e.sh. Expected: all required commands pass and the user-owned untracked docs/design/ directory remains untouched.

- [ ] Step 5: Commit documentation

~~~bash
git add docs/design/2026-09-09-vault-usability-improvements.md docs/superpowers/specs/2026-09-09-vault-move-and-remove-address-design.md
git commit -m "docs: align vault move semantics with new-item behavior"
~~~

