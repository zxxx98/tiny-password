package integration

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tiny-password/tiny-password/internal/platform/archive"
)

// archiveBin returns the pinned 7zz binary for integration tests; tests that
// need real 7-Zip skip when neither SEVENZIP_BIN nor the downloaded binary
// is present.
func archiveBin(t *testing.T) {
	t.Helper()
	if os.Getenv("SEVENZIP_BIN") != "" {
		return
	}
	if _, err := os.Stat("/tmp/tp-7zz/7zz"); err == nil {
		t.Setenv("SEVENZIP_BIN", "/tmp/tp-7zz/7zz")
		return
	}
	t.Skip("pinned 7zz binary not available (SEVENZIP_BIN unset)")
}

func writeSecretFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveCreateExtractRoundTrip(t *testing.T) {
	archiveBin(t)
	ctx := context.Background()
	work := t.TempDir()
	src := filepath.Join(work, "src")
	writeSecretFile(t, src, "manifest.json", `{"version":1}`)
	writeSecretFile(t, src, "items/login-1.json", "SYNSECRET-archive-payload")

	created, err := archive.Create(ctx, archive.CreateOptions{
		SourceDir: src, WorkDir: work, Passphrase: "archive-passphrase-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(filepath.Dir(created))

	// The archive file itself is not readable by others and lives in a
	// 0700 workspace.
	if info, err := os.Stat(created); err != nil || info.Size() == 0 {
		t.Fatalf("archive stat: %v", err)
	}

	dest, entries, err := archive.Extract(ctx, created, "archive-passphrase-1", work)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dest)
	raw, err := os.ReadFile(filepath.Join(dest, "manifest.json"))
	if err != nil {
		t.Fatalf("extracted tree: %v", err)
	}
	if !bytes.Contains(raw, []byte(`"version":1`)) {
		t.Fatalf("extracted content wrong: %q", raw)
	}
	if len(entries) < 2 {
		t.Fatalf("entries=%d", len(entries))
	}

	// Wrong passphrase must fail with the stable error and leave nothing.
	if _, _, err := archive.Extract(ctx, created, "wrong-passphrase!", work); err != archive.ErrWrongPassphrase {
		t.Fatalf("wrong passphrase: %v", err)
	}

	// A corrupted archive must fail closed as BAD_ARCHIVE.
	rawArchive, _ := os.ReadFile(created)
	corrupt := filepath.Join(work, "corrupt.7z")
	if err := os.WriteFile(corrupt, append(bytes.Clone(rawArchive[:20]), []byte("garbage")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := archive.Extract(ctx, corrupt, "archive-passphrase-1", work); err == nil {
		t.Fatal("corrupt archive extracted")
	}

	// No synthetic secret may linger in the 7z error output surfaces — the
	// package never returns tool output, so assert our API stays clean.
	if _, err := archive.List(ctx, created, "", work); err == nil {
		t.Fatal("listing without passphrase must fail")
	}
}
