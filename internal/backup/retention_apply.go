package backup

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tiny-password/tiny-password/internal/audit"
)

// ApplyRetention reconciles one target against its GFS keep set (design
// §11.4 step 9, T24). It runs automatically after a target gains a verified
// backup and is deliberately
// stateless: the target is listed, everything outside the keep set is
// deleted, and a failed deletion is therefore retried naturally by the
// next successful run. Each target reconciles independently, so an R2
// failure never blocks local cleanup.
func (r *Runner) ApplyRetention(ctx context.Context, target Target, input RunInput) {
	configs, err := LoadJobConfigs(r.opts.DB.DB)
	if err != nil {
		r.opts.Logger.Error("retention config unavailable")
		return
	}
	var cfg *JobConfig
	for i := range configs {
		if configs[i].Target == target {
			cfg = &configs[i]
			break
		}
	}
	if cfg == nil {
		return
	}

	type runRow struct {
		id string
		at time.Time
	}
	rows, err := r.opts.DB.Query(
		`SELECT id, started_at FROM backup_runs WHERE target = ? AND status = 'succeeded'`,
		string(target),
	)
	if err != nil {
		r.opts.Logger.Error("retention run list failed")
		return
	}
	defer rows.Close()
	var runs []runRow
	for rows.Next() {
		var row runRow
		var startedAt string
		if err := rows.Scan(&row.id, &startedAt); err != nil {
			return
		}
		parsed, err := time.Parse(time.RFC3339Nano, startedAt)
		if err != nil {
			continue
		}
		row.at = parsed
		runs = append(runs, row)
	}
	// Without a verified success there is no retention decision to make:
	// deleting against an empty keep set could remove the only usable
	// backup (T24: failed backups must not trigger cleanup).
	if len(runs) == 0 {
		return
	}
	candidates := make([]RetentionCandidate, 0, len(runs))
	for _, run := range runs {
		candidates = append(candidates, RetentionCandidate{Ref: run.id, CreatedAt: run.at})
	}
	keep := RetentionKeepSet(candidates, cfg.RetentionDaily, cfg.RetentionWeekly, cfg.RetentionMonthly)

	switch target {
	case TargetLocal:
		r.reconcileLocal(input.LocalDir, keep)
	case TargetR2:
		if input.R2 != nil {
			r.reconcileR2(ctx, input.R2, keep)
		}
	}
}

// reconcileLocal deletes local archives outside the keep set. The newest
// file on disk is always spared regardless of bookkeeping: a crash between
// publish and row update must never lose the most recent artifact.
func (r *Runner) reconcileLocal(dir string, keep map[string]bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type file struct {
		name  string
		modAt time.Time
	}
	var files []file
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !strings.HasSuffix(e.Name(), ".7z") {
			continue
		}
		files = append(files, file{name: e.Name(), modAt: info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modAt.After(files[j].modAt) })
	for i, f := range files {
		stem := strings.TrimSuffix(f.name, ".7z")
		if i == 0 || keep[stem] {
			continue
		}
		if err := os.Remove(filepath.Join(dir, f.name)); err != nil {
			// Kept for the next reconciliation pass (T24: retryable).
			r.opts.Logger.Error("retention delete failed")
			continue
		}
		r.auditRetention(stem)
	}
}

// reconcileR2 deletes R2 objects outside the keep set; same newest guard
// and retry semantics as the local target.
func (r *Runner) reconcileR2(ctx context.Context, d *R2Delivery, keep map[string]bool) {
	objects, err := d.Client.ListObjects(ctx, d.Prefix+"/")
	if err != nil {
		r.opts.Logger.Error("retention r2 list failed")
		return
	}
	sort.Slice(objects, func(i, j int) bool { return objects[i].LastModified.After(objects[j].LastModified) })
	finalPrefix := strings.TrimSuffix(d.Prefix, "/") + "/"
	finalIndex := 0
	for _, obj := range objects {
		// Retention owns only final archives directly below the configured
		// prefix. Nested incoming objects belong to CleanupIncoming and must
		// never affect the newest guard or the GFS keep set.
		rel, ok := strings.CutPrefix(obj.Key, finalPrefix)
		if !ok || strings.Contains(rel, "/") || !strings.HasSuffix(rel, ".7z") {
			continue
		}
		stem := strings.TrimSuffix(rel, ".7z")
		if finalIndex == 0 || keep[stem] {
			finalIndex++
			continue
		}
		finalIndex++
		if err := d.Client.DeleteObject(ctx, obj.Key); err != nil {
			r.opts.Logger.Error("retention r2 delete failed")
			continue
		}
		r.auditRetention(obj.Key)
	}
}

// auditRetention records one deletion with the opaque object identifier
// only (T24).
func (r *Runner) auditRetention(ref string) {
	if r.opts.Audit == nil {
		return
	}
	if err := r.opts.Audit.Record(context.Background(), r.opts.DB.DB, audit.Event{
		Name:       audit.EventBackupRetentionDeleted,
		ActorID:    audit.Anonymous,
		TargetType: audit.TargetBackup,
		TargetID:   ref,
		Result:     audit.ResultSuccess,
	}); err != nil {
		r.opts.Logger.Error("retention audit write failed")
	}
}
