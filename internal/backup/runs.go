package backup

import (
	"time"

	"github.com/tiny-password/tiny-password/internal/platform/ident"
)

// insertRunningRow opens the run's lifecycle row. The status CHECK in the
// schema allows pending/running/succeeded/failed/interrupted; rows enter as
// running (T23 marks rows left running by a crash as interrupted).
func (r *Runner) insertRunningRow(runID string, input RunInput) error {
	_, err := r.opts.DB.Exec(
		`INSERT INTO backup_runs (id, target, status, triggered_by, started_at)
		 VALUES (?, ?, 'running', ?, ?)`,
		runID, string(input.Target), string(input.Trigger), r.opts.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return wrapInternal(err, "record backup run")
	}
	return nil
}

// succeedRun closes the row as succeeded with the delivered size/digest.
// Best effort: a recording failure is logged, never converted into a fake
// failure of the delivered backup.
func (r *Runner) succeedRun(runID string, result *RunResult) {
	_, err := r.opts.DB.Exec(
		`UPDATE backup_runs SET status = 'succeeded', finished_at = ?, size_bytes = ?, sha256 = ?
		 WHERE id = ?`,
		r.opts.Now().UTC().Format(time.RFC3339Nano), result.SizeBytes, result.SHA256, runID,
	)
	if err != nil {
		r.opts.Logger.Error("backup run record update failed", "error", err.Error())
	}
}

// failRun closes the row as failed with the stable error code only.
func (r *Runner) failRun(runID, code string) {
	if _, err := r.opts.DB.Exec(
		`UPDATE backup_runs SET status = 'failed', finished_at = ?, error_code = ? WHERE id = ?`,
		r.opts.Now().UTC().Format(time.RFC3339Nano), code, runID,
	); err != nil {
		r.opts.Logger.Error("backup run record update failed", "error", err.Error())
	}
}

// recordFailure writes a failed run row for a failure that happened before
// ordinary bookkeeping started (e.g. input validation). Run ids are minted
// here so the row is still traceable.
func (r *Runner) recordFailure(runID string, input RunInput, err error) error {
	if runID == "" {
		runID = ident.NewUUIDv7()
	}
	if _, dbErr := r.opts.DB.Exec(
		`INSERT INTO backup_runs (id, target, status, triggered_by, started_at, finished_at, error_code)
		 VALUES (?, ?, 'failed', ?, ?, ?, ?)`,
		runID, string(input.Target), string(input.Trigger),
		r.opts.Now().UTC().Format(time.RFC3339Nano), r.opts.Now().UTC().Format(time.RFC3339Nano), errorCode(err),
	); dbErr != nil {
		r.opts.Logger.Error("backup failure record failed", "error", dbErr.Error())
	}
	return err
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
