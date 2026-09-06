// Package archive drives the pinned 7-Zip CLI for the personal
// import/export archives (design §6.5, §11; decision D04). The invocation
// contract comes from scripts/probes/archive-password.sh:
//
//	create : 7zz a -t7z -mhe=on -p <archive> <files>   (bare -p REQUIRED;
//	         without it 7zz silently creates an UNENCRYPTED archive)
//	list   : 7zz l -slt <archive>                      (no -p flag)
//	test   : 7zz t <archive>
//	extract: 7zz x -y <archive> -o <dir>
//
// For list/test/extract the passphrase is read from stdin only when needed.
// Passphrases never appear in argv, environment, logs, or error text.
package archive

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Limits from the T01 decision record (D09/D10): a personal archive's
// compressed body may be 64 MiB, its extracted content 128 MiB with at most
// 512 files.
const (
	MaxArchiveBytes = 64 << 20
	MaxExtractBytes = 128 << 20
	MaxFiles        = 512
	// MaxArchiveBytesOnDisk caps what the caller may hand us before 7z sees it.
	// extractTimeout bounds every 7-Zip invocation.
	extractTimeout = 120 * time.Second
)

// Limits parameterizes the archive size caps and per-invocation budget.
// Personal import/export uses personalLimits; instance backups pass their
// own explicitly configured caps (T01: instance archive limits are
// configuration, not the personal constants).
type Limits struct {
	MaxArchiveBytes int64
	MaxExtractBytes int64
	MaxFiles        int
	Timeout         time.Duration
}

// personalLimits reproduces the personal-archive constants exactly.
var personalLimits = Limits{
	MaxArchiveBytes: MaxArchiveBytes,
	MaxExtractBytes: MaxExtractBytes,
	MaxFiles:        MaxFiles,
	Timeout:         extractTimeout,
}

func (l Limits) withDefaults() Limits {
	if l.MaxArchiveBytes <= 0 {
		l.MaxArchiveBytes = MaxArchiveBytes
	}
	if l.MaxExtractBytes <= 0 {
		l.MaxExtractBytes = MaxExtractBytes
	}
	if l.MaxFiles <= 0 {
		l.MaxFiles = MaxFiles
	}
	if l.Timeout <= 0 {
		l.Timeout = extractTimeout
	}
	return l
}

// Errors surfaced to the HTTP layer.
var (
	// ErrWrongPassphrase reports a decryption failure (wrong passphrase).
	ErrWrongPassphrase = errors.New("archive: wrong passphrase")
	// ErrBadArchive reports an unreadable, corrupt or disallowed archive.
	ErrBadArchive = errors.New("archive: bad archive")
	// ErrTooLarge reports an archive or extracted tree beyond the limits.
	ErrTooLarge = errors.New("archive: exceeds size limits")
)

// binaryPath resolves the pinned 7zz binary: SEVENZIP_BIN first, then PATH.
func binaryPath() (string, error) {
	if p := os.Getenv("SEVENZIP_BIN"); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("%w: SEVENZIP_BIN unreadable", ErrBadArchive)
		}
		return p, nil
	}
	p, err := exec.LookPath("7zz")
	if err != nil {
		return "", fmt.Errorf("%w: 7zz binary not found (SEVENZIP_BIN unset)", ErrBadArchive)
	}
	return p, nil
}

// opGate bounds concurrent 7-Zip processes for the whole process.
var opGate = make(chan struct{}, 2)

// run executes one 7zz invocation with the passphrase piped to stdin, a
// bounded lifetime, and captured output. Output is returned for error
// classification but NEVER logged. When dir is non-empty the process runs
// with that working directory so archived paths stay relative.
func run(ctx context.Context, dir string, args []string, stdin string) (string, error) {
	bin, err := binaryPath()
	if err != nil {
		return "", err
	}
	select {
	case opGate <- struct{}{}:
		defer func() { <-opGate }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return out.String(), err
	}
	return out.String(), nil
}

// classify maps a 7zz failure onto the stable errors without echoing any of
// the tool's output (which could contain paths but never secrets).
func classify(output string, runErr error) error {
	if errors.Is(runErr, context.DeadlineExceeded) || errors.Is(runErr, context.Canceled) {
		return fmt.Errorf("%w: timed out", ErrBadArchive)
	}
	msg := strings.ToLower(output)
	if strings.Contains(msg, "wrong password") || strings.Contains(msg, "enter password") {
		return ErrWrongPassphrase
	}
	if strings.Contains(msg, "cannot open") && strings.Contains(msg, "as archive") {
		return ErrBadArchive
	}
	return fmt.Errorf("%w: 7-zip failed", ErrBadArchive)
}

