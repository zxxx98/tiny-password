package httpapi

import (
	"net/http"

	"github.com/tiny-password/tiny-password/internal/auth"
	"github.com/tiny-password/tiny-password/internal/backup"
	tpsqlite "github.com/tiny-password/tiny-password/internal/platform/sqlite"
)

// BackupsDeps wires the admin backup endpoints (T27). The runner and the
// backup package own all logic; handlers never touch SQL, crypto, or 7z.
type BackupsDeps struct {
	Runner *backup.Runner
	DB     *tpsqlite.DB
	// LocalDir, R2 and Passphrase complete the manual run input; secrets
	// live in the runner options / secret files, never in responses.
	LocalDir   string
	R2         *backup.R2Delivery
	Passphrase string
	Session    *auth.Service
	Cursor     *CursorCodec
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
		jobs := make([]map[string]any, 0, len(configs))
		for _, c := range configs {
			configured := deps.LocalDir != ""
			if c.Target == backup.TargetR2 {
				configured = deps.R2 != nil
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
		if err := backup.UpdateJobConfig(deps.DB.DB, update); err != nil {
			writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "the job configuration is invalid")
			return
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
			R2:         deps.R2,
		}
		for _, t := range input.Targets {
			switch t {
			case string(backup.TargetLocal), string(backup.TargetR2):
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
