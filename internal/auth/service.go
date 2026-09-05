package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/tiny-password/tiny-password/internal/platform/ident"
)

var (
	ErrUnauthorized           = errors.New("invalid credentials or session")
	ErrAccountDisabled        = errors.New("account disabled")
	ErrPasswordChangeRequired = errors.New("password change required")
	ErrRateLimited            = errors.New("too many authentication attempts")
	ErrNotFound               = errors.New("session not found")
	ErrInvalidIdleTimeout     = errors.New("idle timeout must be between 5 and 30 minutes")
)

const AbsoluteLifetime = 24 * time.Hour
const timestampFormat = "2006-01-02T15:04:05.000000000Z"

func timestamp(t time.Time) string { return t.UTC().Format(timestampFormat) }
func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

type Options struct {
	Now    func() time.Time
	Limits Limits
}
type Service struct {
	db        *sql.DB
	now       func() time.Time
	limits    Limits
	dummyHash string
	verify    func(string, string) (bool, error)
}

func NewService(db *sql.DB, options Options) (*Service, error) {
	if options.Now == nil {
		options.Now = time.Now
	}
	// A real Argon2 hash makes unknown accounts perform the same password work.
	dummy, err := HashPassword("unusable dummy password " + ident.NewUUIDv7())
	if err != nil {
		return nil, err
	}
	return &Service{db: db, now: options.Now, limits: options.Limits.defaults(), dummyHash: dummy, verify: VerifyPassword}, nil
}

type Principal struct {
	UserID             string      `json:"user_id"`
	Username           string      `json:"username"`
	Role               string      `json:"role"`
	MustChangePassword bool        `json:"must_change_password"`
	IdleTimeoutMinutes int         `json:"idle_timeout_minutes"`
	Session            SessionInfo `json:"session"`
}
type LoginResult struct {
	Token     string
	Principal Principal
}

func newSession() (string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), ident.NewUUIDv7(), nil
}

// Login verifies outside the write transaction, then compares the verified hash
// again while inserting. A concurrent password change cannot mint an old session.
func (s *Service) Login(ctx context.Context, username, password, source string) (*LoginResult, error) {
	username = strings.ToLower(username)
	if err := s.reserveAttempt(ctx, username, source); err != nil {
		return nil, err
	}
	if len(username) > 64 || len(password) > MaxPasswordBytes {
		return nil, ErrUnauthorized
	}
	var id, display, role, status, hash string
	var force bool
	var idle int
	err := s.db.QueryRowContext(ctx, `SELECT id,username_display,role,status,password_hash,must_change_password,idle_timeout_minutes FROM users WHERE username_norm=?`, username).Scan(&id, &display, &role, &status, &hash, &force, &idle)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		hash = s.dummyHash
	}
	valid, verifyErr := s.verify(password, hash)
	if verifyErr != nil || !valid || id == "" {
		return nil, ErrUnauthorized
	}
	if status != "active" {
		return nil, ErrAccountDisabled
	}
	token, publicID, err := newSession()
	if err != nil {
		return nil, err
	}
	now := s.now()
	absolute := now.Add(AbsoluteLifetime)
	idleExpiry := now.Add(time.Duration(idle) * time.Minute)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// The insert takes the writer lock before reading current user settings.
	// Its provisional expiry is corrected before any other connection sees it.
	result, err := tx.ExecContext(ctx, `INSERT INTO sessions(id,public_id,user_id,created_at,absolute_expires_at,idle_expires_at)
 SELECT ?,?,id,?,?,? FROM users WHERE id=? AND password_hash=? AND status='active'`, digest(token), publicID, timestamp(now), timestamp(absolute), timestamp(idleExpiry), id, hash)
	if err != nil {
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected != 1 {
		return nil, ErrUnauthorized
	}
	if err := tx.QueryRowContext(ctx, "SELECT username_display,role,must_change_password,idle_timeout_minutes FROM users WHERE id=?", id).Scan(&display, &role, &force, &idle); err != nil {
		return nil, err
	}
	idleExpiry = now.Add(time.Duration(idle) * time.Minute)
	if _, err := tx.ExecContext(ctx, "UPDATE sessions SET idle_expires_at=? WHERE id=?", timestamp(idleExpiry), digest(token)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &LoginResult{Token: token, Principal: Principal{UserID: id, Username: display, Role: role, MustChangePassword: force, IdleTimeoutMinutes: idle,
		Session: SessionInfo{ID: publicID, CreatedAt: timestamp(now), ExpiresAt: timestamp(absolute), IdleExpiresAt: timestamp(idleExpiry), Current: true}}}, nil
}

// ChangePassword revokes every old credential and issues one fresh session in
// the same transaction. It rechecks session validity after expensive hashing.
func (s *Service) ChangePassword(ctx context.Context, token, current, next, source string) (*LoginResult, error) {
	p, err := s.Authenticate(ctx, token)
	if err != nil {
		return nil, err
	}
	if err := s.reserveAttempt(ctx, "password:"+p.UserID, source); err != nil {
		return nil, err
	}
	if len(current) > MaxPasswordBytes {
		return nil, ErrUnauthorized
	}
	if err := ValidateNewPassword(next); err != nil {
		return nil, err
	}
	var oldHash string
	if err := s.db.QueryRowContext(ctx, "SELECT password_hash FROM users WHERE id=?", p.UserID).Scan(&oldHash); err != nil {
		return nil, err
	}
	ok, err := s.verify(current, oldHash)
	if err != nil || !ok {
		return nil, ErrUnauthorized
	}
	if current == next {
		return nil, ErrPasswordPolicy
	}
	newHash, err := HashPassword(next)
	if err != nil {
		return nil, err
	}
	raw, id, err := newSession()
	if err != nil {
		return nil, err
	}
	now := s.now()
	at := timestamp(now)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// First statement acquires the SQLite writer lock; no stale read transaction.
	res, err := tx.ExecContext(ctx, `UPDATE users SET password_hash=?,must_change_password=0,updated_at=?
 WHERE id=? AND password_hash=? AND status='active' AND EXISTS(
 SELECT 1 FROM sessions WHERE id=? AND user_id=users.id AND revoked_at IS NULL AND absolute_expires_at>? AND idle_expires_at>?)`, newHash, at, p.UserID, oldHash, digest(token), at, at)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, ErrUnauthorized
	}
	if _, err = tx.ExecContext(ctx, "UPDATE sessions SET revoked_at=? WHERE user_id=? AND revoked_at IS NULL", at, p.UserID); err != nil {
		return nil, err
	}
	var idle int
	if err = tx.QueryRowContext(ctx, "SELECT idle_timeout_minutes FROM users WHERE id=?", p.UserID).Scan(&idle); err != nil {
		return nil, err
	}
	absolute, idleExpiry := timestamp(now.Add(AbsoluteLifetime)), timestamp(now.Add(time.Duration(idle)*time.Minute))
	if _, err = tx.ExecContext(ctx, `INSERT INTO sessions(id,public_id,user_id,created_at,absolute_expires_at,idle_expires_at) VALUES(?,?,?,?,?,?)`, digest(raw), id, p.UserID, at, absolute, idleExpiry); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	p.MustChangePassword = false
	p.IdleTimeoutMinutes = idle
	p.Session = SessionInfo{ID: id, CreatedAt: at, ExpiresAt: absolute, IdleExpiresAt: idleExpiry, Current: true}
	return &LoginResult{Token: raw, Principal: *p}, nil
}
