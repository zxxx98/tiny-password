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

// JobUpdate is the whitelisted, non-sensitive configuration one target's
// job accepts from the admin API (T27). Credentials are never part of it.
type JobUpdate struct {
	Target           Target
	Enabled          bool
	ScheduleTime     string // "HH:MM" or empty (disabled schedule)
	ScheduleTimezone string // IANA name; empty = UTC
	RetentionDaily   int
	RetentionWeekly  int
	RetentionMonthly int
}

var jobUpdateTarget = map[Target]bool{TargetLocal: true, TargetR2: true}

// UpdateJobConfig validates and upserts one target's job row.
func UpdateJobConfig(db *sql.DB, u JobUpdate) error {
	if !jobUpdateTarget[u.Target] {
		return fmt.Errorf("backup: unsupported target %q", u.Target)
	}
	if u.ScheduleTime != "" {
		if _, _, err := scheduler.ParseDailyTime(u.ScheduleTime); err != nil {
			return err
		}
	}
	if _, err := scheduler.LoadLocation(u.ScheduleTimezone); err != nil {
		return fmt.Errorf("backup: invalid timezone: %w", err)
	}
	for _, n := range []int{u.RetentionDaily, u.RetentionWeekly, u.RetentionMonthly} {
		if n < 0 || n > 999 {
			return fmt.Errorf("backup: retention counts must be 0-999")
		}
	}
	enabled := 0
	if u.Enabled {
		enabled = 1
	}
	_, err := db.Exec(
		`INSERT INTO backup_jobs (id, target, enabled, schedule_time, schedule_timezone, retention_daily, retention_weekly, retention_monthly, updated_at)
		 VALUES ('job-' || ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(target) DO UPDATE SET
		   enabled = excluded.enabled, schedule_time = excluded.schedule_time,
		   schedule_timezone = excluded.schedule_timezone,
		   retention_daily = excluded.retention_daily,
		   retention_weekly = excluded.retention_weekly,
		   retention_monthly = excluded.retention_monthly,
		   updated_at = excluded.updated_at`,
		string(u.Target), string(u.Target), enabled, nullIfEmpty(u.ScheduleTime),
		nullIfEmpty(u.ScheduleTimezone), u.RetentionDaily, u.RetentionWeekly, u.RetentionMonthly,
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("store backup job: %w", err)
	}
	return nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// RunRow is one backup_runs entry for the admin history listing.
type RunRow struct {
	ID          string  `json:"id"`
	Target      string  `json:"target"`
	Status      string  `json:"status"`
	TriggeredBy string  `json:"triggered_by"`
	StartedAt   string  `json:"started_at"`
	FinishedAt  *string `json:"finished_at,omitempty"`
	SizeBytes   *int64  `json:"size_bytes,omitempty"`
	SHA256      *string `json:"sha256,omitempty"`
	ErrorCode   *string `json:"error_code,omitempty"`
}

// ListRuns pages through run history (newest first), optionally filtered
// by target.
func ListRuns(db *sql.DB, target, beforeCreated, beforeID string, limit int) ([]RunRow, error) {
	query := `SELECT id, target, status, triggered_by, started_at, finished_at, size_bytes, sha256, error_code
	          FROM backup_runs`
	var args []any
	if target != "" {
		query += ` WHERE target = ?`
		args = append(args, target)
	}
	if beforeCreated != "" {
		if target != "" {
			query += ` AND`
		} else {
			query += ` WHERE`
		}
		query += ` (started_at < ? OR (started_at = ? AND id < ?))`
		args = append(args, beforeCreated, beforeCreated, beforeID)
	}
	query += ` ORDER BY started_at DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list backup runs: %w", err)
	}
	defer rows.Close()
	var out []RunRow
	for rows.Next() {
		var row RunRow
		var finished, sha, code sql.NullString
		var size sql.NullInt64
		if err := rows.Scan(&row.ID, &row.Target, &row.Status, &row.TriggeredBy,
			&row.StartedAt, &finished, &size, &sha, &code); err != nil {
			return nil, err
		}
		if finished.Valid {
			row.FinishedAt = &finished.String
		}
		if size.Valid {
			row.SizeBytes = &size.Int64
		}
		if sha.Valid {
			row.SHA256 = &sha.String
		}
		if code.Valid {
			row.ErrorCode = &code.String
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// ScheduledJobs returns one scheduler job per enabled target that has a
// daily schedule. The passphrase and delivery configuration come from the
// runner options (secret files / settings resolver), never from the
// database's sensitive surface.
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
				input, err := r.scheduledInput(ctx, target)
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
func (r *Runner) scheduledInput(ctx context.Context, target Target) (RunInput, error) {
	input := RunInput{Targets: []Target{target}, Trigger: TriggerScheduled}
	if r.opts.ScheduledPassphrase == "" {
		return input, fmt.Errorf("backup: scheduled backup passphrase not configured")
	}
	input.Passphrase = r.opts.ScheduledPassphrase
	switch target {
	case TargetLocal:
		input.LocalDir = r.opts.ScheduledLocalDir
	case TargetR2:
		if r.opts.R2 == nil {
			return input, fmt.Errorf("backup: r2 delivery is not configured")
		}
		delivery, err := r.opts.R2(ctx)
		if err != nil {
			return input, fmt.Errorf("backup: resolve r2 delivery: %w", err)
		}
		if delivery == nil {
			return input, fmt.Errorf("backup: r2 delivery is not configured")
		}
		input.R2 = delivery
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
	// R2Incoming, when set, sweeps interrupted R2 temporary objects. The
	// resolver is evaluated per execution so settings changes apply without
	// a restart; a nil result skips the sweep.
	R2Incoming R2Resolver
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
				delivery, err := deps.R2Incoming(ctx)
				if err != nil {
					return err
				}
				if delivery == nil {
					return nil
				}
				_, err = delivery.CleanupIncoming(ctx)
				return err
			},
		})
	}
	return jobs
}
