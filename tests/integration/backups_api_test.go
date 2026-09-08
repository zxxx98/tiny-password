package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tiny-password/tiny-password/internal/audit"
	"github.com/tiny-password/tiny-password/internal/auth"
	"github.com/tiny-password/tiny-password/internal/backup"
	"github.com/tiny-password/tiny-password/internal/bootstrap"
	"github.com/tiny-password/tiny-password/internal/httpapi"
	"github.com/tiny-password/tiny-password/internal/scheduler"
	"github.com/tiny-password/tiny-password/internal/settings"
)

// backupsAPIHarness exposes the T27 admin endpoints over HTTP on top of a
// real backup runner and the shared auth harness.
type backupsAPIHarness struct {
	*authHarness
	runner       *backup.Runner
	admin        authClient
	member       authClient
	localDir     string
	r2AccessPath string
	r2SecretPath string
	entered      chan struct{}
	release      chan struct{}
	sched        *scheduler.Scheduler
}

// newBackupsAPIHarness builds the plain harness (no fault injection).
func newBackupsAPIHarness(t *testing.T) *backupsAPIHarness {
	return newBackupsAPIHarnessWithHooks(t, backup.Hooks{})
}

// newBackupsAPIHarnessWithBlockingArchive installs an AfterArchiveCreated
// hook that blocks every run until released (manual-run busy tests). The
// hook's channels are re-pointed onto the harness so tests can wait and
// release through the same pair.
func newBackupsAPIHarnessWithBlockingArchive(t *testing.T) *backupsAPIHarness {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	h := newBackupsAPIHarnessWithHooks(t, backup.Hooks{
		AfterArchiveCreated: func(_ context.Context, _ string) error {
			entered <- struct{}{}
			<-release
			return nil
		},
	})
	h.entered, h.release = entered, release
	return h
}

