# R2 Environment Credentials Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox ( - [ ] ) syntax for tracking.

**Goal:** Allow R2 Access Key ID and Secret Access Key to be supplied through runtime environment variables while retaining the existing credential-file deployment path.

**Architecture:** The R2 resolver will resolve each credential independently from a non-empty environment variable first, then from its existing file path. Endpoint, bucket, and prefix remain in the admin settings store; only a boolean configured state is exposed to the UI. Compose will pass the two optional environment variables through to the app, while deploy/compose.r2.yaml remains the file-based compatibility path.

**Tech Stack:** Go 1.26, database/sql, SQLite settings service, Docker Compose, React/TypeScript, Vitest.

---

## File map

- Modify internal/backup/r2resolve.go: add environment-variable constants and credential resolution used by both delivery construction and the admin presence check.
- Create internal/backup/r2resolve_test.go: test environment precedence, file fallback, missing credentials, and R2 delivery construction against a migrated SQLite settings database.
- Modify cmd/tiny-password/main.go: update the wiring comment to describe both supported credential sources.
- Modify internal/httpapi/settings.go: return a generic r2_credentials_configured boolean instead of a file-specific status name.
- Modify web/src/features/admin/SettingsPage.tsx: consume the generic status field and render whether credentials are configured without claiming they came from a file.
- Modify tests/integration/backups_api_test.go: decode the renamed generic settings status and add environment-backed resolver coverage to the existing R2 settings/backup harness.
- Modify web/src/features/admin/admin.test.tsx: update mocked settings payloads and assert the generic status text.
- Modify compose.yaml: pass TP_R2_ACCESS_KEY and TP_R2_SECRET_KEY into the app container as optional environment variables.
- Modify docs/operations/deploy.md: document direct environment configuration, precedence, validation, and the file-based override.
- Modify docs/operations/secrets.md: document both credential sources and the accepted environment-variable exposure trade-off.

### Task 1: Add failing resolver tests

Files:
- Create: internal/backup/r2resolve_test.go

- [ ] Step 1: Add a migrated-settings test helper and environment test cases.

Create a SQLite database using sqlite.Open, migrate it with migrations.FS, write valid R2 settings with settings.NewService, and use absent temporary paths for the credential files. The tests must set and clear TP_R2_ACCESS_KEY and TP_R2_SECRET_KEY with t.Setenv, never print their values, and assert only whether a delivery is nil or non-nil.

The core table must cover these cases:

    tests := []struct {
        name       string
        accessEnv  string
        secretEnv  string
        accessFile string
        secretFile string
        wantReady  bool
    }{
        {name: "environment credentials", accessEnv: "env-access", secretEnv: "env-secret", wantReady: true},
        {name: "file fallback", accessFile: accessPath, secretFile: secretPath, wantReady: true},
        {name: "environment overrides files", accessEnv: "env-access", secretEnv: "env-secret", accessFile: accessPath, secretFile: secretPath, wantReady: true},
        {name: "only access credential", accessEnv: "env-access", wantReady: false},
        {name: "no credentials", wantReady: false},
    }

For every ready case, call the resolver with context.Background() and assert a non-nil *R2Delivery; for every missing case assert a nil delivery and nil error. For the environment-override case, make both file paths directories rather than readable files so the test proves the resolver does not read a lower-priority file when both environment values are present.

Add a separate R2CredentialsPresent test using the same source combinations and assert that its boolean matches the resolver readiness. The test should also verify that whitespace-only environment variables fall back to the file source.

- [ ] Step 2: Run the new tests and verify the expected RED failure.

Run:

    go test ./internal/backup -run 'TestR2Resolver|TestR2Credentials' -count=1

Expected: the new environment-backed cases fail because the current resolver reads only credential files; the existing file fallback case remains green.

- [ ] Step 3: Commit only the test file.

    git add internal/backup/r2resolve_test.go
    git commit -m "test: cover R2 environment credentials"

### Task 2: Implement environment-first credential resolution

Files:
- Modify: internal/backup/r2resolve.go:18-86
- Modify: cmd/tiny-password/main.go:163-175

- [ ] Step 1: Add stable environment names and one fallback helper.

Add constants:

    const (
        R2AccessKeyEnv = "TP_R2_ACCESS_KEY"
        R2SecretKeyEnv = "TP_R2_SECRET_KEY"
    )

Add a helper that trims an environment value and only calls ReadOptionalSecretFile when the environment value is empty:

    func readR2Credential(envName, filePath string) (string, error) {
        if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
            return value, nil
        }
        return ReadOptionalSecretFile(filePath)
    }

Use the helper separately for access and secret keys inside NewSettingsR2Resolver and R2CredentialsPresent. Keep existing behavior for absent files, unreadable lower-priority files, incomplete credentials, and invalid endpoint/bucket settings. Do not include credential values in errors or logs.

- [ ] Step 2: Run the resolver tests and verify GREEN.

Run:

    gofmt -w internal/backup/r2resolve.go internal/backup/r2resolve_test.go
    go test ./internal/backup -run 'TestR2Resolver|TestR2Credentials' -count=1

Expected: all new resolver cases pass.

- [ ] Step 3: Update the main wiring comment.

Change the comment above NewSettingsR2Resolver to state that endpoint/bucket/prefix come from admin settings and credentials come from TP_R2_ACCESS_KEY/TP_R2_SECRET_KEY or the configured credential files. No main signature change is needed because the resolver reads the process environment at run time.

- [ ] Step 4: Commit the resolver implementation.

    git add internal/backup/r2resolve.go cmd/tiny-password/main.go
    git commit -m "feat: support R2 credentials from environment"

