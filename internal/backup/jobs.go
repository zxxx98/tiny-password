package backup

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/tiny-password/tiny-password/internal/auth"
	"github.com/tiny-password/tiny-password/internal/idempotency"
	"github.com/tiny-password/tiny-password/internal/platform/sqlite"
	"github.com/tiny-password/tiny-password/internal/scheduler"
	"github.com/tiny-password/tiny-password/internal/vault"
)

// MarkInterruptedRuns flags run rows left in pending/running by a crashed
// or restarted process as interrupted (T23): an operator must never see a
// backup that claims to be running forever. Safe to run at every startup.
func MarkInterruptedRuns(db *sql.DB, now time.Time) (int64, error) {
	res, err := db.Exec(
		`UPDATE backup_runs SET status = 'interrupted', finished_at = ?,
		   error_code = COALESCE(error_code, 'BACKUP_INTERRUPTED')
		 WHERE status IN ('pending', 'running')`,
		now.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return 0, fmt.Errorf("mark interrupted runs: %w", err)
	}
	return res.RowsAffected()
}

// JobConfig is one row of backup_jobs: the persisted, non-sensitive job
// configuration (T27 renders and updates it through the settings API).
type JobConfig struct {
	Target           Target
	Enabled          bool
	ScheduleTime     string // "HH:MM" or empty
	ScheduleTimezone string // IANA name or empty (UTC)
	RetentionDaily   int
	RetentionWeekly  int
	RetentionMonthly int
}

// LoadJobConfigs reads the per-target configuration. Rows are seeded on
// first use so the admin API always has both targets to present.
func LoadJobConfigs(db *sql.DB) ([]JobConfig, error) {
	if _, err := db.Exec(
		`INSERT INTO backup_jobs (id, target, enabled, updated_at) VALUES
		   ('job-local', 'local', 0, ?),
		   ('job-r2',    'r2',    0, ?)
		 ON CONFLICT(target) DO NOTHING`,
		time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		return nil, fmt.Errorf("seed backup jobs: %w", err)
	}
	rows, err := db.Query(
		`SELECT target, enabled, COALESCE(schedule_time, ''), COALESCE(schedule_timezone, ''),
		        retention_daily, retention_weekly, retention_monthly
		 FROM backup_jobs ORDER BY target`,
	)
	if err != nil {
		return nil, fmt.Errorf("read backup jobs: %w", err)
	}
	defer rows.Close()
	var configs []JobConfig
	for rows.Next() {
		var c JobConfig
		var enabled int
		if err := rows.Scan(&c.Target, &enabled, &c.ScheduleTime, &c.ScheduleTimezone,
			&c.RetentionDaily, &c.RetentionWeekly, &c.RetentionMonthly); err != nil {
			return nil, err
		}
		c.Enabled = enabled == 1
		configs = append(configs, c)
	}
	return configs, rows.Err()
}

// ScheduledJobs returns one scheduler job per enabled target that has a
// daily schedule. The passphrase and delivery configuration come from the
// runner options (secret files), never from the database.
func (r *Runner) ScheduledJobs() ([]scheduler.Job, error) {
	configs, err := LoadJobConfigs(r.opts.DB.DB)
	if err != nil {
		return nil, err
	}
	var jobs []scheduler.Job
	for _, c := range configs {
		if !c.Enabled || c.ScheduleTime == "" {
			continue
		}
		target := c.Target
		jobs = append(jobs, scheduler.Job{
			Name:     "backup." + string(target),
			Timezone: c.ScheduleTimezone,
			DailyAt:  c.ScheduleTime,
			Fn: func(ctx context.Context) error {
				input, err := r.scheduledInput(target)
				if err != nil {
					return err
				}
				_, err = r.Run(ctx, input)
				return err
			},
		})
	}
	return jobs, nil
}

// scheduledInput builds the RunInput for scheduled executions. It fails
// closed on missing configuration: an enabled target without its delivery
// settings reports an error instead of guessing.
func (r *Runner) scheduledInput(target Target) (RunInput, error) {
	input := RunInput{Targets: []Target{target}, Trigger: TriggerScheduled}
	if r.opts.ScheduledPassphrase == "" {
		return input, fmt.Errorf("backup: scheduled backup passphrase not configured")
	}
	input.Passphrase = r.opts.ScheduledPassphrase
	switch target {
	case TargetLocal:
		input.LocalDir = r.opts.ScheduledLocalDir
	case TargetR2:
		input.R2 = r.opts.ScheduledR2
		if input.R2 == nil {
			return input, fmt.Errorf("backup: r2 delivery is not configured")
		}
	default:
		return input, fmt.Errorf("backup: unsupported target %q", target)
	}
	return input, nil
}

// MaintenanceDeps carries the collaborators the bounded maintenance jobs
// need. Everything optional stays nil and its job is skipped.
type MaintenanceDeps struct {
	DB          *sqlite.DB
	Vault       *vault.Service
	Idempotency *idempotency.Service
	// R2Incoming, when set, sweeps interrupted R2 temporary objects.
	R2Incoming *R2Delivery
}

// MaintenanceJobs returns the bounded sweeps registered alongside backups
// (T23): trash retention, expired sessions and rate-limit rows, expired
// idempotency claims, stale R2 incoming objects, and the low-peak WAL
// checkpoint. Each runs inside a panic-isolated scheduler job.
func MaintenanceJobs(deps MaintenanceDeps) []scheduler.Job {
	jobs := make([]scheduler.Job, 0, 5)
	if deps.Vault != nil {
		jobs = append(jobs, scheduler.Job{
			Name: "maintenance.trash",
			Fn: func(ctx context.Context) error {
				_, err := deps.Vault.PurgeExpiredTrash(ctx)
				return err
			},
		})
	}
	if deps.DB != nil {
		jobs = append(jobs, scheduler.Job{
			Name: "maintenance.sessions",
			Fn: func(ctx context.Context) error {
				_, err := auth.CleanupExpiredSessions(ctx, deps.DB.DB, time.Now())
				return err
			},
		})
		jobs = append(jobs, scheduler.Job{
			Name: "maintenance.login_attempts",
			Fn: func(ctx context.Context) error {
				_, err := auth.CleanupLoginAttempts(ctx, deps.DB.DB, time.Now().Add(-time.Hour))
				return err
			},
		})
		jobs = append(jobs, scheduler.Job{
			Name: "maintenance.wal_checkpoint",
			Fn: func(ctx context.Context) error {
				return deps.DB.Checkpoint()
			},
		})
	}
	if deps.Idempotency != nil {
		jobs = append(jobs, scheduler.Job{
			Name: "maintenance.idempotency",
			Fn: func(ctx context.Context) error {
				_, err := deps.Idempotency.CleanupExpired(ctx)
				return err
			},
		})
	}
	if deps.R2Incoming != nil {
		jobs = append(jobs, scheduler.Job{
			Name: "maintenance.r2_incoming",
			Fn: func(ctx context.Context) error {
				_, err := deps.R2Incoming.CleanupIncoming(ctx)
				return err
			},
		})
	}
	return jobs
}