func newBackupsAPIHarnessWithHooks(t *testing.T, hooks backup.Hooks) *backupsAPIHarness {
	t.Helper()
	archiveBin(t)
	h := &backupsAPIHarness{authHarness: newAuthHarness(t), localDir: t.TempDir()}
	h.entered, h.release = make(chan struct{}, 1), make(chan struct{})

	// R2 credential sources start absent: the target is unconfigured until a
	// test supplies them.
	h.r2AccessPath = filepath.Join(t.TempDir(), "r2_access_key")
	h.r2SecretPath = filepath.Join(t.TempDir(), "r2_secret_key")
	settingsService := settings.NewService(h.db.DB)

	runner, err := backup.NewRunner(backup.Options{
		DB:                  h.db,
		WorkDir:             t.TempDir(),
		AppVersion:          "test",
		MasterKeyRaw:        bytes.Repeat([]byte{0x11}, 32),
		Now:                 func() time.Time { return time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC) },
		ScheduledPassphrase: "backup-passphrase-1",
		ScheduledLocalDir:   h.localDir,
		R2:                  backup.NewSettingsR2Resolver(h.db.DB, settingsService, h.r2AccessPath, h.r2SecretPath),
		Audit:               audit.NewService(audit.Options{}),
		Hooks:               hooks,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.runner = runner

	csrf := httpapi.NewPreAuthCSRF(false)
	cursor, err := httpapi.NewCursorCodec([]byte(strings.Repeat("c", 32)), 0)
	if err != nil {
		t.Fatal(err)
	}
	sched, err := scheduler.New(scheduler.Options{
		DB:     h.db.DB,
		Now:    func() time.Time { return time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC) },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	h.sched = sched
	// A dedicated bootstrap instance whose logger captures the one-time token.
	logger := &capturedHandler{}
	boot, err := bootstrap.NewService(h.db, mustMasterKey(t), slog.New(logger))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(httpapi.New(httpapi.Options{
		Setup: &httpapi.SetupDeps{Service: boot, CSRF: csrf, Logger: slogDiscard()},
		Auth:  &httpapi.AuthDeps{Service: h.svc, CSRF: csrf},
		Audit: &httpapi.AuditDeps{Service: audit.NewService(audit.Options{}), DB: h.db, Cursor: cursor, Session: h.svc},
		Backups: &httpapi.BackupsDeps{
			Runner:     runner,
			DB:         h.db,
			LocalDir:   h.localDir,
			R2:         backup.NewSettingsR2Resolver(h.db.DB, settingsService, h.r2AccessPath, h.r2SecretPath),
			Passphrase: "backup-passphrase-1",
			Session:    h.svc,
			Cursor:     cursor,
			Scheduler:  sched,
			Audit:      audit.NewService(audit.Options{}),
		},
		Settings: &httpapi.SettingsDeps{
			Settings:  settingsService,
			Session:   h.svc,
			Scheduler: sched,
			Version:   "test",
			Ready:     func() (map[string]bool, bool) { return map[string]bool{"database": true}, true },
			R2CredentialsPresent: func() bool {
				return backup.R2CredentialsPresent(h.r2AccessPath, h.r2SecretPath)
			},
		},
	}))
	t.Cleanup(srv.Close)
	h.server = srv

	// One admin and one member.
	token := logger.tokenValue()
	if token == "" {
		t.Fatal("no setup token logged")
	}
	if _, err := boot.Initialize(bootstrap.InitializeInput{Token: token, Username: "Admin", Password: validPassword}); err != nil {
		t.Fatal(err)
	}
	h.admin, _ = h.login(t, "Admin", validPassword)
	// A directly inserted member (same trick as the items harness).
	hash, err := auth.HashPassword(validPassword)
	if err != nil {
		t.Fatal(err)
	}
	at := h.clock.Now().UTC().Format(time.RFC3339Nano)
	if _, err := h.db.Exec(`INSERT INTO users(id,username_norm,username_display,role,status,must_change_password,password_hash,created_at,updated_at)
		VALUES('member-1','member','member','member','active',0,?,?,?)`, hash, at, at); err != nil {
		t.Fatal(err)
	}
	h.member, _ = h.login(t, "Member", validPassword)
	return h
}

func TestBackupsAdminAuthorizationMatrix(t *testing.T) {
	h := newBackupsAPIHarness(t)
	for _, tc := range []struct {
		method, path string
	}{
		{"GET", "/admin/backups/jobs"},
		{"PUT", "/admin/backups/jobs"},
		{"GET", "/admin/backups/runs"},
		{"POST", "/admin/backups/run"},
		{"GET", "/admin/settings"},
		{"PUT", "/admin/settings"},
	} {
		resp := h.request(t, tc.method, tc.path, map[string]any{}, h.member)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s %s as member: %d, want 403", tc.method, tc.path, resp.StatusCode)
		}
	}
	// Admin may read.
	resp := h.request(t, "GET", "/admin/backups/jobs", nil, h.admin)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin jobs list: %d", resp.StatusCode)
	}
}

