package backup

import (
	"context"
	"time"

	"github.com/tiny-password/tiny-password/internal/audit"
	"github.com/tiny-password/tiny-password/internal/platform/ident"
)

// insertRunningRow opens the run's lifecycle row. The status CHECK in the
// schema allows pending/running/succeeded/failed/interrupted; rows enter as
// running (T23 marks rows left running by a crash as interrupted).
func (r *Runner) insertRunningRow(runID string, target Target, trigger Trigger) error {
	_, err := r.opts.DB.Exec(
		`INSERT INTO backup_runs (id, target, status, triggered_by, started_at)
		 VALUES (?, ?, 'running', ?, ?)`,
		runID, string(target), string(trigger), r.opts.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return wrapInternal(err, "record backup run")
	}
	return nil
}

// succeedRun closes the row as succeeded with the delivered size/digest.
// Best effort: a recording failure is logged, never converted into a fake
// failure of the delivered backup.
func (r *Runner) succeedRun(runID string, sizeBytes int64, sha256 string) {
	_, err := r.opts.DB.Exec(
		`UPDATE backup_runs SET status = 'succeeded', finished_at = ?, size_bytes = ?, sha256 = ?
		 WHERE id = ?`,
		r.opts.Now().UTC().Format(time.RFC3339Nano), sizeBytes, sha256, runID,
	)
	if err != nil {
		r.opts.Logger.Error("backup run record update failed", "error", err.Error())
	}
	r.auditRun(runID, audit.ResultSuccess)
}

// failRun closes the row as failed with the stable error code only.
func (r *Runner) failRun(runID, code string) {
	if _, err := r.opts.DB.Exec(
		`UPDATE backup_runs SET status = 'failed', finished_at = ?, error_code = ? WHERE id = ?`,
		r.opts.Now().UTC().Format(time.RFC3339Nano), code, runID,
	); err != nil {
		r.opts.Logger.Error("backup run record update failed", "error", err.Error())
	}
	r.auditRun(runID, audit.ResultFailure)
}

// recordFailures writes a failed run row for every requested target after
// a failure that happened before ordinary bookkeeping started (e.g. input
// validation). Run ids are minted here so the rows stay traceable.
func (r *Runner) recordFailures(input RunInput, err error) error {
	for _, t := range dedupTargets(input.Targets) {
		runID := ident.NewUUIDv7()
		now := r.opts.Now().UTC().Format(time.RFC3339Nano)
		if _, dbErr := r.opts.DB.Exec(
			`INSERT INTO backup_runs (id, target, status, triggered_by, started_at, finished_at, error_code)
			 VALUES (?, ?, 'failed', ?, ?, ?, ?)`,
			runID, string(t), string(input.Trigger), now, now, errorCode(err),
		); dbErr != nil {
			r.opts.Logger.Error("backup failure record failed", "error", dbErr.Error())
		}
		r.auditRun(runID, audit.ResultFailure)
	}
	return err
}

func (r *Runner) auditRun(runID, result string) {
	if r.opts.Audit == nil {
		return
	}
	if err := r.opts.Audit.Record(context.Background(), r.opts.DB.DB, audit.Event{
		Name:       audit.EventBackupRun,
		ActorID:    audit.Anonymous,
		TargetType: audit.TargetBackup,
		TargetID:   runID,
		Result:     result,
	}); err != nil {
		r.opts.Logger.Error("backup audit write failed", "error", err.Error())
	}
}

func wrapInternal(err error, what string) error {
	return &rowError{what: what, err: err}
}

type rowError struct {
	what string
	err  error
}

func (e *rowError) Error() string { return e.what + " failed" }
func (e *rowError) Unwrap() error { return e.err }
