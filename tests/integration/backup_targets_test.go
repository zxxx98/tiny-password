package integration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tiny-password/tiny-password/internal/backup"
	"github.com/tiny-password/tiny-password/internal/platform/objectstore"
)

// fakeR2 emulates the S3 subset the backup adapter uses: PUT, GET, HEAD,
// DELETE, server-side COPY and ListObjectsV2 — enough to exercise the full
// temp→verify→copy→verify→cleanup delivery without a real R2 account.
type fakeR2 struct {
	mu      sync.Mutex
	objects map[string][]byte
	modTime map[string]time.Time
	ops     map[string]int // "PUT /bucket/key" style counters

	// Behavior injection.
	failPutStatus  int // status to answer PUT with
	failPutTimes   int // number of failing PUTs left (0 = unlimited)
	alwaysFailPut  bool
	corruptGetBody bool
	failCopyStatus int
}

func newFakeR2() *fakeR2 {
	return &fakeR2{
		objects: map[string][]byte{},
		modTime: map[string]time.Time{},
		ops:     map[string]int{},
	}
}

func (f *fakeR2) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	bucketAndKey := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.SplitN(bucketAndKey, "/", 2)
	if len(parts) == 1 && parts[0] == "test-bucket" && r.URL.Query().Get("list-type") == "2" {
		// Bucket-level listing request.
		f.list(w, r)
		return
	}
	if len(parts) != 2 || parts[0] != "test-bucket" {
		http.Error(w, "no such bucket", http.StatusNotFound)
		return
	}
	key := parts[1]
	f.ops[r.Method+" "+key]++

	switch r.Method {
	case http.MethodPut:
		if r.Header.Get("x-amz-copy-source") != "" {
			src := strings.TrimPrefix(r.Header.Get("x-amz-copy-source"), "/test-bucket/")
			body, ok := f.objects[src]
			if !ok {
				http.Error(w, "NoSuchKey", http.StatusNotFound)
				return
			}
			if f.failCopyStatus != 0 {
				http.Error(w, "copy rejected", f.failCopyStatus)
				return
			}
			f.objects[key] = append([]byte(nil), body...)
			f.modTime[key] = time.Now().UTC()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<CopyObjectResult><ETag>"copy"</ETag></CopyObjectResult>`))
			return
		}
		if f.failPutStatus != 0 && (f.alwaysFailPut || f.failPutTimes > 0) {
			if !f.alwaysFailPut {
				f.failPutTimes--
			}
			http.Error(w, "injected failure", f.failPutStatus)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		f.objects[key] = raw
		f.modTime[key] = time.Now().UTC()
		w.WriteHeader(http.StatusOK)

	case http.MethodGet:
		if r.URL.Query().Get("list-type") == "2" {
			f.list(w, r)
			return
		}
		body, ok := f.objects[key]
		if !ok {
			http.Error(w, "NoSuchKey", http.StatusNotFound)
			return
		}
		out := body
		if f.corruptGetBody && len(out) > 0 {
			out = append([]byte(nil), out...)
			out[0] ^= 0xFF
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(out)

	case http.MethodHead:
		body, ok := f.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.WriteHeader(http.StatusOK)

	case http.MethodDelete:
		delete(f.objects, key)
		delete(f.modTime, key)
		w.WriteHeader(http.StatusNoContent)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// list serves a ListObjectsV2 response over the requested prefix.
func (f *fakeR2) list(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	type item struct {
		Key          string    `xml:"Key"`
		LastModified time.Time `xml:"LastModified"`
	}
	type listing struct {
		XMLName  xml.Name `xml:"ListBucketResult"`
		Contents []item   `xml:"Contents"`
	}
	out := listing{}
	for k, mt := range f.modTime {
		if strings.HasPrefix(k, prefix) {
			out.Contents = append(out.Contents, item{Key: k, LastModified: mt})
		}
	}
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	_ = xml.NewEncoder(w).Encode(out)
}

// targetsHarness couples the backup harness with a fake R2 server.
type targetsHarness struct {
	*backupHarness
	fake   *fakeR2
	server *httptest.Server
	client *objectstore.Client
	prefix string
}

func newTargetsHarness(t *testing.T) *targetsHarness {
	t.Helper()
	h := &targetsHarness{backupHarness: newBackupHarness(t), fake: newFakeR2(), prefix: "backups/test-instance"}
	h.server = httptest.NewServer(http.HandlerFunc(h.fake.handler))
	t.Cleanup(h.server.Close)
	client, err := objectstore.NewClient(objectstore.Config{
		Endpoint:        h.server.URL,
		Bucket:          "test-bucket",
		AccessKeyID:     "test-access",
		SecretAccessKey: "test-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	h.client = client
	return h
}

func (h *targetsHarness) runBoth(t *testing.T, runner *backup.Runner) (*backup.RunSummary, error) {
	t.Helper()
	return runner.Run(context.Background(), backup.RunInput{
		Targets:    []backup.Target{backup.TargetLocal, backup.TargetR2},
		Trigger:    backup.TriggerManual,
		Passphrase: "backup-passphrase-1",
		LocalDir:   h.localDir,
		R2: &backup.R2Delivery{
			Client: h.client,
			Prefix: h.prefix,
		},
	})
}

func (h *targetsHarness) runsForTarget(t *testing.T, target string) (status, code string) {
	t.Helper()
	var statusNull, codeNull sql.NullString
	err := h.db.QueryRow(
		"SELECT status, error_code FROM backup_runs WHERE target = ? ORDER BY rowid DESC LIMIT 1",
		target,
	).Scan(&statusNull, &codeNull)
	if err != nil {
		t.Fatal(err)
	}
	return statusNull.String, codeNull.String
}

func TestBackupTargetsBothSucceedIndependently(t *testing.T) {
	h := newTargetsHarness(t)
	summary, err := h.runBoth(t, h.runner)
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	local := summary.For(backup.TargetLocal)
	r2res := summary.For(backup.TargetR2)
	if local.Err != nil || r2res.Err != nil {
		t.Fatalf("unexpected errors: %v %v", local.Err, r2res.Err)
	}

	// Local: published file with the summary digest.
	raw, err := os.ReadFile(local.Path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != local.SHA256 {
		t.Fatal("local digest mismatch")
	}

	// R2: exactly the final object, no leftover temporary object, bytes
	// equal to the local archive (digest verified by the delivery itself).
	var finalKeys []string
	for k := range h.fake.objects {
		if strings.HasPrefix(k, h.prefix+"/incoming/") {
			t.Fatalf("temporary object leaked: %s", k)
		}
		finalKeys = append(finalKeys, k)
	}
	if len(finalKeys) != 1 || !strings.HasPrefix(finalKeys[0], h.prefix+"/") {
		t.Fatalf("r2 objects: %v", finalKeys)
	}
	remoteSum := sha256.Sum256(h.fake.objects[finalKeys[0]])
	if hex.EncodeToString(remoteSum[:]) != r2res.SHA256 {
		t.Fatal("r2 digest mismatch")
	}

	// Two independent succeeded rows: one per target.
	if status, _ := h.runsForTarget(t, "local"); status != "succeeded" {
		t.Fatalf("local row: %s", status)
	}
	if status, _ := h.runsForTarget(t, "r2"); status != "succeeded" {
		t.Fatalf("r2 row: %s", status)
	}
}

func TestBackupTargetsLocalSucceedsWhenR2Fails(t *testing.T) {
	h := newTargetsHarness(t)
	h.fake.failPutStatus = http.StatusServiceUnavailable
	h.fake.alwaysFailPut = true

	summary, err := h.runBoth(t, h.runner)
	if err == nil {
		t.Fatal("expected overall error")
	}
	local := summary.For(backup.TargetLocal)
	r2res := summary.For(backup.TargetR2)
	if local.Err != nil {
		t.Fatalf("local must be unaffected: %v", local.Err)
	}
	if r2res.Err == nil {
		t.Fatal("r2 must fail")
	}
	if status, code := h.runsForTarget(t, "local"); status != "succeeded" || code != "" {
		t.Fatalf("local row: %s/%s", status, code)
	}
	if status, code := h.runsForTarget(t, "r2"); status != "failed" || code != "BACKUP_UPLOAD_FAILED" {
		t.Fatalf("r2 row: %s/%s", status, code)
	}
	// The local backup exists and the R2 failure did not unpublish it.
	if names := listDirNames(t, h.localDir); len(names) != 1 {
		t.Fatalf("local backups: %v", names)
	}
}

func TestBackupTargetsR2SucceedsWhenLocalFails(t *testing.T) {
	h := newTargetsHarness(t)
	// A read-only publish dir: the space gate passes but LocalStore.Publish
	// cannot create its temporary file.
	if err := os.MkdirAll(h.localDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(h.localDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(h.localDir, 0o700) })
	summary, err := h.runner.Run(context.Background(), backup.RunInput{
		Targets:    []backup.Target{backup.TargetLocal, backup.TargetR2},
		Trigger:    backup.TriggerManual,
		Passphrase: "backup-passphrase-1",
		LocalDir:   h.localDir,
		R2: &backup.R2Delivery{
			Client: h.client,
			Prefix: h.prefix,
		},
	})
	if err == nil {
		t.Fatal("expected overall error")
	}
	if summary == nil {
		t.Fatal("expected a summary despite partial failure")
	}
	local := summary.For(backup.TargetLocal)
	r2res := summary.For(backup.TargetR2)
	if local.Err == nil {
		t.Fatal("local must fail")
	}
	if r2res.Err != nil {
		t.Fatalf("r2 must succeed: %v", r2res.Err)
	}
	if status, code := h.runsForTarget(t, "local"); status != "failed" || code != "BACKUP_PUBLISH_FAILED" {
		t.Fatalf("local row: %s/%s", status, code)
	}
	if status, _ := h.runsForTarget(t, "r2"); status != "succeeded" {
		t.Fatalf("r2 row: %s", status)
	}
	if len(h.fake.objects) != 1 {
		t.Fatalf("r2 objects: %v", h.fake.objects)
	}
}

func TestBackupTargetsArchiveFailureFailsBoth(t *testing.T) {
	h := newTargetsHarness(t)
	corrupt := func(_ context.Context, path string) error {
		return os.WriteFile(path, []byte("corrupt"), 0o600)
	}
	runner := h.newRunner(t, backup.Hooks{AfterArchiveCreated: corrupt})
	if _, err := h.runBoth(t, runner); err == nil {
		t.Fatal("expected archive failure")
	}
	// Both rows failed with the shared archive-verification code.
	for _, target := range []string{"local", "r2"} {
		if status, code := h.runsForTarget(t, target); status != "failed" || code != "BACKUP_VERIFY_FAILED" {
			t.Fatalf("%s row: %s/%s", target, status, code)
		}
	}
	if len(h.fake.objects) != 0 {
		t.Fatalf("nothing may reach r2: %v", h.fake.objects)
	}
}

func TestBackupR2RetriesTransientFailures(t *testing.T) {
	h := newTargetsHarness(t)
	h.fake.failPutStatus = http.StatusTooManyRequests
	h.fake.failPutTimes = 2 // two 429s, then success

	summary, err := h.runBoth(t, h.runner)
	if err != nil {
		t.Fatalf("run should succeed after retries: %v", err)
	}
	if res := summary.For(backup.TargetR2); res.Err != nil {
		t.Fatalf("r2: %v", res.Err)
	}
	puts := 0
	for op, n := range h.fake.ops {
		if strings.HasPrefix(op, "PUT") {
			puts += n
		}
	}
	if puts < 3 {
		t.Fatalf("expected retried PUTs, got %d", puts)
	}
}

func TestBackupR2PermanentErrorFailsImmediately(t *testing.T) {
	h := newTargetsHarness(t)
	h.fake.failPutStatus = http.StatusForbidden
	h.fake.alwaysFailPut = true

	_, err := h.runBoth(t, h.runner)
	if err == nil {
		t.Fatal("expected failure")
	}
	if status, code := h.runsForTarget(t, "r2"); status != "failed" || code != "BACKUP_UPLOAD_FAILED" {
		t.Fatalf("r2 row: %s/%s", status, code)
	}
	puts := 0
	for op, n := range h.fake.ops {
		if strings.HasPrefix(op, "PUT") {
			puts += n
		}
	}
	if puts != 1 {
		t.Fatalf("permanent error must not retry, got %d PUTs", puts)
	}
}

func TestBackupR2ReadbackMismatchFailsAndTempObjectIsCleanable(t *testing.T) {
	h := newTargetsHarness(t)
	h.fake.corruptGetBody = true

	_, err := h.runBoth(t, h.runner)
	if err == nil {
		t.Fatal("expected verification failure")
	}
	if status, code := h.runsForTarget(t, "r2"); status != "failed" || code != "BACKUP_UPLOAD_VERIFY_FAILED" {
		t.Fatalf("r2 row: %s/%s", status, code)
	}
	// The temporary object could not be removed by the failed delivery;
	// the cleanup path must be able to recover it once it ages past the TTL.
	if len(h.fake.objects) == 0 {
		t.Fatal("expected a leftover temporary object")
	}
	aged := backup.R2Delivery{
		Client: h.client, Prefix: h.prefix,
		Now: func() time.Time { return time.Now().UTC().Add(25 * time.Hour) },
	}
	removed, err := aged.CleanupIncoming(context.Background())
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if removed == 0 || len(h.fake.objects) != 0 {
		t.Fatalf("cleanup removed %d, left %d objects", removed, len(h.fake.objects))
	}
}

func TestBackupR2CleanupIncomingRespectsAge(t *testing.T) {
	h := newTargetsHarness(t)
	oldKey := h.prefix + "/incoming/old.7z"
	freshKey := h.prefix + "/incoming/fresh.7z"
	finalKey := h.prefix + "/final.7z"
	for _, k := range []string{oldKey, freshKey, finalKey} {
		h.fake.objects[k] = []byte("data")
		h.fake.modTime[k] = time.Now().UTC()
	}
	// The old object predates the TTL; the fresh one does not.
	h.fake.modTime[oldKey] = time.Now().UTC().Add(-48 * time.Hour)

	removed, err := backup.R2Delivery{Client: h.client, Prefix: h.prefix}.CleanupIncoming(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed %d objects", removed)
	}
	if _, ok := h.fake.objects[oldKey]; ok {
		t.Fatal("old incoming object survived")
	}
	for _, k := range []string{freshKey, finalKey} {
		if _, ok := h.fake.objects[k]; !ok {
			t.Fatalf("%s was wrongly removed", k)
		}
	}
}

// The SigV4 signer must produce standard signatures; verify against an
// independent implementation of the AWS signing procedure for a sample
// request (the live R2 acceptance in r2_live_test.go is the authoritative
// end-to-end check).
func TestObjectstoreSigV4AgainstReference(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		ok := sigv4ReferenceCheck(r, auth, "test-secret")
		if !ok {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client, err := objectstore.NewClient(objectstore.Config{
		Endpoint: srv.URL, Bucket: "test-bucket",
		AccessKeyID: "test-access", SecretAccessKey: "test-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("hello signed world")
	sum := sha256.Sum256(payload)
	openBody := func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(string(payload))), nil }
	err = client.PutObject(context.Background(), "dir/key.txt", openBody, int64(len(payload)), hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatalf("reference signature rejected: %v", err)
	}
}
