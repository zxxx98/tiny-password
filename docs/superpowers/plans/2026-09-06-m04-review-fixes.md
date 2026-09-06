# M04 review fixes

> **For agentic workers:** Use superpowers:subagent-driven-development and verify each task against its acceptance criteria before integration.

**Goal:** Resolve the eleven findings in the M04 review without changing M5 behavior.

**Architecture:** Keep the existing vault policy and encrypted archive format. Bound uploads before parsing, use isolated ephemeral staging with explicit lifecycle cleanup, and serialize consumption of preview tokens. Reuse the frontend session-aware request lifecycle.

**Tech Stack:** Go, SQLite, 7zz, React, TypeScript, Vitest, Playwright.

## Task 1: Personal transfer correctness and lifecycle (findings 1–7)

Files: `internal/vault/service.go`, `internal/transfer/`, `internal/httpapi/transfer.go`, `cmd/tiny-password/main.go`, deployment configuration if needed, and targeted integration/unit tests.

- [ ] Add regressions for creator-only export, explicit capacity errors instead of truncation, and export/import capacity agreement (count manifest and directory entries).
- [ ] Query export candidates by ownership before applying limits; use limit+1 to detect excess and return a stable size error. Retain read/export policy checks.
- [ ] Bound the entire multipart request before parsing and bound archive reads. Preserve safe error codes and release temporary uploads on every path.
- [ ] Use restricted ephemeral staging by default, clean workspaces on failures, and close temporary file handles. Add lifecycle cleanup that runs without another preview request; cancel previews and remove them when the originating session is invalidated. Wire startup/shutdown correctly.
- [ ] Add deterministic concurrent-confirm regression; atomically claim a preview before import, preserving safe failure/retry behavior and preventing duplicate imports.
- [ ] Validate imported references against the complete imported set: identity targets only, shared sources require shared targets, missing external references remain cleared as documented. Reject invalid archives atomically.
- [ ] Run `go test ./internal/transfer ./internal/vault ./tests/integration -run 'TestTransfer|TestShared|TestImport|TestExport|TestPreview' -count=1` plus any new unit test names.
- [ ] Perform independent specification review, then code quality review.

## Task 2: Generator request validation and cancellation (findings 10–11)

Files: `internal/generator/ssh.go`, `internal/httpapi/generators.go`, generator unit/integration tests.

- [ ] Add failing HTTP cases for all-false character classes and explicit zero/negative lengths while preserving defaults for omitted fields.
- [ ] Represent optional fields explicitly and reject invalid supplied values through the generator validator.
- [ ] Make SSH generation context-aware, bound queue wait and total execution time, and ensure cancelled work stops consuming generation capacity. Avoid goroutine timeout wrappers that leave expensive work running.
- [ ] Test queued cancellation and deadline behavior deterministically, then run `go test ./internal/generator ./tests/integration -run 'TestGenerator|TestPassword|TestPassphrase|TestSSH' -count=1`.
- [ ] Perform independent specification review, then code quality review.

## Task 3: Frontend transfer and SSH state (findings 8–9)

Files: `web/src/app/api.ts`, `web/src/features/transfer/TransferPage.tsx`, `web/src/features/generator/GeneratorPage.tsx`, related frontend tests.

- [ ] Bind SSH result to generation-time passphrase/comment or invalidate the result on input edits; test generate-A/edit-B/save behavior.
- [ ] Route multipart and binary transfers through the common session-aware request lifecycle, including 401 invalidation and abort handling. Prevent late downloads after locking/unmounting.
- [ ] Wire the task-1 preview cancellation API to cancellation/navigation cleanup; clear local secrets consistently.
- [ ] Test session expiry during export/preview and late export responses after session clearing. Run `npm --prefix web run typecheck` and `npm --prefix web test -- --run`.
- [ ] Perform independent specification review, then code quality review.

## Final verification and delivery

- [ ] Run `go vet ./...`, `go test ./... -count=1`, targeted race tests, frontend typecheck/tests/build, and transfer/generator browser E2E where supported.
- [ ] Inspect diff and confirm all eleven findings are covered by implementation and regressions.
- [ ] Record actual validation evidence and integrate the reviewed fixes into the user's workspace, preserving `internal/scheduler/`.
