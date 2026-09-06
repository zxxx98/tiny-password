package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	tpsqlite "github.com/tiny-password/tiny-password/internal/platform/sqlite"
	"github.com/tiny-password/tiny-password/migrations"
)

// newTestScheduler opens a migrated SQLite database and a scheduler whose
// clock the test controls.
func newTestScheduler(t *testing.T, start time.Time) (*Scheduler, *time.Time, func(time.Time)) {
	t.Helper()
	dir := t.TempDir()
	db, err := tpsqlite.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := tpsqlite.Migrate(db.DB, migrations.FS); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	current := start
	s, err := New(Options{
		DB:     db.DB,
		Now:    func() time.Time { return current },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, &current, func(t time.Time) { current = t }
}

// The scheduler is due-driven: a job fires exactly once when the clock
// crosses its scheduled time, in UTC by default.
func TestDailyJobRunsOnceAtScheduledTimeUTC(t *testing.T) {
	start := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	s, _, setClock := newTestScheduler(t, start)
	runs := 0
	if err := s.Register(Job{Name: "nightly", DailyAt: "03:30", Fn: func(context.Context) error {
		runs++
		return nil
	}}); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	// Before the scheduled time: nothing runs.
	setClock(start.Add(3 * time.Hour))
	if res := s.Evaluate(ctx); len(res) != 0 {
		t.Fatalf("job ran early: %+v", res)
	}
	// After the scheduled time: exactly one execution.
	setClock(start.Add(3*time.Hour + 31*time.Minute))
	res := s.Evaluate(ctx)
	if len(res) != 1 || res[0].Status != StatusSucceeded || res[0].Ran != true {
		t.Fatalf("evaluate: %+v", res)
	}
	// Re-evaluating the same day must not re-run.
	if res := s.Evaluate(ctx); len(res) != 0 {
		t.Fatalf("job re-ran same day: %+v", res)
	}
	setClock(start.Add(20 * time.Hour))
	if res := s.Evaluate(ctx); len(res) != 0 {
		t.Fatalf("job re-ran later same day: %+v", res)
	}
	// The next day it is due again.
	setClock(start.Add(24*time.Hour + 4*time.Hour))
	res = s.Evaluate(ctx)
	if len(res) != 1 || res[0].Status != StatusSucceeded {
		t.Fatalf("next day evaluate: %+v", res)
	}
	if runs != 2 {
		t.Fatalf("runs=%d, want 2", runs)
	}
}

// A schedule in a fixed-offset zone fires at the corresponding UTC instant.
func TestDailyJobTimezoneAsiaShanghai(t *testing.T) {
	start := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	s, _, setClock := newTestScheduler(t, start)
	if err := s.Register(Job{Name: "cn", Timezone: "Asia/Shanghai", DailyAt: "08:30", Fn: func(context.Context) error {
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	// 08:30 CST == 00:30 UTC. Not due at 00:29 UTC.
	setClock(time.Date(2026, 9, 6, 0, 29, 0, 0, time.UTC))
	if res := s.Evaluate(context.Background()); len(res) != 0 {
		t.Fatalf("ran before CST 08:30: %+v", res)
	}
	// Due at 00:31 UTC.
	setClock(time.Date(2026, 9, 6, 0, 31, 0, 0, time.UTC))
	res := s.Evaluate(context.Background())
	if len(res) != 1 || res[0].Status != StatusSucceeded {
		t.Fatalf("evaluate: %+v", res)
	}
	// The day boundary is the job-local date: at 15:31 UTC it is already
	// 23:31 CST of the same day — no second run.
	setClock(time.Date(2026, 9, 6, 15, 31, 0, 0, time.UTC))
	if res := s.Evaluate(context.Background()); len(res) != 0 {
		t.Fatalf("re-ran same CST day: %+v", res)
	}
}

// Spring forward (America/New_York, 2026-03-08): 02:30 does not exist; the
// job runs at the next valid local instant (03:30 EDT = 07:30 UTC).
func TestDailyJobDSTSpringForwardSkippedTime(t *testing.T) {
	start := time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC)
	s, _, setClock := newTestScheduler(t, start)
	if err := s.Register(Job{Name: "ny", Timezone: "America/New_York", DailyAt: "02:30", Fn: func(context.Context) error {
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	// 07:00 UTC = 02:00 EST, right at the transition: 02:30 local will not
	// exist this day. Not yet due (the next valid instant is 03:30 EDT).
	setClock(time.Date(2026, 3, 8, 6, 45, 0, 0, time.UTC))
	if res := s.Evaluate(context.Background()); len(res) != 0 {
		t.Fatalf("ran before next valid instant: %+v", res)
	}
	// 07:31 UTC = 03:31 EDT — past the normalized 03:30 run time.
	setClock(time.Date(2026, 3, 8, 7, 31, 0, 0, time.UTC))
	res := s.Evaluate(context.Background())
	if len(res) != 1 || res[0].Status != StatusSucceeded {
		t.Fatalf("skipped-time catch-up: %+v", res)
	}
}

// Fall back (America/New_York, 2026-11-01): 01:30 occurs twice; the job
// runs once at the first occurrence (01:30 EDT = 05:30 UTC).
func TestDailyJobDSTFallBackRunsOnce(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	s, _, setClock := newTestScheduler(t, start)
	runs := 0
	if err := s.Register(Job{Name: "ny", Timezone: "America/New_York", DailyAt: "01:30", Fn: func(context.Context) error {
		runs++
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	// First occurrence: 01:30 EDT = 05:30 UTC.
	setClock(time.Date(2026, 11, 1, 5, 31, 0, 0, time.UTC))
	if res := s.Evaluate(context.Background()); len(res) != 1 {
		t.Fatalf("first occurrence not detected: %+v", res)
	}
	// Second occurrence: 01:30 EST = 06:30 UTC. Must NOT run again.
	setClock(time.Date(2026, 11, 1, 6, 31, 0, 0, time.UTC))
	if res := s.Evaluate(context.Background()); len(res) != 0 {
		t.Fatalf("ran at the repeated instant: %+v", res)
	}
	if runs != 1 {
		t.Fatalf("runs=%d, want 1", runs)
	}
}

// A run missed during downtime is caught up once after restart; the next
// evaluation the same day does not re-run.
func TestDailyJobCatchUpAfterDowntime(t *testing.T) {
	start := time.Date(2026, 9, 5, 3, 0, 0, 0, time.UTC)
	s, _, setClock := newTestScheduler(t, start)
	runs := 0
	if err := s.Register(Job{Name: "nightly", DailyAt: "03:30", Fn: func(context.Context) error {
		runs++
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Day 1 ran normally.
	setClock(time.Date(2026, 9, 5, 4, 0, 0, 0, time.UTC))
	s.Evaluate(ctx)
	// Process down across the day-2 scheduled time; comes back at 12:00.
	setClock(time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC))
	if res := s.Evaluate(ctx); len(res) != 1 || res[0].Status != StatusSucceeded {
		t.Fatalf("catch-up run: %+v", res)
	}
	// Same day, later: no additional run.
	setClock(time.Date(2026, 9, 6, 18, 0, 0, 0, time.UTC))
	if res := s.Evaluate(ctx); len(res) != 0 {
		t.Fatalf("double catch-up: %+v", res)
	}
	if runs != 2 {
		t.Fatalf("runs=%d, want 2", runs)
	}
}

// A failed execution still counts as the day's run: the scheduler never
// retries in a tight loop; the failure is visible in the result.
func TestDailyJobFailureCountedOnce(t *testing.T) {
	start := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	s, _, setClock := newTestScheduler(t, start)
	runs := 0
	if err := s.Register(Job{Name: "nightly", DailyAt: "03:00", Fn: func(context.Context) error {
		runs++
		return errors.New("boom")
	}}); err != nil {
		t.Fatal(err)
	}
	setClock(start.Add(4 * time.Hour))
	res := s.Evaluate(context.Background())
	if len(res) != 1 || res[0].Status != StatusFailed {
		t.Fatalf("failure result: %+v", res)
	}
	setClock(start.Add(5 * time.Hour))
	if res := s.Evaluate(context.Background()); len(res) != 0 {
		t.Fatalf("retried after failure: %+v", res)
	}
	if runs != 1 {
		t.Fatalf("runs=%d, want 1", runs)
	}
}

// A panicking job becomes a failed result; the scheduler survives.
func TestDailyJobPanicBecomesFailed(t *testing.T) {
	start := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	s, _, setClock := newTestScheduler(t, start)
	if err := s.Register(Job{Name: "exploding", DailyAt: "03:00", Fn: func(context.Context) error {
		panic("synthetic background panic")
	}}); err != nil {
		t.Fatal(err)
	}
	setClock(start.Add(4 * time.Hour))
	res := s.Evaluate(context.Background())
	if len(res) != 1 || res[0].Status != StatusFailed || res[0].Detail != "background job panicked" {
		t.Fatalf("panic result: %+v", res)
	}
	// The process (and scheduler) is alive: a normal job still runs.
	if err := s.Register(Job{Name: "fine", DailyAt: "05:00", Fn: func(context.Context) error {
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	setClock(start.Add(6 * time.Hour))
	res = s.Evaluate(context.Background())
	if len(res) != 1 || res[0].Status != StatusSucceeded {
		t.Fatalf("post-panic evaluate: %+v", res)
	}
}

// Busy jobs are skipped, not failed, and the skip is not remembered as the
// day's run (the day's run happens when the job actually executes).
func TestDailyJobBusySkipped(t *testing.T) {
	start := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	s, _, setClock := newTestScheduler(t, start)
	if err := s.Register(Job{Name: "nightly", DailyAt: "03:00", Fn: func(context.Context) error {
		return ErrBusy
	}}); err != nil {
		t.Fatal(err)
	}
	setClock(start.Add(4 * time.Hour))
	res := s.Evaluate(context.Background())
	if len(res) != 1 || res[0].Status != StatusSkipped || res[0].Ran {
		t.Fatalf("busy result: %+v", res)
	}
	// A later tick retries: the marker was not written for a skip.
	res = s.Evaluate(context.Background())
	if len(res) != 1 || res[0].Status != StatusSkipped {
		t.Fatalf("retry after skip: %+v", res)
	}
}

// Concurrent evaluation cannot start one job twice.
func TestConcurrentEvaluateRunsJobOnce(t *testing.T) {
	start := time.Date(2026, 9, 6, 0, 5, 0, 0, time.UTC)
	s, _, _ := newTestScheduler(t, start)
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	runs := 0
	// The fake clock is already past the scheduled time (00:01).
	if err := s.Register(Job{Name: "nightly", DailyAt: "00:01", Fn: func(context.Context) error {
		started <- struct{}{}
		<-release
		runs++
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	done := make(chan []Result, 4)
	for i := 0; i < 4; i++ {
		go func() { done <- s.Evaluate(context.Background()) }()
	}
	<-started
	close(release)
	total := 0
	for i := 0; i < 4; i++ {
		for _, r := range <-done {
			if r.Ran {
				total++
			}
		}
	}
	if total != 1 {
		t.Fatalf("concurrent executions=%d, want 1", total)
	}
}

// Registration validates schedule syntax and zone names up front.
func TestRegisterValidation(t *testing.T) {
	s, _, _ := newTestScheduler(t, time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC))
	if err := s.Register(Job{Name: "bad time", Fn: func(context.Context) error { return nil }}); err == nil {
		t.Fatal("invalid name accepted")
	}
	if err := s.Register(Job{Name: "j", DailyAt: "24:00", Fn: func(context.Context) error { return nil }}); err == nil {
		t.Fatal("invalid hour accepted")
	}
	if err := s.Register(Job{Name: "j", DailyAt: "03:5", Fn: func(context.Context) error { return nil }}); err == nil {
		t.Fatal("invalid minute accepted")
	}
	if err := s.Register(Job{Name: "j", Timezone: "Mars/Olympus", Fn: func(context.Context) error { return nil }}); err == nil {
		t.Fatal("invalid timezone accepted")
	}
	if err := s.Register(Job{Name: "ok", Timezone: "UTC", DailyAt: "08:30", Fn: func(context.Context) error { return nil }}); err != nil {
		t.Fatalf("valid job rejected: %v", err)
	}
}
