package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tiny-password/tiny-password/internal/audit"
	"github.com/tiny-password/tiny-password/internal/backup"
)

// seedSucceededRun inserts a succeeded backup_runs row and its archive
// file. Returns the run id used as the file stem.
func seedSucceededRun(t *testing.T, h *backupHarness, id string, startedAt time.Time, localDir string) {
	seedSucceededRunOn(t, h, "local", id, startedAt, localDir)
}

func seedSucceededRunOn(t *testing.T, h *backupHarness, target, id string, startedAt time.Time, localDir string) {
	t.Helper()
	if _, err := h.db.Exec(
		`INSERT INTO backup_runs (id, target, status, triggered_by, started_at, finished_at, size_bytes, sha256)
		 VALUES (?, ?, 'succeeded', 'manual', ?, ?, 10, 'aa')`,
		id, target, startedAt.Format(time.RFC3339Nano), startedAt.Format(time.RFC3339Nano),
	); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(localDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localDir, id+".7z"), []byte("archive:"+id), 0o600); err != nil {
		t.Fatal(err)
	}
}

func configureRetention(t *testing.T, h *backupHarness, daily, weekly, monthly int) {
	configureRetentionTarget(t, h, "local", daily, weekly, monthly)
}

func configureRetentionTarget(t *testing.T, h *backupHarness, target string, daily, weekly, monthly int) {
	t.Helper()
	if _, err := backup.LoadJobConfigs(h.db.DB); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(
		`UPDATE backup_jobs SET retention_daily = ?, retention_weekly = ?, retention_monthly = ? WHERE target = ?`,
		daily, weekly, monthly, target,
	); err != nil {
		t.Fatal(err)
	}
}

