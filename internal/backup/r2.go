package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/tiny-password/tiny-password/internal/platform/ident"
	"github.com/tiny-password/tiny-password/internal/platform/objectstore"
)

// Stable error codes for the R2 delivery phase (display-safe; persisted in
// backup_runs.error_code).
const (
	CodeUploadFailed       = "BACKUP_UPLOAD_FAILED"
	CodeUploadVerifyFailed = "BACKUP_UPLOAD_VERIFY_FAILED"
)

// Errors for the R2 delivery phase.
var (
	// ErrUpload reports a failed upload/copy/delete against R2.
	ErrUpload = errors.New("backup: r2 delivery failed")
	// ErrUploadVerify reports a failed read-back or final-object check.
	ErrUploadVerify = errors.New("backup: r2 delivery verification failed")
)

// R2Delivery configures the R2 target. Credentials live inside the Client
// (loaded from secret files by the operator); they are never persisted in
// the database or echoed back (T22).
type R2Delivery struct {
	Client *objectstore.Client
	// Prefix is the object key namespace, e.g. "tiny-password".
	Prefix string
	// IncomingTTL bounds how long an interrupted temporary object may live
	// before CleanupIncoming removes it. Zero uses 24h.
	IncomingTTL time.Duration
	// Now is injectable for tests.
	Now func() time.Time
}

func (d R2Delivery) ttl() time.Duration {
	if d.IncomingTTL > 0 {
		return d.IncomingTTL
	}
	return 24 * time.Hour
}

func (d R2Delivery) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d R2Delivery) incomingKey(backupID string) string {
	return d.Prefix + "/incoming/" + backupID + "-" + ident.NewUUIDv7() + ".7z"
}

func (d R2Delivery) finalKey(backupID string) string {
	return d.Prefix + "/" + backupID + ".7z"
}

// Deliver executes the T22 contract: upload one unique temporary object,
// read it back and verify the archive SHA-256, copy it to the final key,
// verify the final object exists with the expected size, then delete the
// temporary object. ETags are never used as integrity evidence.
func (d R2Delivery) Deliver(ctx context.Context, stagedPath string, size int64, shaHex, backupID string) (string, error) {
	tmpKey := d.incomingKey(backupID)
	openBody := func() (io.ReadCloser, error) { return os.Open(stagedPath) }
	if err := d.Client.PutObject(ctx, tmpKey, openBody, size, shaHex); err != nil {
		return "", classifyUpload(err, false)
	}

	// Read back the temporary object and verify the digest byte-for-byte.
	back, err := d.Client.GetObject(ctx, tmpKey)
	if err != nil {
		return "", classifyUpload(err, true)
	}
	sum := sha256.New()
	if _, err := io.Copy(sum, back); err != nil {
		_ = back.Close()
		return "", fmt.Errorf("%w: read-back failed", ErrUploadVerify)
	}
	if err := back.Close(); err != nil {
		return "", fmt.Errorf("%w: read-back failed", ErrUploadVerify)
	}
	if hex.EncodeToString(sum.Sum(nil)) != shaHex {
		return "", fmt.Errorf("%w: read-back digest mismatch", ErrUploadVerify)
	}

	finalKey := d.finalKey(backupID)
	if err := d.Client.CopyObject(ctx, tmpKey, finalKey); err != nil {
		return "", classifyUpload(err, true)
	}
	info, err := d.Client.HeadObject(ctx, finalKey)
	if err != nil {
		return "", classifyUpload(err, true)
	}
	if info.Size != size {
		return "", fmt.Errorf("%w: final object size %d, want %d", ErrUploadVerify, info.Size, size)
	}
	// HEAD only proves the advertised length. Read back the final bytes with
	// a one-byte bound so a same-size corruption (or an oversized response)
	// cannot be accepted before the temporary object is removed.
	final, err := d.Client.GetObject(ctx, finalKey)
	if err != nil {
		return "", classifyUpload(err, true)
	}
	finalSum := sha256.New()
	readN, readErr := io.Copy(finalSum, io.LimitReader(final, size+1))
	closeErr := final.Close()
	if readErr != nil || closeErr != nil {
		return "", fmt.Errorf("%w: final read-back failed", ErrUploadVerify)
	}
	if readN != size {
		return "", fmt.Errorf("%w: final object size %d, want %d", ErrUploadVerify, readN, size)
	}
	if hex.EncodeToString(finalSum.Sum(nil)) != shaHex {
		return "", fmt.Errorf("%w: final read-back digest mismatch", ErrUploadVerify)
	}

	// The final object is delivered and verified. A temporary object whose
	// delete failed lingers only until CleanupIncoming sweeps it (TTL); it
	// must never turn a verified delivery into a failed run.
	_ = d.Client.DeleteObject(ctx, tmpKey)
	return finalKey, nil
}

// CleanupIncoming deletes temporary objects under <prefix>/incoming/ that
// are older than the TTL — the recovery path for interrupted uploads.
// Failures are reported per object; the sweep is retried by the scheduler.
func (d R2Delivery) CleanupIncoming(ctx context.Context) (int, error) {
	prefix := d.Prefix + "/incoming/"
	objects, err := d.Client.ListObjects(ctx, prefix)
	if err != nil {
		return 0, classifyUpload(err, false)
	}
	cutoff := d.now().Add(-d.ttl())
	removed := 0
	var firstErr error
	for _, obj := range objects {
		if !obj.LastModified.Before(cutoff) {
			continue
		}
		if err := d.Client.DeleteObject(ctx, obj.Key); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		removed++
	}
	return removed, firstErr
}

// classifyUpload maps objectstore errors onto the delivery errors.
func classifyUpload(err error, verifyPhase bool) error {
	if err == nil {
		return nil
	}
	if verifyPhase {
		return fmt.Errorf("%w: %s", ErrUploadVerify, err.Error())
	}
	return fmt.Errorf("%w: %s", ErrUpload, err.Error())
}
