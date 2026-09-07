// Package scheduler runs daily jobs (instance backups) and bounded
// maintenance sweeps (design §11.4, T23). The clock is injectable; DST is
// handled per decision D09: a wall time skipped by a spring-forward runs at
// the next valid instant, a repeated wall time runs once, and a run missed
// during downtime is caught up exactly once after restart. Every executed
// run is remembered in app_settings so a restart can never double-execute
// the same day's scheduled run.
package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	_ "time/tzdata" // embedded IANA database: containers carry no /usr/share/zoneinfo
)

// ErrBusy reports that the job is already running (a manual/scheduled
// overlap surfaces as a skip, not a failure).
var ErrBusy = errors.New("scheduler: job busy")

// Job states exposed to the admin API.
const (
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusSkipped   = "skipped"
)

// Job is one schedulable unit. DailyAt ("HH:MM") and Timezone (IANA) come
// from persisted configuration; an empty DailyAt registers a job that only
// maintenance invocations run.
type Job struct {
	Name     string
	Timezone string // IANA name; empty = UTC
	DailyAt  string // "HH:MM"; empty = no daily schedule
	Fn       func(ctx context.Context) error
}

// Result reports one evaluation outcome.
type Result struct {
	Job        string    `json:"job"`
	Status     string    `json:"status"`
	Ran        bool      `json:"ran"` // executed now (vs not due / skipped)
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Detail     string    `json:"detail,omitempty"` // stable, display-safe reason
}

// Options configures the scheduler.
type Options struct {
	DB     *sql.DB
	Now    func() time.Time
	Logger *slog.Logger
	// Interval bounds how often due jobs are polled once Started; Evaluate
	// is also exported for tests and manual ticks.
	Interval time.Duration
	// KeyPrefix namespaces the last-run markers in app_settings.
	KeyPrefix string
}

// Scheduler owns job registration and evaluation. Evaluate is safe for
// concurrent use; each job runs at most once concurrently.
type Scheduler struct {
	mu         sync.Mutex
	jobs       map[string]Job
	running    map[string]bool
	lastResult map[string]Result
	now        func() time.Time
	logger     *slog.Logger
	db         *sql.DB
	prefix     string
	interval   time.Duration
	stop       chan struct{}
	stopped    chan struct{}
}

// New validates the configuration.
func New(options Options) (*Scheduler, error) {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if options.Interval <= 0 {
		options.Interval = 30 * time.Second
	}
	if options.KeyPrefix == "" {
		options.KeyPrefix = "scheduler"
	}
	if options.DB == nil {
		return nil, errors.New("scheduler: db is required")
	}
	return &Scheduler{
		jobs:       map[string]Job{},
		running:    map[string]bool{},
		lastResult: map[string]Result{},
		now:        options.Now,
		logger:     options.Logger,
		db:         options.DB,
		prefix:     options.KeyPrefix,
		interval:   options.Interval,
		stop:       make(chan struct{}),
		stopped:    make(chan struct{}),
	}, nil
}

// Register adds a job. Names, time zones and daily times are validated up
// front so a typo cannot silently disable a backup.
func (s *Scheduler) Register(job Job) error {
	if err := validateJob(job); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.Name] = job
	return nil
}