func TestBackupsJobsConfigRoundTrip(t *testing.T) {
	h := newBackupsAPIHarness(t)
	update := map[string]any{
		"target": "local", "enabled": true,
		"schedule_time": "03:30", "schedule_timezone": "Asia/Shanghai",
		"retention_daily": 5, "retention_weekly": 3, "retention_monthly": 2,
	}
	resp := h.request(t, "PUT", "/admin/backups/jobs", update, h.admin)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update: %d", resp.StatusCode)
	}
	resp.Body.Close()
	var configAuditCount int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE event = ? AND target_id = 'local' AND result = 'success'`, audit.EventBackupConfigUpdated).Scan(&configAuditCount); err != nil {
		t.Fatal(err)
	}
	if configAuditCount != 1 {
		t.Fatalf("backup config audit rows=%d, want 1", configAuditCount)
	}

	resp2 := h.request(t, "GET", "/admin/backups/jobs", nil, h.admin)
	defer resp2.Body.Close()
	var body struct {
		Jobs []struct {
			Target         string `json:"target"`
			Enabled        bool   `json:"enabled"`
			ScheduleTime   string `json:"schedule_time"`
			Timezone       string `json:"schedule_timezone"`
			RetentionDaily int    `json:"retention_daily"`
			DeliveryReady  bool   `json:"delivery_ready"`
		} `json:"jobs"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	var local *struct {
		Target         string `json:"target"`
		Enabled        bool   `json:"enabled"`
		ScheduleTime   string `json:"schedule_time"`
		Timezone       string `json:"schedule_timezone"`
		RetentionDaily int    `json:"retention_daily"`
		DeliveryReady  bool   `json:"delivery_ready"`
	}
	for i := range body.Jobs {
		if body.Jobs[i].Target == "local" {
			j := body.Jobs[i]
			local = &struct {
				Target         string `json:"target"`
				Enabled        bool   `json:"enabled"`
				ScheduleTime   string `json:"schedule_time"`
				Timezone       string `json:"schedule_timezone"`
				RetentionDaily int    `json:"retention_daily"`
				DeliveryReady  bool   `json:"delivery_ready"`
			}{j.Target, j.Enabled, j.ScheduleTime, j.Timezone, j.RetentionDaily, j.DeliveryReady}
		}
	}
	if local == nil || !local.Enabled || local.ScheduleTime != "03:30" || local.Timezone != "Asia/Shanghai" || local.RetentionDaily != 5 || !local.DeliveryReady {
		t.Fatalf("job config: %+v", local)
	}

	// Invalid whitelist values are refused.
	for _, bad := range []map[string]any{
		{"target": "local", "schedule_time": "25:00"},
		{"target": "local", "schedule_timezone": "Mars/Olympus"},
		{"target": "floppy"},
		{"target": "local", "retention_daily": 5000},
	} {
		resp := h.request(t, "PUT", "/admin/backups/jobs", bad, h.admin)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid config accepted: %v -> %d", bad, resp.StatusCode)
		}
	}
}

func TestBackupScheduleUpdateReloadsRunningScheduler(t *testing.T) {
	h := newBackupsAPIHarness(t)
	// The scheduler is already running before this configuration is saved.
	// A past schedule makes the reload observable on the next evaluation.
	resp := h.request(t, "PUT", "/admin/backups/jobs", map[string]any{
		"target": "local", "enabled": true,
		"schedule_time": "00:01", "schedule_timezone": "UTC",
	}, h.admin)
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("enable scheduled backup: %d", resp.StatusCode)
	}
	resp.Body.Close()

	results := h.sched.Evaluate(context.Background())
	if len(results) != 1 || results[0].Job != "backup.local" || results[0].Status != scheduler.StatusSucceeded {
		t.Fatalf("reloaded scheduler results: %+v", results)
	}

	// Disabling the persisted row unregisters the target from subsequent
	// evaluations without affecting unrelated scheduler jobs.
	resp2 := h.request(t, "PUT", "/admin/backups/jobs", map[string]any{
		"target": "local", "enabled": false,
	}, h.admin)
	if resp2.StatusCode != http.StatusOK {
		resp2.Body.Close()
		t.Fatalf("disable scheduled backup: %d", resp2.StatusCode)
	}
	resp2.Body.Close()
	if got := h.sched.Evaluate(context.Background()); len(got) != 0 {
		t.Fatalf("disabled scheduled backup still evaluated: %+v", got)
	}
	if _, ok := h.sched.LastResult("backup.local"); ok {
		t.Fatal("disabled scheduled backup retained a runtime result")
	}
}