func localAuditDeletions(t *testing.T, h *backupHarness) []string {
	t.Helper()
	rows, err := h.db.Query(
		"SELECT target_id FROM audit_events WHERE event = ? ORDER BY created_at, target_id",
		audit.EventBackupRetentionDeleted,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

func TestRetentionLocalReconcilesExactSet(t *testing.T) {
	h := newBackupHarness(t)
	configureRetention(t, h, 2, 1, 1)
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

	// r1: 40 days old (only artifact of its month → dropped: monthly=1
	// points at September). r2: 8 days old (same). r3: 2 days old (the
	// newest artifact of the second-most-recent day → kept by daily=2).
	// r4: 6 hours old (same UTC day as r5 but older → dropped).
	seedSucceededRun(t, h, "r1", now.Add(-40*24*time.Hour), h.localDir)
	seedSucceededRun(t, h, "r2", now.Add(-8*24*time.Hour), h.localDir)
	seedSucceededRun(t, h, "r3", now.Add(-2*24*time.Hour), h.localDir)
	seedSucceededRun(t, h, "r4", now.Add(-6*time.Hour), h.localDir)
	seedSucceededRun(t, h, "r5", now, h.localDir)

	h.runner.ApplyRetention(context.Background(), backup.TargetLocal, backup.RunInput{Targets: []backup.Target{backup.TargetLocal}, LocalDir: h.localDir})

	remaining := map[string]bool{}
	for _, name := range listDirNames(t, h.localDir) {
		remaining[strings.TrimSuffix(name, ".7z")] = true
	}
	if len(remaining) != 2 || !remaining["r3"] || !remaining["r5"] {
		t.Fatalf("remaining files: %v", remaining)
	}
	// Every deletion produced exactly one audit row with the opaque ref.
	if ids := localAuditDeletions(t, h); len(ids) != 3 {
		t.Fatalf("audit deletions: %v", ids)
	}
	// Re-running changes nothing (idempotent reconciliation).
	h.runner.ApplyRetention(context.Background(), backup.TargetLocal, backup.RunInput{Targets: []backup.Target{backup.TargetLocal}, LocalDir: h.localDir})
	if names := listDirNames(t, h.localDir); len(names) != 2 {
		t.Fatalf("after rerun: %v", names)
	}
}

// Retention never deletes when the target has no verified success: a
// failing backup must not cause the removal of the last usable backup.
func TestRetentionGuardWithoutSucceededRuns(t *testing.T) {
	h := newBackupHarness(t)
	configureRetention(t, h, 1, 1, 1)
	// Only a failed run and an old file on disk.
	if _, err := h.db.Exec(
		`INSERT INTO backup_runs (id, target, status, triggered_by, started_at, finished_at, error_code)
		 VALUES ('r-fail', 'local', 'failed', 'manual', ?, ?, 'BACKUP_ARCHIVE_FAILED')`,
		time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(h.localDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.localDir, "r-fail.7z"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.runner.ApplyRetention(context.Background(), backup.TargetLocal, backup.RunInput{Targets: []backup.Target{backup.TargetLocal}, LocalDir: h.localDir})
	if names := listDirNames(t, h.localDir); len(names) != 1 {
		t.Fatalf("guard failed: %v", names)
	}
	if ids := localAuditDeletions(t, h); len(ids) != 0 {
		t.Fatalf("unexpected deletions: %v", ids)
	}
}

// The newest artifact is always spared even when its bookkeeping row is
// missing (crash between publish and row update).
func TestRetentionSparesNewestUntrackedFile(t *testing.T) {
	h := newBackupHarness(t)
	configureRetention(t, h, 1, 1, 1)
	now := time.Now().UTC()
	// new-tracked is the newest succeeded run (kept); old-tracked is
	// outside the 1/1/1 buckets (deleted); the untracked file has the
	// newest mtime of all but no run row.
	seedSucceededRun(t, h, "old-tracked", now.Add(-48*time.Hour), h.localDir)
	seedSucceededRun(t, h, "new-tracked", now.Add(-6*time.Hour), h.localDir)
	untracked := "untracked0000000000000000000000000000"
	untrackedPath := filepath.Join(h.localDir, untracked+".7z")
	if err := os.WriteFile(untrackedPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(untrackedPath, future, future); err != nil {
		t.Fatal(err)
	}
	h.runner.ApplyRetention(context.Background(), backup.TargetLocal, backup.RunInput{Targets: []backup.Target{backup.TargetLocal}, LocalDir: h.localDir})
	remaining := map[string]bool{}
	for _, name := range listDirNames(t, h.localDir) {
		remaining[strings.TrimSuffix(name, ".7z")] = true
	}
	if remaining["old-tracked"] {
		t.Fatalf("old-tracked must be deleted: %v", remaining)
	}
	if !remaining["untracked0000000000000000000000000000"] {
		t.Fatalf("newest untracked file must be spared: %v", remaining)
	}
	if !remaining["new-tracked"] {
		t.Fatalf("new-tracked must be kept: %v", remaining)
	}
}

// R2 reconciliation: exact set, independent of the local target, with
// failed deletions retried on the next pass.
func TestRetentionR2ReconcilesAndRetries(t *testing.T) {
	th := newTargetsHarness(t)
	configureRetentionTarget(t, th.backupHarness, "r2", 1, 0, 0)
	now := time.Now().UTC()

	seedSucceededRun(t, th.backupHarness, "k1", now.Add(-48*time.Hour), th.localDir)
	seedSucceededRun(t, th.backupHarness, "k2", now, th.localDir)
	// R2 runs use distinct ids; objects are named by run id.
	seedSucceededRunOn(t, th.backupHarness, "r2", "rk1", now.Add(-48*time.Hour), th.localDir)
	seedSucceededRunOn(t, th.backupHarness, "r2", "rk2", now, th.localDir)
	for _, id := range []string{"rk1", "rk2"} {
		key := th.prefix + "/" + id + ".7z"
		th.fake.objects[key] = []byte("archive:" + id)
		th.fake.modTime[key] = now.Add(-48 * time.Hour)
		if id == "rk2" {
			th.fake.modTime[key] = now
		}
	}
	input := backup.RunInput{
		Targets:  []backup.Target{backup.TargetR2},
		LocalDir: th.localDir,
		R2:       &backup.R2Delivery{Client: th.client, Prefix: th.prefix},
	}

	// Deleting fails: the object stays (retryable), local is untouched.
	th.fake.failDelete = true
	th.runner.ApplyRetention(context.Background(), backup.TargetR2, input)
	if _, ok := th.fake.objects[th.prefix+"/rk1.7z"]; !ok {
		t.Fatal("rk1 deleted despite injected failure")
	}

	// The retry succeeds; only the newest (rk2) remains.
	th.fake.failDelete = false
	th.runner.ApplyRetention(context.Background(), backup.TargetR2, input)
	if _, ok := th.fake.objects[th.prefix+"/rk1.7z"]; ok {
		t.Fatal("rk1 survived the retry")
	}
	if _, ok := th.fake.objects[th.prefix+"/rk2.7z"]; !ok {
		t.Fatal("rk2 must be kept")
	}
}
