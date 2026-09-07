# M05 Review Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the remaining M05 backup, scheduler, retention, R2, and audit findings with regression coverage.

**Architecture:** Reuse the existing scheduler for daily maintenance and add an atomic reload path for persisted backup schedules. Keep backup target delivery independent, verify R2 final objects by digest, restrict retention to final objects, and emit display-safe audit events for administrative and offline operations.

**Tech Stack:** Go, SQLite, S3-compatible REST client, React/TypeScript, integration tests.

---

### Task 1: Schedule maintenance jobs

**Files:**
- Modify: `internal/backup/jobs.go`
- Test: `tests/integration/backup_scheduler_test.go`

- [x] Add a scheduler-path regression that registers `MaintenanceJobs`, evaluates after the maintenance time, and asserts the task runs once.
- [x] Run the regression and confirm it fails because maintenance jobs have an empty `DailyAt`.
- [x] Set all maintenance jobs to the shared `03:30` UTC schedule and retain their existing functions.
- [x] Run the focused integration test and the scheduler package tests.

### Task 2: Refresh persisted backup schedules

**Files:**
- Modify: `internal/scheduler/scheduler.go`, `internal/httpapi/backups.go`, `cmd/tiny-password/main.go`
- Test: `tests/integration/backups_api_test.go`, `tests/integration/backup_scheduler_test.go`

- [x] Add a regression showing an enabled schedule saved after startup is visible to the running scheduler without restart.
- [x] Run it to verify the current startup-only registration fails.
- [x] Add an atomic scheduler reload method and call it after a successful job update; unregister disabled targets and replace changed jobs.
- [x] Run focused API and scheduler tests.

### Task 3: Preserve busy scheduled runs as skips

**Files:**
- Modify: `internal/backup/jobs.go`
- Test: `tests/integration/backup_scheduler_test.go`

- [x] Add a regression where a manual run holds the backup mutex and a due scheduled run returns a skipped result without consuming the daily marker.
- [x] Run it to confirm the error-type mismatch currently records failure.
- [x] Wrap `backup.ErrBusy` as `scheduler.ErrBusy` at the scheduler boundary.
- [x] Run the focused test.

### Task 4: Verify R2 final content and isolate retention objects

**Files:**
- Modify: `internal/backup/r2.go`, `internal/backup/retention_apply.go`
- Test: `tests/integration/backup_targets_test.go`, `tests/integration/retention_test.go`

- [x] Add fake-object tests for same-size final-object corruption and for `incoming` objects being excluded from retention.
- [x] Run them to observe the current false-success/deletion behavior.
- [x] Stream final R2 content with a bounded digest check before deleting the temporary object; restrict retention to final keys directly under the configured prefix.
- [x] Run focused backup target and retention tests.

### Task 5: Make instance identity part of the snapshot

**Files:**
- Modify: `internal/backup/runner.go`
- Test: `tests/integration/backup_local_test.go`

- [x] Add a first-backup regression that restores or inspects the snapshot and requires `system_state.instance_id` to match the manifest.
- [x] Run it to confirm the snapshot currently precedes identity creation.
- [x] Ensure the instance ID exists before taking the online snapshot and use the same value in the manifest.
- [x] Run focused local backup tests.

### Task 6: Audit M05 operations and add time filtering

**Files:**
- Modify: `internal/audit/events.go`, `internal/backup/runner.go`, `internal/backup/jobs.go`, `internal/httpapi/backups.go`, `internal/httpapi/audit.go`, `cmd/tiny-password/restore.go`, `internal/platform/sqlite/migrate.go`, `web/src/features/admin/AuditPage.tsx`
- Test: `tests/integration/audit_test.go`, `tests/integration/backups_api_test.go`, frontend admin tests

- [x] Add API regressions for backup configuration/run events and audit time filtering.
- [x] Run them to confirm the current endpoints expose no such events or time parameters.
- [x] Add display-safe event constants and record success/failure at the operation boundaries; add validated `from`/`to` UTC filters to API and UI.
- [x] Run Go audit/API tests, frontend typecheck, and admin tests.

### Final verification

- [x] Run `go vet ./...`.
- [x] Run `SEVENZIP_BIN=/tmp/tp-7zz/7zz go test ./... -count=1` (integration rerun passed; one earlier full-run attempt hit a documented flaky 503).
- [x] Run frontend typecheck/tests/build.
- [x] Record any external validation still blocked by missing real R2 credentials.