func TestBackupsManualRunAndHistory(t *testing.T) {
	h := newBackupsAPIHarnessWithBlockingArchive(t)
	// Hold the mutex with a blocking hook to prove the 409 path.
	resp := h.request(t, "POST", "/admin/backups/run", map[string]any{"targets": []string{"local"}}, h.admin)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run start: %d", resp.StatusCode)
	}
	<-h.entered // the run is now inside the archive hook, mutex held

	resp2 := h.request(t, "POST", "/admin/backups/run", map[string]any{"targets": []string{"local"}}, h.admin)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("second run: %d, want 409 BACKUP_BUSY", resp2.StatusCode)
	}

	close(h.release) // release the first run
	// The run eventually records a succeeded row with size and digest.
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp3 := h.request(t, "GET", "/admin/backups/runs", nil, h.admin)
		var body struct {
			Items []backup.RunRow `json:"items"`
		}
		err := json.NewDecoder(resp3.Body).Decode(&body)
		resp3.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if len(body.Items) > 0 && body.Items[0].Status == "succeeded" && body.Items[0].SizeBytes != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run never succeeded: %+v", body.Items)
		}
		time.Sleep(50 * time.Millisecond)
	}
	var runAuditCount int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE event = ? AND result = 'success'`, audit.EventBackupRun).Scan(&runAuditCount); err != nil {
		t.Fatal(err)
	}
	if runAuditCount == 0 {
		t.Fatal("successful backup run was not audited")
	}
}

func TestSettingsWhitelistAndAuditResultFilter(t *testing.T) {
	h := newBackupsAPIHarness(t)
	// Valid update.
	resp := h.request(t, "PUT", "/admin/settings", map[string]any{
		"r2_endpoint": "https://acc123.r2.cloudflarestorage.com",
		"r2_bucket":   "my-backups",
		"r2_prefix":   "tiny-password",
	}, h.admin)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("settings update: %d", resp.StatusCode)
	}
	// Invalid values refused.
	resp2 := h.request(t, "PUT", "/admin/settings", map[string]any{
		"r2_endpoint": "http://insecure.example.com",
	}, h.admin)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid settings accepted: %d", resp2.StatusCode)
	}
	// GET never contains credential material.
	resp3 := h.request(t, "GET", "/admin/settings", nil, h.admin)
	defer resp3.Body.Close()
	var body struct {
		Settings map[string]any `json:"settings"`
	}
	if err := json.NewDecoder(resp3.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	// The settings object carries exactly the non-sensitive whitelist; no
	// credential fields can ever appear.
	if len(body.Settings) != 3 {
		t.Fatalf("settings keys: %v", body.Settings)
	}
	for _, key := range []string{"r2_endpoint", "r2_bucket", "r2_prefix"} {
		if _, ok := body.Settings[key]; !ok {
			t.Fatalf("missing whitelisted key %s: %v", key, body.Settings)
		}
	}
	// Audit result filter.
	seedAuditSuccessAndFailure(t, h.db.DB)
	var probe struct {
		Items []struct {
			Event  string `json:"event"`
			Result string `json:"result"`
		} `json:"items"`
	}
	respAll := h.request(t, "GET", "/admin/audit", nil, h.admin)
	if err := json.NewDecoder(respAll.Body).Decode(&probe); err != nil {
		t.Fatal(err)
	}
	respAll.Body.Close()
	t.Logf("all events: %+v", probe.Items)

	resp4 := h.request(t, "GET", "/admin/audit?result=failure", nil, h.admin)
	defer resp4.Body.Close()
	var auditBody struct {
		Items []struct {
			Result string `json:"result"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp4.Body).Decode(&auditBody); err != nil {
		t.Fatal(err)
	}
	if len(auditBody.Items) == 0 {
		t.Fatal("no failure rows returned")
	}
	for _, item := range auditBody.Items {
		if item.Result != "failure" {
			t.Fatalf("result filter leaked: %+v", auditBody.Items)
		}
	}
}

// jobReady decodes the jobs listing and reports the named target's
// delivery_ready flag.
func jobReady(t *testing.T, h *backupsAPIHarness, client authClient, target string) bool {
	t.Helper()
	resp := h.request(t, "GET", "/admin/backups/jobs", nil, client)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("jobs list: %d", resp.StatusCode)
	}
	var body struct {
		Jobs []struct {
			Target        string `json:"target"`
			DeliveryReady bool   `json:"delivery_ready"`
		} `json:"jobs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	for _, j := range body.Jobs {
		if j.Target == target {
			return j.DeliveryReady
		}
	}
	t.Fatalf("target %s missing from jobs listing", target)
	return false
}

// credentialsFlag decodes GET /admin/settings and reports the
// source-neutral R2 credential presence flag.
func credentialsFlag(t *testing.T, h *backupsAPIHarness, client authClient) bool {
	t.Helper()
	resp := h.request(t, "GET", "/admin/settings", nil, client)
	defer resp.Body.Close()
	var body struct {
		R2CredentialsConfigured bool `json:"r2_credentials_configured"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body.R2CredentialsConfigured
}

