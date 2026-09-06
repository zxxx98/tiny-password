package transfer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestPrepareWorkDirCreatesAndCleansOnlyItsChild(t *testing.T) {
	if !isLinuxEphemeralFS("/dev/shm") {
		t.Skip("/dev/shm is not an available tmpfs")
	}
	parent, err := os.MkdirTemp("/dev/shm", "tiny-password-transfer-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(parent)
	sentinel := filepath.Join(parent, "keep-me")
	if err := os.WriteFile(sentinel, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	child, cleanup, err := PrepareWorkDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if child == parent || filepath.Dir(child) != parent {
		t.Fatalf("child=%q parent=%q", child, parent)
	}
	if info, err := os.Stat(child); err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("work child is not a restricted directory: info=%v err=%v", info, err)
	}
	cleanup()
	if _, err := os.Stat(child); !os.IsNotExist(err) {
		t.Fatalf("child still exists after cleanup: %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("cleanup removed parent content: %v", err)
	}
}

func TestPrepareWorkDirRejectsNonTmpfsParent(t *testing.T) {
	if isLinuxEphemeralFS(t.TempDir()) {
		t.Skip("test filesystem is tmpfs")
	}
	if _, _, err := PrepareWorkDir(t.TempDir()); err == nil {
		t.Fatal("PrepareWorkDir accepted a non-tmpfs parent")
	}
}

func TestSweepDeletesStaleIncompletePreviewButNotFreshOne(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "preview-old")
	fresh := filepath.Join(root, "preview-fresh")
	for _, dir := range []string{old, fresh} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	oldTime := time.Now().Add(-previewCreationGrace - time.Second)
	if err := os.Chtimes(old, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	svc := &Service{workDir: root, now: time.Now}
	svc.sweepExpiredPreviews()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("stale incomplete preview was retained: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh incomplete preview was swept: %v", err)
	}
}

func TestManifestSelfCheckRejectsUnsafeOrInconsistentEntries(t *testing.T) {
	validID := "018f2f2e-2f69-7abc-8def-0123456789ab"
	base := Manifest{
		Version: FormatVersion,
		Counts:  map[string]int{"secure_note": 1},
		Files:   []ManifestFile{{ID: validID, Type: "secure_note", Scope: "personal", Path: "items/" + validID + ".json", SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}},
	}
	if err := ManifestSelfCheck(base); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	tests := []Manifest{
		func() Manifest {
			m := base
			m.Files = append([]ManifestFile(nil), base.Files...)
			m.Files[0].Path = "items/../secret.json"
			return m
		}(),
		func() Manifest {
			m := base
			m.Files = append([]ManifestFile(nil), base.Files...)
			m.Files[0].ID = validID
			m.Files[0].Path = "items/" + validID + ".json"
			m.Files = append(m.Files, m.Files[0])
			m.Counts["secure_note"] = 2
			return m
		}(),
		func() Manifest { m := base; m.Counts["secure_note"] = 2; return m }(),
	}
	for i, manifest := range tests {
		if err := ManifestSelfCheck(manifest); err == nil {
			t.Fatalf("manifest case %d unexpectedly accepted", i)
		}
	}
}

func TestClaimPreviewHasOneAtomicWinner(t *testing.T) {
	root := t.TempDir()
	stage := filepath.Join(root, "preview-token")
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	winners := make(chan string, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, err := claimPreview(stage)
			if err == nil {
				winners <- claimed
			}
		}()
	}
	wg.Wait()
	close(winners)
	if got := len(winners); got != 1 {
		t.Fatalf("claim winners=%d, want 1", got)
	}
	claimed := <-winners
	if filepath.Base(claimed) != "consuming-token" {
		t.Fatalf("claimed path=%q", claimed)
	}
	if _, err := os.Stat(claimed); err != nil {
		t.Fatalf("claimed directory missing: %v", err)
	}
}

func TestPreviewSweeperRunsWithoutAnotherRequest(t *testing.T) {
	root := t.TempDir()
	stage := filepath.Join(root, "preview-expired")
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	meta, err := json.Marshal(previewMeta{Actor: "user", Expiry: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "meta.json"), meta, 0o600); err != nil {
		t.Fatal(err)
	}
	svc := &Service{workDir: root, now: func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }}
	ctx, cancel := context.WithCancel(context.Background())
	svc.Start(ctx)
	defer cancel()
	defer svc.Close()
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(stage); os.IsNotExist(err) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("expired preview was not swept without a new request")
}