// tempWorkspace creates a 0700 directory (a tmpfs mount in deployment, D11)
// with a random subdirectory per operation.
func tempWorkspace(base string) (string, error) {
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", err
	}
	dir := filepath.Join(base, "archive-"+hex.EncodeToString(rnd[:]))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// Entry is one file inside an archive as reported by `l -slt`.
type Entry struct {
	Path       string
	Size       int64
	IsDir      bool
	Attributes string
}

var sltName = regexp.MustCompile(`(?m)^Path = (.+)$`)
var sltSize = regexp.MustCompile(`(?m)^Size = (\d+)$`)
var sltAttributes = regexp.MustCompile(`(?m)^Attributes = (.{1,40})$`)

var nestedArchiveSuffix = regexp.MustCompile(`(?i)\.(7z|zip|tar|gz|bz2|xz|rar|cab|iso|dmg|jar)$`)

// ValidateEntries enforces the personal unpack whitelist (plan T19):
// relative clean paths only, no traversal or absolute paths, no duplicates,
// no nested archives, no device entries, and the count/total-size limits.
func ValidateEntries(entries []Entry) error {
	return validateEntries(entries, personalLimits)
}

func validateEntries(entries []Entry, limits Limits) error {
	limits = limits.withDefaults()
	if len(entries) == 0 {
		return fmt.Errorf("%w: archive is empty", ErrBadArchive)
	}
	if len(entries) > limits.MaxFiles {
		return fmt.Errorf("%w: more than %d files", ErrTooLarge, limits.MaxFiles)
	}
	seen := make(map[string]bool, len(entries))
	var total int64
	for _, e := range entries {
		if e.Path == "" {
			return fmt.Errorf("%w: empty entry path", ErrBadArchive)
		}
		if strings.Contains(e.Path, "\\") || strings.HasPrefix(e.Path, "/") ||
			strings.Contains(e.Path, "../") || e.Path == ".." || strings.HasPrefix(e.Path, "../") {
			return fmt.Errorf("%w: unsafe entry path", ErrBadArchive)
		}
		clean := filepath.Clean(e.Path)
		if clean == ".." || strings.HasPrefix(clean, "../") || clean != e.Path {
			return fmt.Errorf("%w: unsafe entry path", ErrBadArchive)
		}
		if seen[e.Path] {
			return fmt.Errorf("%w: duplicate entry", ErrBadArchive)
		}
		seen[e.Path] = true
		if !e.IsDir {
			if nestedArchiveSuffix.MatchString(e.Path) {
				return fmt.Errorf("%w: nested archive not allowed", ErrBadArchive)
			}
			if strings.Contains(e.Attributes, "lnx") {
				return fmt.Errorf("%w: symlink not allowed", ErrBadArchive)
			}
			total += e.Size
		}
	}
	if total > limits.MaxExtractBytes {
		return fmt.Errorf("%w: extracted size beyond %d bytes", ErrTooLarge, limits.MaxExtractBytes)
	}
	return nil
}

// parseSLT splits `l -slt` output into per-file blocks and extracts the
// fields this package needs. The first block is the archive's own metadata
// (its absolute path would otherwise look like an unsafe entry); file
// entries start after the `----------` separator line.
func parseSLT(out string) []Entry {
	var entries []Entry
	if idx := strings.LastIndex(out, "\n----------\n"); idx >= 0 {
		out = out[idx+len("\n----------\n"):]
	}
	blocks := strings.Split(out, "\n\n")
	for _, block := range blocks {
		if !strings.Contains(block, "Path = ") {
			continue
		}
		name := sltName.FindStringSubmatch(block)
		size := sltSize.FindStringSubmatch(block)
		attrs := sltAttributes.FindStringSubmatch(block)
		if name == nil {
			continue
		}
		e := Entry{Path: strings.TrimSpace(name[1])}
		if size != nil {
			fmt.Sscanf(size[1], "%d", &e.Size)
		}
		if attrs != nil {
			e.Attributes = strings.TrimSpace(attrs[1])
			e.IsDir = strings.Contains(e.Attributes, "D")
		}
		entries = append(entries, e)
	}
	return entries
}

// CreateOptions describes one archive creation.
type CreateOptions struct {
	// SourceDir holds the files to archive (flat or nested); every regular
	// file under it is included.
	SourceDir string
	// WorkDir is the restricted scratch base (tmpfs in deployment).
	WorkDir    string
	Passphrase string
	// Timeout bounds the 7zz invocation; zero uses the personal default.
	Timeout time.Duration
}

