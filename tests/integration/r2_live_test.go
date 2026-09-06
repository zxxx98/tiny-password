package integration

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tiny-password/tiny-password/internal/platform/objectstore"
)

// TestR2Live is the authoritative R2 acceptance (T22): it runs against the
// real Cloudflare R2 service using dedicated TEST credentials and a test
// prefix. Without credentials the test is skipped and reported as not
// completed in the release evidence — a mock must never substitute for the
// live check.
//
// Required environment:
//
//	TP_R2_TEST_ENDPOINT      https://<account>.r2.cloudflarestorage.com
//	TP_R2_TEST_BUCKET        existing test bucket
//	TP_R2_TEST_ACCESS_KEY    token access key id
//	TP_R2_TEST_SECRET_KEY    token secret access key
func TestR2Live(t *testing.T) {
	endpoint := os.Getenv("TP_R2_TEST_ENDPOINT")
	bucket := os.Getenv("TP_R2_TEST_BUCKET")
	accessKey := os.Getenv("TP_R2_TEST_ACCESS_KEY")
	secretKey := os.Getenv("TP_R2_TEST_SECRET_KEY")
	if endpoint == "" || bucket == "" || accessKey == "" || secretKey == "" {
		t.Skip("real R2 credentials not configured (TP_R2_TEST_*); live acceptance not completed")
	}

	client, err := objectstore.NewClient(objectstore.Config{
		Endpoint:        endpoint,
		Region:          "auto",
		Bucket:          bucket,
		AccessKeyID:     accessKey,
		SecretAccessKey: secretKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	key := "tiny-password-live-test/" + time.Now().UTC().Format("20060102T150405Z") + ".bin"
	payload := make([]byte, 1<<20) // 1 MiB of random data
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])

	// PUT with the SigV4-signed payload digest.
	openBody := func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(string(payload))), nil
	}
	if err := client.PutObject(ctx, key, openBody, int64(len(payload)), digest); err != nil {
		t.Fatalf("live PUT: %v", err)
	}
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), time.Minute)
		defer ccancel()
		_ = client.DeleteObject(cctx, key)
		_ = client.DeleteObject(cctx, key+".copy")
	})

	// GET back and verify byte-for-byte.
	back, err := client.GetObject(ctx, key)
	if err != nil {
		t.Fatalf("live GET: %v", err)
	}
	got, err := io.ReadAll(back)
	if err != nil {
		t.Fatalf("live GET read: %v", err)
	}
	_ = back.Close()
	if sha256.Sum256(got) != sum {
		t.Fatal("live GET digest mismatch")
	}

	// HEAD: size must match (never the ETag).
	info, err := client.HeadObject(ctx, key)
	if err != nil {
		t.Fatalf("live HEAD: %v", err)
	}
	if info.Size != int64(len(payload)) {
		t.Fatalf("live HEAD size %d, want %d", info.Size, len(payload))
	}

	// Server-side copy and delete — the delivery contract (T22).
	if err := client.CopyObject(ctx, key, key+".copy"); err != nil {
		t.Fatalf("live COPY: %v", err)
	}
	if _, err := client.HeadObject(ctx, key+".copy"); err != nil {
		t.Fatalf("live HEAD copy: %v", err)
	}
	if err := client.DeleteObject(ctx, key); err != nil {
		t.Fatalf("live DELETE: %v", err)
	}
	if _, err := client.HeadObject(ctx, key); err == nil {
		t.Fatal("live object survived delete")
	}
}
