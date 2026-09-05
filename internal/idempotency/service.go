// Package idempotency implements transactional create-idempotency: a scope +
// key claim is recorded before the business change, completed inside the same
// transaction as the change, and replayed by rendering the stored opaque
// resource id. Only HMAC fingerprints and opaque resource ids are persisted —
// never request payloads or response bodies.
package idempotency

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"time"
)

// Outcome reports what a Claim call decided.
type Outcome int

const (
	// OutcomeFresh means the caller won the claim and must execute the
	// operation, completing the claim inside its transaction.
	OutcomeFresh Outcome = iota
	// OutcomeReplay means a completed record exists for the same scope, key
	// and fingerprint; the caller must re-authorize and render the stored
	// resource id instead of executing again.
	OutcomeReplay
	// OutcomeInFlight means an identical request is currently executing.
	OutcomeInFlight
	// OutcomeConflict means the same key was used with a different
	// fingerprint (different content or a different actor).
	OutcomeConflict
)

// Execer abstracts *sql.DB and *sql.Tx so claims complete inside the caller's
// transaction.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

var errStaleClaim = errors.New("stale idempotency claim")

// fixedTimestamp is fixed-width UTC: this table is compared with string SQL
// predicates, which stay lexicographically sound only for uniform widths.
const fixedTimestamp = "2006-01-02T15:04:05.000000000Z"

func formatTime(t time.Time) string { return t.UTC().Format(fixedTimestamp) }

// Service owns the idempotency_keys table.
type Service struct {
	db           *sql.DB
	now          func() time.Time
	macKey       []byte
	newResource  func() string
	pendingTTL   time.Duration
	completedTTL time.Duration
}

// Options configures the service; zero values select defaults.
type Options struct {
	// Now is injectable for expiry tests.
	Now func() time.Time
	// MACKey is the server-side secret for request fingerprints. It must be
	// unpredictable; with a per-process random key fingerprints cannot be
	// forged across restarts.
	MACKey []byte
	// PendingTTL bounds how long a claimed-but-unfinished request blocks the
	// key (default 10m).
	PendingTTL time.Duration
	// CompletedTTL bounds how long a successful result can be replayed
	// (default 24h).
	CompletedTTL time.Duration
	// NewResource generates opaque resource ids when a caller has none.
	NewResource func() string
}

func (o Options) pendingTTL() time.Duration {
	if o.PendingTTL > 0 {
		return o.PendingTTL
	}
	return 10 * time.Minute
}

func (o Options) completedTTL() time.Duration {
	if o.CompletedTTL > 0 {
		return o.CompletedTTL
	}
	return 24 * time.Hour
}

