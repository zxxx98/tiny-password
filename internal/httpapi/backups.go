package httpapi

import (
	"net/http"

	"github.com/tiny-password/tiny-password/internal/audit"
	"github.com/tiny-password/tiny-password/internal/auth"
	"github.com/tiny-password/tiny-password/internal/backup"
	tpsqlite "github.com/tiny-password/tiny-password/internal/platform/sqlite"
	"github.com/tiny-password/tiny-password/internal/scheduler"
)

// BackupsDeps wires the admin backup endpoints (T27). The runner and the
// backup package own all logic; handlers never touch SQL, crypto, or 7z.
type BackupsDeps struct {
	Runner *backup.Runner
	DB     *tpsqlite.DB
	// LocalDir is the local publish directory; R2 resolves the R2 delivery
	// at request time (settings + credential files). Passphrase and the
	// credential values themselves never appear in responses.
	LocalDir   string
	R2         backup.R2Resolver
	Passphrase string
	Session    *auth.Service
	Cursor     *CursorCodec
	// Scheduler is refreshed after a successful persisted job update so
	// changes take effect without restarting the process.
	Scheduler *scheduler.Scheduler
	Audit     *audit.Service
}

// resolveR2 returns the current R2 delivery for this request. A nil
// delivery with a nil error means the target is not configured; a non-nil
// error reports a resolution failure (settings store or credential file).
func (d BackupsDeps) resolveR2(r *http.Request) (*backup.R2Delivery, error) {
	if d.R2 == nil {
		return nil, nil
	}
	return d.R2(r.Context())
}

