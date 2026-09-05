package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"

	"github.com/tiny-password/tiny-password/internal/audit"
)

type SessionInfo struct {
	ID            string `json:"id"`
	CreatedAt     string `json:"created_at"`
	ExpiresAt     string `json:"expires_at"`
	IdleExpiresAt string `json:"idle_expires_at"`
	Current       bool   `json:"current"`
}

// CSRFToken is a domain-separated, one-way derivation of the unpredictable
// session credential. Only same-origin responses expose it; it cannot recover
// the HttpOnly credential, and changes whenever the session rotates.
func CSRFToken(token string) string {
	sum := sha256.Sum256([]byte("tiny-password/session-csrf/v1:" + token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
func VerifyCSRF(token, presented string) bool {
	return token != "" && subtle.ConstantTimeCompare([]byte(CSRFToken(token)), []byte(presented)) == 1
}

// Authenticate is read-only: polling must never extend idle or absolute expiry.
func (s *Service) Authenticate(ctx context.Context, token string) (*Principal, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 {
		return nil, ErrUnauthorized
	}
	at := timestamp(s.now())
	p := &Principal{}
	err = s.db.QueryRowContext(ctx, `SELECT u.id,u.username_display,u.role,u.must_change_password,u.idle_timeout_minutes,
 s.public_id,s.created_at,s.absolute_expires_at,s.idle_expires_at
 FROM sessions s JOIN users u ON u.id=s.user_id
 WHERE s.id=? AND s.revoked_at IS NULL AND s.absolute_expires_at>? AND s.idle_expires_at>? AND u.status='active'`, digest(token), at, at).
		Scan(&p.UserID, &p.Username, &p.Role, &p.MustChangePassword, &p.IdleTimeoutMinutes, &p.Session.ID, &p.Session.CreatedAt, &p.Session.ExpiresAt, &p.Session.IdleExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnauthorized
	}
	if err != nil {
		return nil, err
	}
	p.Session.Current = true
	return p, nil
}

// Touch updates only a still-valid session, never an expired/revoked one.
func (s *Service) Touch(ctx context.Context, token string) error {
	at := timestamp(s.now())
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET idle_expires_at=min(absolute_expires_at,
 strftime('%Y-%m-%dT%H:%M:%f', ?, '+' || (SELECT idle_timeout_minutes FROM users WHERE id=sessions.user_id) || ' minutes') || '000000Z')
 WHERE id=? AND revoked_at IS NULL AND absolute_expires_at>? AND idle_expires_at>?
 AND EXISTS(SELECT 1 FROM users WHERE id=sessions.user_id AND status='active' AND must_change_password=0)`, at, digest(token), at, at)
	return requireAffected(res, err, ErrUnauthorized)
}

func (s *Service) ListSessions(ctx context.Context, token string) ([]SessionInfo, error) {
	p, err := s.Authenticate(ctx, token)
	if err != nil {
		return nil, err
	}
	if p.MustChangePassword {
		return nil, ErrPasswordChangeRequired
	}
	at := timestamp(s.now())
	rows, err := s.db.QueryContext(ctx, `SELECT public_id,created_at,absolute_expires_at,idle_expires_at FROM sessions WHERE user_id=? AND revoked_at IS NULL AND absolute_expires_at>? AND idle_expires_at>? ORDER BY created_at DESC,public_id`, p.UserID, at, at)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []SessionInfo{}
	for rows.Next() {
		var item SessionInfo
		if err := rows.Scan(&item.ID, &item.CreatedAt, &item.ExpiresAt, &item.IdleExpiresAt); err != nil {
			return nil, err
		}
		item.Current = item.ID == p.Session.ID
		items = append(items, item)
	}
	return items, rows.Err()
}

// Logout revokes the caller's live session; the revocation and its audit row
// commit together.
func (s *Service) Logout(ctx context.Context, token string) error {
	if !validTokenShape(token) {
		return ErrUnauthorized
	}
	at := timestamp(s.now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var userID, publicID string
	err = tx.QueryRowContext(ctx, `SELECT user_id,public_id FROM sessions WHERE id=? AND revoked_at IS NULL AND absolute_expires_at>? AND idle_expires_at>?
 AND EXISTS(SELECT 1 FROM users WHERE id=sessions.user_id AND status='active')`, digest(token), at, at).Scan(&userID, &publicID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUnauthorized
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE sessions SET revoked_at=? WHERE id=? AND revoked_at IS NULL", at, digest(token)); err != nil {
		return err
	}
	if err := s.auditRecord(ctx, tx, audit.Event{Name: audit.EventLogout, ActorID: userID, TargetType: audit.TargetSession, TargetID: publicID, Result: audit.ResultSuccess}); err != nil {
		return err
	}
	return tx.Commit()
}

// RevokeSession lets a user revoke one of their own live sessions. The
// caller's session must itself be live; the audit row shares the transaction.
func (s *Service) RevokeSession(ctx context.Context, token, id string) error {
	if !validTokenShape(token) {
		return ErrNotFound
	}
	at := timestamp(s.now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var callerID string
	err = tx.QueryRowContext(ctx, `SELECT user_id FROM sessions WHERE id=? AND revoked_at IS NULL AND absolute_expires_at>? AND idle_expires_at>?
 AND EXISTS(SELECT 1 FROM users u WHERE u.id=sessions.user_id AND u.status='active' AND u.must_change_password=0)`, digest(token), at, at).Scan(&callerID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUnauthorized
	}
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at=? WHERE public_id=? AND revoked_at IS NULL AND user_id=?`, at, id, callerID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrNotFound
	}
	if err := s.auditRecord(ctx, tx, audit.Event{Name: audit.EventSessionRevoked, ActorID: callerID, TargetType: audit.TargetSession, TargetID: id, Result: audit.ResultSuccess}); err != nil {
		return err
	}
	return tx.Commit()
}

// validTokenShape rejects malformed credentials before touching the database.
func validTokenShape(token string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(raw) == 32
}
func (s *Service) SetIdleTimeout(ctx context.Context, token string, minutes int) error {
	if minutes < 5 || minutes > 30 {
		return ErrInvalidIdleTimeout
	}
	at := timestamp(s.now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE users SET idle_timeout_minutes=?,updated_at=? WHERE status='active' AND must_change_password=0 AND id IN
 (SELECT user_id FROM sessions WHERE id=? AND revoked_at IS NULL AND absolute_expires_at>? AND idle_expires_at>?)`, minutes, at, digest(token), at, at)
	if err = requireAffected(res, err, ErrUnauthorized); err != nil {
		return err
	}
	// Reducing the preference shortens live sessions; increasing it doesn't
	// silently renew other devices or resurrect already-expired credentials.
	if _, err = tx.ExecContext(ctx, `UPDATE sessions SET idle_expires_at=min(idle_expires_at,absolute_expires_at,
 strftime('%Y-%m-%dT%H:%M:%f', ?, '+' || ? || ' minutes') || '000000Z')
 WHERE user_id=(SELECT user_id FROM sessions WHERE id=?) AND revoked_at IS NULL AND absolute_expires_at>? AND idle_expires_at>?`, at, minutes, digest(token), at, at); err != nil {
		return err
	}
	return tx.Commit()
}
func requireAffected(res sql.Result, err, errorIfMissing error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errorIfMissing
	}
	return nil
}