// Create packs SourceDir's files into one header-encrypted 7z archive and
// returns its path (inside WorkDir). The caller owns cleanup of WorkDir.
func Create(ctx context.Context, opts CreateOptions) (string, error) {
	if opts.Passphrase == "" {
		return "", fmt.Errorf("%w: empty passphrase", ErrBadArchive)
	}
	if strings.ContainsAny(opts.Passphrase, "\r\n") {
		return "", fmt.Errorf("%w: passphrase must not contain line breaks", ErrBadArchive)
	}
	work, err := tempWorkspace(opts.WorkDir)
	if err != nil {
		return "", err
	}
	archivePath := filepath.Join(work, "archive.7z")
	// `a -t7z -mhe=on -p <archive> <dir>`: the bare -p makes 7zz prompt for
	// the passphrase on stdin (probe contract; a missing -p silently yields
	// an UNENCRYPTED archive). -mhe encrypts the file names; running inside
	// the source dir keeps archived paths relative.
	args := []string{"a", "-t7z", "-mhe=on", "-p", archivePath, "."}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = extractTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := run(ctx, opts.SourceDir, args, opts.Passphrase+"\n")
	if err != nil {
		os.RemoveAll(work)
		return "", classify(out, err)
	}
	// Trust nothing: verify the created archive decrypts and lists cleanly.
	if _, err := List(ctx, archivePath, opts.Passphrase, opts.WorkDir); err != nil {
		os.RemoveAll(work)
		return "", err
	}
	return archivePath, nil
}

// List reads the archive's file table. The passphrase is piped on stdin.
func List(ctx context.Context, archivePath, passphrase, workDir string) ([]Entry, error) {
	work, err := tempWorkspace(workDir)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	args := []string{"l", "-slt", archivePath}
	ctx, cancel := context.WithTimeout(ctx, extractTimeout)
	defer cancel()
	out, err := run(ctx, "", args, passphrase+"\n")
	if err != nil {
		return nil, classify(out, err)
	}
	return parseSLT(out), nil
}

// Extract unpacks the archive into a fresh 0700 directory after validating
// the file table, then re-verifies the extracted tree on disk. Returns the
// destination directory; the caller owns cleanup. Personal-archive limits
// apply.
func Extract(ctx context.Context, archivePath, passphrase, workDir string) (string, []Entry, error) {
	return ExtractLimited(ctx, archivePath, passphrase, workDir, personalLimits)
}

// ExtractLimited is Extract with explicit limits (instance backups).
func ExtractLimited(ctx context.Context, archivePath, passphrase, workDir string, limits Limits) (string, []Entry, error) {
	limits = limits.withDefaults()
	info, err := os.Stat(archivePath)
	if err != nil {
		return "", nil, fmt.Errorf("%w: unreadable archive", ErrBadArchive)
	}
	if info.Size() > limits.MaxArchiveBytes {
		return "", nil, ErrTooLarge
	}
	entries, err := List(ctx, archivePath, passphrase, workDir)
	if err != nil {
		return "", nil, err
	}
	if err := validateEntries(entries, limits); err != nil {
		return "", nil, err
	}
	dest, err := tempWorkspace(workDir)
	if err != nil {
		return "", nil, err
	}
	args := []string{"x", "-y", archivePath, "-o" + dest}
	ctx, cancel := context.WithTimeout(ctx, limits.Timeout)
	defer cancel()
	out, runErr := run(ctx, "", args, passphrase+"\n")
	if runErr != nil {
		os.RemoveAll(dest)
		return "", nil, classify(out, runErr)
	}
	// Re-verify on disk: header declarations are not trusted.
	if err := verifyExtracted(dest, limits); err != nil {
		os.RemoveAll(dest)
		return "", nil, err
	}
	return dest, entries, nil
}

// verifyExtracted walks the extracted tree and enforces the same whitelist
// and size limits against what is actually on disk.
func verifyExtracted(dest string, limits Limits) error {
	limits = limits.withDefaults()
	var files, total int64
	return filepath.WalkDir(dest, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("%w: unreadable extracted entry", ErrBadArchive)
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%w: non-regular extracted entry", ErrBadArchive)
		}
		files++
		total += info.Size()
		if files > int64(limits.MaxFiles) || total > limits.MaxExtractBytes {
			return ErrTooLarge
		}
		_ = path
		return nil
	})
}

// TempFileUnder creates a 0600 scratch file with random name under base —
// used by the transfer package for uploaded archives (D11: restricted dir).
func TempFileUnder(base string) (*os.File, error) {
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return nil, err
	}
	dir, err := tempWorkspace(base)
	if err != nil {
		return nil, err
	}
	return os.CreateTemp(dir, "upload-"+hex.EncodeToString(rnd[:]))
}
