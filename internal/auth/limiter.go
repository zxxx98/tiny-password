package auth

import (
	"context"
	"time"
)

type Limits struct{ Username, Source, Global int }

func (l Limits) defaults() Limits {
	if l.Username <= 0 {
		l.Username = 5
	}
	if l.Source <= 0 {
		l.Source = 20
	}
	if l.Global <= 0 {
		l.Global = 100
	}
	return l
}

// reserveAttempt serializes admission across all dimensions and processes.
// Reservations count even when cancelled or successful; denied calls don't
// extend the rolling window. Digests bound keys and avoid retaining raw input.
func (s *Service) reserveAttempt(ctx context.Context, username, source string) error {
	now := s.now()
	cutoff := timestamp(now.Add(-time.Minute))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM login_attempts WHERE created_at<=?", cutoff); err != nil {
		return err
	}
	userKey, sourceKey := digest("username:"+username), digest("source:"+source)
	if _, err = tx.ExecContext(ctx, `INSERT INTO login_attempts(username_norm,source_hash,outcome,created_at) VALUES(?,?,'failure',?)`, userKey, sourceKey, timestamp(now)); err != nil {
		return err
	}
	var userCount, sourceCount, total int
	if err = tx.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(username_norm=?),0),coalesce(sum(source_hash=?),0) FROM login_attempts WHERE created_at>?`, userKey, sourceKey, cutoff).Scan(&total, &userCount, &sourceCount); err != nil {
		return err
	}
	if userCount > s.limits.Username || sourceCount > s.limits.Source || total > s.limits.Global {
		return ErrRateLimited
	}
	return tx.Commit()
}