func registerBackups(api *http.ServeMux, deps BackupsDeps) {
	admin := func(handler func(w http.ResponseWriter, r *http.Request)) http.Handler {
		return RequireAdmin(deps.Session, false, http.HandlerFunc(handler))
	}

	// Job configuration listing (non-sensitive fields only).
	api.Handle("GET /api/v1/admin/backups/jobs", admin(func(w http.ResponseWriter, r *http.Request) {
		configs, err := backup.LoadJobConfigs(deps.DB.DB)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL", "the job configuration is unavailable")
			return
		}
		r2Delivery, r2Err := deps.resolveR2(r)
		jobs := make([]map[string]any, 0, len(configs))
		for _, c := range configs {
			configured := deps.LocalDir != ""
			if c.Target == backup.TargetR2 {
				configured = r2Err == nil && r2Delivery != nil
			}
			jobs = append(jobs, map[string]any{
				"target":            string(c.Target),
				"enabled":           c.Enabled,
				"schedule_time":     c.ScheduleTime,
				"schedule_timezone": c.ScheduleTimezone,
				"retention_daily":   c.RetentionDaily,
				"retention_weekly":  c.RetentionWeekly,
				"retention_monthly": c.RetentionMonthly,
				"delivery_ready":    configured,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
	}))

	// Job configuration update from an explicit whitelist.
	api.Handle("PUT /api/v1/admin/backups/jobs", admin(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Target           string `json:"target"`
			Enabled          bool   `json:"enabled"`
			ScheduleTime     string `json:"schedule_time"`
			ScheduleTimezone string `json:"schedule_timezone"`
			RetentionDaily   *int   `json:"retention_daily"`
			RetentionWeekly  *int   `json:"retention_weekly"`
			RetentionMonthly *int   `json:"retention_monthly"`
		}
		if !decodeJSONBody(w, r, &input, authMaxBodyBytes) {
			return
		}
		update := backup.JobUpdate{
			Target:           backup.Target(input.Target),
			Enabled:          input.Enabled,
			ScheduleTime:     input.ScheduleTime,
			ScheduleTimezone: input.ScheduleTimezone,
			RetentionDaily:   derefOr(input.RetentionDaily, 7),
			RetentionWeekly:  derefOr(input.RetentionWeekly, 4),
			RetentionMonthly: derefOr(input.RetentionMonthly, 6),
		}
		tx, err := deps.DB.BeginTx(r.Context(), nil)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL", "the job configuration is unavailable")
			return
		}
		if err := backup.UpdateJobConfigTx(tx, update); err != nil {
			_ = tx.Rollback()
			if deps.Audit != nil && (update.Target == backup.TargetLocal || update.Target == backup.TargetR2) {
				p := CurrentPrincipal(r.Context())
				_ = deps.Audit.Record(r.Context(), deps.DB.DB, audit.Event{
					Name:       audit.EventBackupConfigUpdated,
					ActorID:    p.UserID,
					TargetType: audit.TargetBackup,
					TargetID:   string(update.Target),
					Result:     audit.ResultFailure,
				})
			}
			writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "the job configuration is invalid")
			return
		}
		if deps.Audit != nil {
			p := CurrentPrincipal(r.Context())
			if err := deps.Audit.Record(r.Context(), tx, audit.Event{
				Name:       audit.EventBackupConfigUpdated,
				ActorID:    p.UserID,
				TargetType: audit.TargetBackup,
				TargetID:   string(update.Target),
				Result:     audit.ResultSuccess,
			}); err != nil {
				_ = tx.Rollback()
				writeError(w, r, http.StatusInternalServerError, "INTERNAL", "the job configuration is unavailable")
				return
			}
		}
		if err := tx.Commit(); err != nil {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL", "the job configuration is unavailable")
			return
		}
		if deps.Scheduler != nil {
			jobs, err := deps.Runner.ScheduledJobs()
			if err != nil {
				writeError(w, r, http.StatusInternalServerError, "INTERNAL", "the scheduler configuration is unavailable")
				return
			}
			if err := deps.Scheduler.ReloadPrefix("backup.", jobs); err != nil {
				writeError(w, r, http.StatusInternalServerError, "INTERNAL", "the scheduler configuration is invalid")
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"updated": true})
	}))

	// Run history per target (newest first, cursor-paged).
	api.Handle("GET /api/v1/admin/backups/runs", admin(func(w http.ResponseWriter, r *http.Request) {
		target := r.URL.Query().Get("target")
		if target != "" && target != string(backup.TargetLocal) && target != string(backup.TargetR2) {
			writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "unknown target filter")
			return
		}
		p := CurrentPrincipal(r.Context())
		filters := "v1:backups-runs:target=" + target
		beforeCreated, beforeID, limit, ok := DecodeCursorParams(w, r, deps.Cursor, p.UserID, filters)
		if !ok {
			return
		}
		rows, err := backup.ListRuns(deps.DB.DB, target, beforeCreated, beforeID, limit+1)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL", "the run history is unavailable")
			return
		}
		var lastCreated, lastID string
		if len(rows) > limit {
			rows = rows[:limit]
			lastCreated = rows[len(rows)-1].StartedAt
			lastID = rows[len(rows)-1].ID
		}
		writeJSON(w, http.StatusOK, CursorPageResponse(deps.Cursor, p.UserID, filters, rows, lastCreated, lastID))
	}))

	// Manual trigger: 202 when accepted, 409 when a run holds the mutex.
	// A requested target without its delivery configuration is refused with
	// 503 MAINTENANCE before anything starts — the run never partially
	// ignores the requested target list.
	api.Handle("POST /api/v1/admin/backups/run", admin(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Targets []string `json:"targets"`
		}
		if !decodeJSONBody(w, r, &input, authMaxBodyBytes) {
			return
		}
		runInput := backup.RunInput{
			Trigger:    backup.TriggerManual,
			Passphrase: deps.Passphrase,
			LocalDir:   deps.LocalDir,
		}
		for _, t := range input.Targets {
			switch t {
			case string(backup.TargetLocal):
				runInput.Targets = append(runInput.Targets, backup.Target(t))
			case string(backup.TargetR2):
				delivery, err := deps.resolveR2(r)
				if err != nil {
					writeError(w, r, http.StatusInternalServerError, "INTERNAL", "the r2 delivery configuration is unavailable")
					return
				}
				if delivery == nil {
					writeError(w, r, http.StatusServiceUnavailable, "MAINTENANCE", "backup delivery is not configured for target r2")
					return
				}
				runInput.R2 = delivery
				runInput.Targets = append(runInput.Targets, backup.Target(t))
			default:
				writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "unknown target")
				return
			}
		}
		started, err := deps.Runner.RunAsync(r.Context(), runInput)
		if err != nil {
			if err == backup.ErrPassphraseInvalid {
				writeError(w, r, http.StatusServiceUnavailable, "MAINTENANCE", "backup passphrase is not configured")
				return
			}
			writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "the backup request is invalid")
			return
		}
		if !started {
			writeError(w, r, http.StatusConflict, "BACKUP_BUSY", "another backup is running")
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"started": true})
	}))
}

func derefOr(v *int, fallback int) int {
	if v == nil {
		return fallback
	}
	return *v
}