// NewService validates that a MAC key is present: persisting forgeable
// fingerprints would let a client pin arbitrary rows.
func NewService(db *sql.DB, options Options) (*Service, error) {
	if len(options.MACKey) < 32 {
		return nil, errors.New("idempotency: MAC key must be at least 32 bytes")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.NewResource == nil {
		options.NewResource = newRandomID
	}
	return &Service{
		db:           db,
		now:          options.Now,
		macKey:       options.MACKey,
		newResource:  options.NewResource,
		pendingTTL:   options.pendingTTL(),
		completedTTL: options.completedTTL(),
	}, nil
}

func newRandomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("idempotency: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// Fingerprint derives the keyed HMAC of a canonical field list. Fields are
// length-prefixed so concatenation is unambiguous. The stored digest cannot
// recover the request content (which includes credentials).
func (s *Service) Fingerprint(fields ...string) string {
	mac := hmac.New(sha256.New, s.macKey)
	var lenBuf [8]byte
	for _, f := range fields {
		binary.BigEndian.PutUint64(lenBuf[:], uint64(len(f)))
		mac.Write(lenBuf[:])
		mac.Write([]byte(f))
	}
	return hex.EncodeToString(mac.Sum(nil))
}

// Claim records the intent to execute scope+key. Expired claims (pending or
// completed) are reclaimed so stale rows cannot block honest retries forever.
func (s *Service) Claim(ctx context.Context, scope, key, fingerprint string) (*Claim, Outcome, error) {
	for attempt := 0; attempt < 2; attempt++ {
		now := s.now()
		expires := now.Add(s.pendingTTL)
		res, err := s.db.ExecContext(ctx,
			`INSERT INTO idempotency_keys(scope,key,fingerprint,status,created_at,expires_at)
			 VALUES (?, ?, ?, 'pending', ?, ?)
			 ON CONFLICT(scope,key) DO NOTHING`,
			scope, key, fingerprint, formatTime(now), formatTime(expires))
		if err != nil {
			return nil, OutcomeFresh, err
		}
		if affected, err := res.RowsAffected(); err == nil && affected == 1 {
			return &Claim{svc: s, scope: scope, key: key, fingerprint: fingerprint}, OutcomeFresh, nil
		}
		decision, err := s.inspect(ctx, scope, key, fingerprint)
		if errors.Is(err, errStaleClaim) {
			continue // expired row deleted; retry the insert once
		}
		if err != nil {
			return nil, OutcomeFresh, err
		}
		return nil, decision, nil
	}
	// Both attempts raced with another reclaimer; report in-flight.
	return nil, OutcomeInFlight, nil
}

func (s *Service) inspect(ctx context.Context, scope, key, fingerprint string) (Outcome, error) {
	var (
		storedFp   string
		status     string
		resourceID sql.NullString
		expiresAt  string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT fingerprint,status,resource_id,expires_at FROM idempotency_keys WHERE scope=? AND key=?`,
		scope, key).Scan(&storedFp, &status, &resourceID, &expiresAt)
	if err != nil {
		return OutcomeFresh, err
	}
	expires, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil || !s.now().Before(expires) {
		// Expired: reclaim regardless of status so the retry can proceed.
		_, err := s.db.ExecContext(ctx,
			`DELETE FROM idempotency_keys WHERE scope=? AND key=?`, scope, key)
		if err != nil {
			return OutcomeFresh, err
		}
		return OutcomeFresh, errStaleClaim
	}
	if storedFp != fingerprint {
		return OutcomeConflict, nil
	}
	if status == "completed" {
		return OutcomeReplay, nil
	}
	return OutcomeInFlight, nil
}

// Claim is a live key reservation.
type Claim struct {
	svc         *Service
	scope       string
	key         string
	fingerprint string
	ResourceID  string
}

// Scope of the claim (operation + actor binding).
func (c *Claim) Scope() string { return c.scope }

// Complete marks the claim completed inside the business transaction and pins
// the replay window to the completed TTL. The claim must still be live
// (pending and unexpired): a vanished or expired claim fails the completion,
// which rolls back the surrounding transaction, so no business change ever
// outlives its idempotency record.
func (c *Claim) Complete(ctx context.Context, db Execer, resourceID string) error {
	now := c.svc.now()
	res, err := db.ExecContext(ctx,
		`UPDATE idempotency_keys SET status='completed', resource_id=?, expires_at=?
		 WHERE scope=? AND key=? AND status='pending' AND expires_at>?`,
		resourceID, formatTime(now.Add(c.svc.completedTTL)), c.scope, c.key,
		formatTime(now))
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return errStaleClaim
	}
	c.ResourceID = resourceID
	return nil
}

// Release drops a pending claim after a failed attempt so the key stays
// usable for a corrected retry. Best-effort: a leftover pending row expires.
func (c *Claim) Release(ctx context.Context) {
	if c == nil {
		return
	}
	_, _ = c.svc.db.ExecContext(ctx,
		`DELETE FROM idempotency_keys WHERE scope=? AND key=? AND status='pending'`, c.scope, c.key)
}

// ResourceID of a replayed claim (valid after OutcomeReplay lookup).
func (s *Service) ReplayResourceID(ctx context.Context, scope, key, fingerprint string) (string, error) {
	var (
		status     string
		storedFp   string
		resourceID sql.NullString
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT status,fingerprint,resource_id FROM idempotency_keys WHERE scope=? AND key=?`,
		scope, key).Scan(&status, &storedFp, &resourceID)
	if err != nil {
		return "", err
	}
	if storedFp != fingerprint || status != "completed" || !resourceID.Valid {
		return "", errStaleClaim
	}
	return resourceID.String, nil
}

// CleanupExpired deletes expired claims; wired to the maintenance scheduler.
func (s *Service) CleanupExpired(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM idempotency_keys WHERE expires_at <= ?`, formatTime(s.now()))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