func TestBackupsR2CredentialsConfiguredFromEnvironment(t *testing.T) {
	h := newBackupsAPIHarness(t)
	if credentialsFlag(t, h, h.admin) {
		t.Fatal("credentials reported present before configuration")
	}

	t.Setenv("TP_R2_ACCESS_KEY", "env-test-access")
	t.Setenv("TP_R2_SECRET_KEY", "env-test-secret")
	if !credentialsFlag(t, h, h.admin) {
		t.Fatal("environment credentials reported absent")
	}
}

// The R2 delivery configuration is resolved at request time from the admin
// settings store plus runtime credential sources (T27): before configuration
// the manual run is refused with MAINTENANCE and the target is not
// delivery-ready; afterwards the same endpoint delivers through the
// settings-resolved client without a restart.
func TestBackupsR2DeliveryResolvedFromSettings(t *testing.T) {
	h := newBackupsAPIHarness(t)

	if jobReady(t, h, h.admin, "r2") {
		t.Fatal("r2 must not be delivery-ready before configuration")
	}
	resp := h.request(t, "POST", "/admin/backups/run", map[string]any{"targets": []string{"r2"}}, h.admin)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured r2 run: %d, want 503 MAINTENANCE", resp.StatusCode)
	}
	resp.Body.Close()

	if credentialsFlag(t, h, h.admin) {
		t.Fatal("credentials reported present before mounting")
	}

	// Mount the credential files and store the non-sensitive settings half.
	// The fake server speaks plain HTTP, so the settings row is seeded
	// directly; the https whitelist itself is covered by
	// TestSettingsWhitelistAndAuditResultFilter.
	fake := newFakeR2()
	server := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer server.Close()
	if err := os.WriteFile(h.r2AccessPath, []byte("test-access\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.r2SecretPath, []byte("test-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.DB.Exec(
		`INSERT INTO app_settings (key, value, updated_at) VALUES ('settings.runtime', ?, ?)`,
		fmt.Sprintf(`{"r2_endpoint":%q,"r2_bucket":"test-bucket","r2_prefix":"backups/test-instance"}`, server.URL),
		time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		t.Fatal(err)
	}

	if !credentialsFlag(t, h, h.admin) {
		t.Fatal("credentials reported absent after mounting")
	}
	if !jobReady(t, h, h.admin, "r2") {
		t.Fatal("r2 must be delivery-ready after settings + credentials")
	}

	resp2 := h.request(t, "POST", "/admin/backups/run", map[string]any{"targets": []string{"r2"}}, h.admin)
	if resp2.StatusCode != http.StatusAccepted {
		t.Fatalf("configured r2 run: %d", resp2.StatusCode)
	}
	resp2.Body.Close()

	deadline := time.Now().Add(30 * time.Second)
	for {
		resp3 := h.request(t, "GET", "/admin/backups/runs?target=r2", nil, h.admin)
		var body struct {
			Items []backup.RunRow `json:"items"`
		}
		err := json.NewDecoder(resp3.Body).Decode(&body)
		resp3.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		done := len(body.Items) > 0 && body.Items[0].Status == "succeeded"
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("r2 run never succeeded: %+v", body.Items)
		}
		time.Sleep(50 * time.Millisecond)
	}

	finals := 0
	for k := range fake.objects {
		if !strings.HasPrefix(k, "backups/test-instance/") {
			t.Fatalf("unexpected object %s", k)
		}
		if !strings.Contains(k, "/incoming/") {
			finals++
		}
	}
	if finals != 1 {
		t.Fatalf("final objects: %d", finals)
	}
}

func seedAuditSuccessAndFailure(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, e := range []struct {
		id     string
		result string
	}{
		{"audit-s-1", "success"},
		{"audit-f-1", "failure"},
	} {
		if _, err := db.Exec(
			`INSERT INTO audit_events (id, event, actor_id, result, created_at) VALUES (?, 'auth.login.success', 'anonymous', ?, ?)`,
			e.id, e.result, time.Now().UTC().Format(time.RFC3339Nano),
		); err != nil {
			t.Fatal(err)
		}
	}
}