// ReloadPrefix atomically replaces the registered jobs whose names start
// with prefix. It is used for persisted job groups (for example backup.*)
// so an admin update takes effect immediately: disabled jobs disappear and
// changed schedules are visible to the next evaluation. Other groups, such
// as maintenance jobs, remain untouched.
func (s *Scheduler) ReloadPrefix(prefix string, jobs []Job) error {
	if prefix == "" {
		return errors.New("scheduler: reload prefix is required")
	}
	replacement := make(map[string]Job, len(jobs))
	for _, job := range jobs {
		if !strings.HasPrefix(job.Name, prefix) {
			return fmt.Errorf("scheduler: job %s is outside reload prefix %q", job.Name, prefix)
		}
		if err := validateJob(job); err != nil {
			return err
		}
		if _, exists := replacement[job.Name]; exists {
			return fmt.Errorf("scheduler: duplicate job %s", job.Name)
		}
		replacement[job.Name] = job
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for name := range s.jobs {
		if strings.HasPrefix(name, prefix) {
			delete(s.jobs, name)
		}
	}
	for name := range s.lastResult {
		if strings.HasPrefix(name, prefix) {
			if _, stillRegistered := replacement[name]; !stillRegistered {
				delete(s.lastResult, name)
			}
		}
	}
	for name, job := range replacement {
		s.jobs[name] = job
	}
	return nil
}

// Reload is a shorthand for ReloadPrefix for callers that manage one named
// job group.
func (s *Scheduler) Reload(prefix string, jobs []Job) error {
	return s.ReloadPrefix(prefix, jobs)
}

func validateJob(job Job) error {
	if job.Name == "" || strings.ContainsAny(job.Name, " \t\n") {
		return fmt.Errorf("scheduler: invalid job name %q", job.Name)
	}
	if job.Fn == nil {
		return fmt.Errorf("scheduler: job %s has no function", job.Name)
	}
	if job.DailyAt != "" {
		if _, _, err := ParseDailyTime(job.DailyAt); err != nil {
			return fmt.Errorf("scheduler: job %s: %w", job.Name, err)
		}
	}
	if _, err := LoadLocation(job.Timezone); err != nil {
		return fmt.Errorf("scheduler: job %s: invalid timezone: %w", job.Name, err)
	}
	return nil
}

// Evaluate runs every job whose scheduled time has passed today (in the
// job's location) and which has not run yet for that date. Jobs run
// sequentially in the caller's context; a panic inside a job is recovered
// into a failed result so background work can never take the process down.
func (s *Scheduler) Evaluate(ctx context.Context) []Result {
	s.mu.Lock()
	jobs := make([]Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		jobs = append(jobs, j)
	}
	s.mu.Unlock()

	var results []Result
	for _, job := range jobs {
		if res := s.evaluateJob(ctx, job); res != nil {
			results = append(results, *res)
		}
	}
	return results
}

func (s *Scheduler) evaluateJob(ctx context.Context, job Job) *Result {
	s.mu.Lock()
	if s.running[job.Name] {
		s.mu.Unlock()
		res := s.record(job, Result{Job: job.Name, Status: StatusSkipped, Detail: "busy"})
		return &res
	}
	// Evaluate the marker only after acquiring the per-job guard. Checking it
	// before the guard lets concurrent callers all observe an old marker and
	// one of them start a duplicate run after the first caller finishes.
	due, runKey, err := s.isDue(job, s.now())
	if err != nil {
		s.mu.Unlock()
		res := s.record(job, Result{Job: job.Name, Status: StatusFailed, Detail: "schedule evaluation failed"})
		return &res
	}
	if !due {
		s.mu.Unlock()
		return nil
	}
	s.running[job.Name] = true
	s.mu.Unlock()

	res := s.runJob(ctx, job)

	// Persist the once-per-day marker before releasing the running guard. This
	// closes the handoff window where a concurrent Evaluate could observe an
	// idle job while the first execution had finished but had not yet recorded
	// its marker, causing a duplicate run.
	if res.Ran {
		s.rememberRun(job.Name, runKey)
	}

	s.mu.Lock()
	delete(s.running, job.Name)
	s.mu.Unlock()
	return &res
}

// runJob executes one job with panic isolation. The named return lets the
// deferred recovery mutate the actual result the caller receives.
func (s *Scheduler) runJob(ctx context.Context, job Job) (res Result) {
	res = Result{Job: job.Name, Ran: true, StartedAt: s.now()}
	defer func() {
		if rec := recover(); rec != nil {
			res.Status = StatusFailed
			res.Detail = "background job panicked"
			s.logger.Error("scheduler job panicked", "job", job.Name)
		}
		res.FinishedAt = s.now()
		s.mu.Lock()
		s.lastResult[job.Name] = res
		s.mu.Unlock()
	}()
	err := job.Fn(ctx)
	switch {
	case err == nil:
		res.Status = StatusSucceeded
	case errors.Is(err, ErrBusy):
		res.Status = StatusSkipped
		res.Ran = false
		res.Detail = "busy"
	default:
		res.Status = StatusFailed
		res.Detail = "job failed"
	}
	return res
}

// record stores and returns a bookkeeping-only result (Fn did not run).
func (s *Scheduler) record(job Job, res Result) Result {
	res.StartedAt = s.now()
	res.FinishedAt = res.StartedAt
	s.mu.Lock()
	s.lastResult[job.Name] = res
	s.mu.Unlock()
	return res
}

// isDue reports whether job's scheduled instant has passed today (in the
// job's location) and the job has not yet run for that date. runKey is the
// job-local date used for the once-per-day marker. DailyRunAt applies the
// D09 DST semantics.
func (s *Scheduler) isDue(job Job, now time.Time) (bool, string, error) {
	if job.DailyAt == "" {
		return false, "", nil
	}
	hh, mm, err := ParseDailyTime(job.DailyAt)
	if err != nil {
		return false, "", err
	}
	loc, err := LoadLocation(job.Timezone)
	if err != nil {
		return false, "", err
	}
	local := now.In(loc)
	runAt := DailyRunAt(now, hh, mm, loc)
	runKey := runAt.Format("2006-01-02")
	if local.Before(runAt) {
		return false, runKey, nil
	}
	last, err := s.lastRunKey(job.Name)
	if err != nil {
		return false, runKey, err
	}
	return runKey != last, runKey, nil
}

func (s *Scheduler) lastRunKey(jobName string) (string, error) {
	var value string
	err := s.db.QueryRow(
		"SELECT value FROM app_settings WHERE key = ?", s.markerKey(jobName),
	).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read scheduler marker: %w", err)
	}
	return value, nil
}

func (s *Scheduler) rememberRun(jobName, runKey string) {
	_, err := s.db.Exec(
		`INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		s.markerKey(jobName), runKey, s.now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		s.logger.Error("scheduler marker write failed", "job", jobName)
	}
}

func (s *Scheduler) markerKey(jobName string) string {
	return s.prefix + ".lastrun." + jobName
}

// Results snapshots the most recent result of every job (admin API).
func (s *Scheduler) Results() []Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.lastResult))
	for name := range s.lastResult {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]Result, 0, len(names))
	for _, name := range names {
		out = append(out, s.lastResult[name])
	}
	return out
}

// LastResult reports the most recent result of one job (admin API).
func (s *Scheduler) LastResult(name string) (Result, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, ok := s.lastResult[name]
	return res, ok
}

// Start polls for due jobs until Stop. It never panics into the caller.
func (s *Scheduler) Start(ctx context.Context) {
	go func() {
		defer close(s.stopped)
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-ticker.C:
				s.Evaluate(ctx)
			}
		}
	}()
}

// Stop terminates the polling loop and waits for it.
func (s *Scheduler) Stop() {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	<-s.stopped
}
