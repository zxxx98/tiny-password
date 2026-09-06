package integration

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/tiny-password/tiny-password/internal/audit"
	"github.com/tiny-password/tiny-password/internal/backup"
	"github.com/tiny-password/tiny-password/internal/idempotency"
	"github.com/tiny-password/tiny-password/internal/scheduler"
	"github.com/tiny-password/tiny-password/internal/vault"
)

// seedRunningRuns inserts lifecycle rows that a crashed process would have
// left behind.
func seedRunningRuns(t *testing.T, h *backupHarness) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, target := range []string{"local", "r2"} {
		if _, err := h.db.Exec(
			`INSERT INTO backup_runs (id, target, status, triggered_by, started_at)
			 VALUES (?, ?, 'running', 'scheduled', ?)`,
			"run-crash-"+target, target, now,
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.db.Exec(
		`INSERT INTO backup_runs (id, target, status, triggered_by, started_at)
		 VALUES ('run-crash-pending', 'local', 'pending', 'manual', ?)`, now,
	); err != nil {
		t.Fatal(err)
	}
}

func TestJobRestartMarksInterruptedRuns(t *testing.T) {
	h := newBackupHarness(t)
	seedRunningRuns(t, h)

	// A completed row must be untouched by the restart sweep.
	if _, err := h.db.Exec(
		`INSERT INTO backup_runs (id, target, status, triggered_by, started_at, finished_at, size_bytes, sha256)
		 VALUES ('run-done', 'local', 'succeeded', 'manual', ?, ?, 10, 'aa')`,
		time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		t.Fatal(err)
	}

	marked, err := backup.MarkInterruptedRuns(h.db.DB, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if marked != 3 {
		t.Fatalf("marked=%d, want 3", marked)
	}
	var statuses []string
	rows, err := h.db.Query(
		"SELECT id, status, COALESCE(error_code,'') FROM backup_runs ORDER BY id",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, status, code string
		if err := rows.Scan(&id, &status, &code); err != nil {
			t.Fatal(err)
		}
		if id != "run-done" {
			if status != "interrupted" || code != "BACKUP_INTERRUPTED" {
				t.Fatalf("%s: %s/%s", id, status, code)
			}
		} else if status != "succeeded" {
			t.Fatalf("completed row rewritten: %s", status)
		}
		statuses = append(statuses, status)
	}
	// Re-running the sweep is safe and changes nothing.
	if marked, err := backup.MarkInterruptedRuns(h.db.DB, time.Now()); err != nil || marked != 0 {
		t.Fatalf("second sweep: %d %v", marked, err)
	}
	_ = statuses
}

// A scheduled backup that is still running holds the process mutex: the
// scheduler's next tick skips (busy) and a manual run is refused with
// ErrBusy. After completion, the day's marker prevents a second scheduled
// execution.
func TestBackupOverlap(t *testing.T) {
	h := newBackupHarness(t)
	entered := make(chan chan struct{})
	release := make(chan chan struct{})
	hooks := backup.Hooks{
		AfterArchiveCreated: func(_ context.Context, _ string) error {
			ack := make(chan struct{})
			entered <- ack
			<-ack
			<-release
			return nil
		},
	}
	runner, err := backup.NewRunner(backup.Options{
		DB:                  h.db,
		WorkDir:             h.workDir,
		AppVersion:          "test",
		MasterKeyRaw:        h.keyRaw,
		Hooks:               hooks,
		ScheduledPassphrase: "backup-passphrase-1",
		ScheduledLocalDir:   h.localDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Seed the per-target configuration, then enable the local target
	// with a daily schedule.
	if _, err := backup.LoadJobConfigs(h.db.DB); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(
		`UPDATE backup_jobs SET enabled = 1, schedule_time = '03:00', schedule_timezone = 'UTC'
		 WHERE target = 'local'`,
	); err != nil {
		t.Fatal(err)
	}

	clock := time.Date(2026, 9, 6, 4, 0, 0, 0, time.UTC) // past 03:00
	sched, err := scheduler.New(scheduler.Options{
		DB:     h.db.DB,
		Now:    func() time.Time { return clock },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := runner.ScheduledJobs()
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("scheduled jobs=%d, want 1", len(jobs))
	}
	if err := sched.Register(jobs[0]); err != nil {
		t.Fatal(err)
	}

	done := make(chan []scheduler.Result, 1)
	go func() { done <- sched.Evaluate(context.Background()) }()
	ack := <-entered

	// Manual run during the scheduled run: busy.
	if _, err := runner.Run(context.Background(), backup.RunInput{
		Targets:    []backup.Target{backup.TargetLocal},
		Trigger:    backup.TriggerManual,
		Passphrase: "backup-passphrase-1",
		LocalDir:   h.localDir,
	}); err == nil {
		t.Fatal("manual run should be busy")
	} else if err.Error() != backup.ErrBusy.Error() {
		t.Fatalf("manual run error: %v", err)
	}

	// The scheduler's own next tick skips as busy too.
	if res := sched.Evaluate(context.Background()); len(res) != 1 || res[0].Status != scheduler.StatusSkipped {
		t.Fatalf("tick during run: %+v", res)
	}

	close(ack)
	close(release)
	res := <-done
	if len(res) != 1 || res[0].Status != scheduler.StatusSucceeded {
		t.Fatalf("scheduled result: %+v", res)
	}
	// The day's scheduled run happened exactly once.
	if res := sched.Evaluate(context.Background()); len(res) != 0 {
		t.Fatalf("scheduled run repeated same day: %+v", res)
	}
	if status, _ := h.lastRun(t); status != "succeeded" {
		t.Fatalf("scheduled run row: %s", status)
	}
}

// The maintenance sweeps remove expired trash, sessions, rate-limit rows
// and idempotency claims, and run the WAL checkpoint; they are idempotent.
func TestMaintenanceJobs(t *testing.T) {
	h := newBackupHarness(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// A user whose session and trash item expire.
	if _, err := h.db.Exec(
		`INSERT INTO users (id, username_norm, username_display, role, status, must_change_password, password_hash, created_at, updated_at)
		 VALUES ('maint-user', 'maint-user', 'maint-user', 'member', 'active', 0, 'synthetic', ?, ?)`,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
	); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-40 * 24 * time.Hour).Format(time.RFC3339Nano)
	fresh := now.Format(time.RFC3339Nano)
	// Trash item past retention (31 days) and one inside it.
	for _, id := range []string{"item-gone", "item-keep"} {
		deleted := old
		if id == "item-keep" {
			deleted = fresh
		}
		if _, err := h.db.Exec(
			`INSERT INTO vault_items (id, vault_scope, owner_user_id, item_type, favorite, payload_version, nonce, ciphertext, revision, created_at, updated_at, deleted_at)
			 VALUES (?, 'personal', 'maint-user', 'secure_note', 0, 1, x'00', x'00', 1, ?, ?, ?)`,
			id, old, old, deleted,
		); err != nil {
			t.Fatal(err)
		}
	}
	// Expired session and a still-valid one (public_id is the unique
	// public handle; the credential id stays opaque).
	if _, err := h.db.Exec(
		`INSERT INTO sessions (id, user_id, created_at, absolute_expires_at, idle_expires_at, public_id)
		 VALUES ('sess-gone', 'maint-user', ?, ?, ?, 'maint-gone'),
		        ('sess-keep', 'maint-user', ?, ?, ?, 'maint-keep')`,
		old, old, old, fresh,
		now.Add(time.Hour).Format(time.RFC3339Nano), now.Add(time.Hour).Format(time.RFC3339Nano),
	); err != nil {
		t.Fatal(err)
	}
	// Stale rate-limit rows and an expired idempotency claim.
	if _, err := h.db.Exec(
		`INSERT INTO login_attempts (username_norm, source_hash, outcome, created_at)
		 VALUES (x'00', x'00', 'failure', ?), (x'01', x'01', 'failure', ?)`,
		old, fresh,
	); err != nil {
		t.Fatal(err)
	}
	idem, err := idempotency.NewService(h.db.DB, idempotency.Options{MACKey: newIdempotencyKeyMaterial()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(
		`INSERT INTO idempotency_keys (scope, key, fingerprint, status, created_at, expires_at)
		 VALUES ('items.create:t1', 'k1', x'00', 'completed', ?, ?)`,
		old, old,
	); err != nil {
		t.Fatal(err)
	}

	vsvc, err := vault.NewService(h.db.DB, mustMasterKey(t), vault.Options{Audit: audit.NewService(audit.Options{})})
	if err != nil {
		t.Fatal(err)
	}
	jobs := backup.MaintenanceJobs(backup.MaintenanceDeps{
		DB:          h.db,
		Vault:       vsvc,
		Idempotency: idem,
	})
	if len(jobs) != 5 {
		t.Fatalf("maintenance jobs=%d, want 5", len(jobs))
	}
	for _, job := range jobs {
		if err := job.Fn(ctx); err != nil {
			t.Fatalf("%s: %v", job.Name, err)
		}
	}

	// Expired rows are gone; fresh rows survive.
	for query, want := range map[string]int{
		"SELECT COUNT(*) FROM vault_items WHERE id = 'item-gone'":              0,
		"SELECT COUNT(*) FROM vault_items WHERE id = 'item-keep'":              1,
		"SELECT COUNT(*) FROM sessions WHERE id = 'sess-gone'":                 0,
		"SELECT COUNT(*) FROM sessions WHERE id = 'sess-keep'":                 1,
		"SELECT COUNT(*) FROM login_attempts WHERE created_at = '" + old + "'": 0,
		"SELECT COUNT(*) FROM idempotency_keys WHERE key = 'k1'":               0,
	} {
		var n int
		if err := h.db.QueryRow(query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Fatalf("%s: got %d, want %d", query, n, want)
		}
	}

	// Re-running every sweep changes nothing.
	for _, job := range jobs {
		if err := job.Fn(ctx); err != nil {
			t.Fatalf("%s rerun: %v", job.Name, err)
		}
	}
}