### Task 3: Make the runtime status source-neutral

Files:
- Modify: internal/httpapi/settings.go:44-51
- Modify: web/src/features/admin/SettingsPage.tsx:23-29,143-146
- Modify: tests/integration/backups_api_test.go:453-465
- Modify: web/src/features/admin/admin.test.tsx settings fixtures and status assertion

- [ ] Step 1: Add the failing integration assertion for environment-backed status.

In tests/integration/backups_api_test.go, extend the existing credentials flag coverage so that it sets both environment variables while the harness credential file paths are absent, then calls GET /admin/settings and expects the status to be true. Decode the new JSON field as:

    var body struct {
        R2CredentialsConfigured bool `json:"r2_credentials_configured"`
    }

Use t.Setenv and clear both variables in the test cleanup provided by t.Setenv; do not assert or log secret contents.

- [ ] Step 2: Run the focused integration test and verify RED.

Run:

    go test ./tests/integration -run 'TestSettings|TestBackupsR2|Test.*Credentials' -count=1

Expected: the environment-backed status assertion fails because the API currently emits only r2_credentials_via_file and the presence helper checks only files.

- [ ] Step 3: Rename the API status field and update the frontend.

In internal/httpapi/settings.go, emit:

    "r2_credentials_configured": deps.R2CredentialsPresent != nil && deps.R2CredentialsPresent(),

In SettingsPage.tsx, rename the SystemInfo property and render 已配置 when true and 未配置 when false. Update all admin test fixtures to use r2_credentials_configured and assert that the status does not say “Secret 文件” for an environment-backed configuration.

- [ ] Step 4: Run the focused backend and frontend tests.

Run:

    go test ./tests/integration -run 'TestSettings|TestBackupsR2|Test.*Credentials' -count=1
    npm --prefix web test -- --run src/features/admin/admin.test.tsx

Expected: focused backend tests and admin frontend tests pass.

- [ ] Step 5: Commit the source-neutral status change.

    git add internal/httpapi/settings.go tests/integration/backups_api_test.go web/src/features/admin/SettingsPage.tsx web/src/features/admin/admin.test.tsx
    git commit -m "feat: report R2 credential configuration generically"

### Task 4: Pass environment variables through Compose and document operation

Files:
- Modify: compose.yaml:16-21
- Modify: docs/operations/deploy.md:96-103
- Modify: docs/operations/secrets.md:7-31

- [ ] Step 1: Add optional Compose environment passthrough.

Add quoted optional mappings under services.app.environment:

    TP_R2_ACCESS_KEY: "${TP_R2_ACCESS_KEY:-}"
    TP_R2_SECRET_KEY: "${TP_R2_SECRET_KEY:-}"

Keep deploy/compose.r2.yaml unchanged for file-based credentials. An empty environment value must preserve file fallback.

- [ ] Step 2: Document both deployment modes.

In docs/operations/deploy.md, document:

1. Environment mode: provide TP_R2_ACCESS_KEY and TP_R2_SECRET_KEY through the deployment environment, run docker compose -f compose.yaml config --quiet, then recreate the app with docker compose -f compose.yaml up -d --build --force-recreate.
2. File mode: set TP_R2_ACCESS_KEY_SOURCE and TP_R2_SECRET_KEY_SOURCE, run TP_CHECK_R2=1 bash scripts/check-compose-secrets.sh, and use -f deploy/compose.r2.yaml.
3. Both modes still require the admin page's HTTPS endpoint, bucket, and prefix; the endpoint must be the account-specific R2 S3 endpoint shown by Cloudflare, and the R2 signing region remains auto in the application.

In docs/operations/secrets.md, state that environment values are intentionally supported for controlled internal deployments but can be exposed through docker inspect, /proc, process dumps, or deployment diagnostics; do not put them in Git or unprotected CI output. Keep the existing Docker Secret recommendation and file paths.

- [ ] Step 3: Validate Compose rendering without printing secret values.

Run:

    docker compose -f compose.yaml config --quiet
    docker compose -f compose.yaml -f deploy/compose.r2.yaml config --quiet
    git diff --check

Expected: all commands exit 0. Do not run plain docker compose config with real credentials because it renders interpolated values.

- [ ] Step 4: Commit Compose and documentation changes.

    git add compose.yaml docs/operations/deploy.md docs/operations/secrets.md
    git commit -m "docs: document R2 environment credential deployment"

### Task 5: Full verification and handoff

Files:
- No additional source files; inspect the complete diff and existing dirty-worktree changes without reverting them.

- [ ] Step 1: Run Go formatting, vet, and tests.

    gofmt -w internal/backup/r2resolve.go internal/backup/r2resolve_test.go
    go vet ./...
    go test ./... -count=1 -timeout=25m

Expected: exit code 0, no test failures, and no credential values in output.

- [ ] Step 2: Run frontend verification.

    npm --prefix web run typecheck
    npm --prefix web test -- --run
    npm --prefix web run build

Expected: typecheck/build exit 0 and all frontend tests pass.

- [ ] Step 3: Review the scoped diff and status.

    git diff --check
    git status --short
    git diff -- internal/backup/r2resolve.go cmd/tiny-password/main.go internal/httpapi/settings.go web/src/features/admin/SettingsPage.tsx compose.yaml docs/operations/deploy.md docs/operations/secrets.md

Confirm that only the planned R2 files are in the implementation commits, existing user changes remain intact, the admin API never returns credential values, and the environment names are exactly TP_R2_ACCESS_KEY and TP_R2_SECRET_KEY.
