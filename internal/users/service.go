// Package users implements the member lifecycle (design §5.3): admin-only
// create, disable, enable, session revocation and irreversible delete, with
// last-admin protection enforced inside the write transaction and deletion
// cascades handled in a single transaction.
package users

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/tiny-password/tiny-password/internal/audit"
	"github.com/tiny-password/tiny-password/internal/auth"
	"github.com/tiny-password/tiny-password/internal/idempotency"
	"github.com/tiny-password/tiny-password/internal/platform/ident"
)

var (
	// ErrForbidden reports a caller lacking the administrator role.
	ErrForbidden = errors.New("administrator role required")
	// ErrNotFound reports an unknown target user.
	ErrNotFound = errors.New("user not found")
	// ErrUsernameTaken reports a normalized-username collision.
	ErrUsernameTaken = errors.New("username already taken")
	// ErrLastAdminProtected reports a disable/delete that would remove the
	// last active administrator.
	ErrLastAdminProtected = errors.New("the last administrator cannot be disabled or deleted")
	// ErrConfirmationMismatch reports a delete confirmation that does not
	// exactly repeat the target's display username.
	ErrConfirmationMismatch = errors.New("delete confirmation mismatch")
	// ErrInvalidUsername reports a username violating the format rules.
	ErrInvalidUsername = errors.New("username must be 3-64 characters (letters, digits, '.', '_', '-')")
)

const (
	roleAdmin  = "admin"
	roleMember = "member"
	// StatusActive/StatusDisabled mirror the storage column; the derived
	// StatusMustChangePassword reports a member whose first login change is
	// still pending.
	StatusActive             = "active"
	StatusDisabled           = "disabled"
	StatusMustChangePassword = "must_change_password"
)

const (
	minUsernameLen = 3
	maxUsernameLen = 64
)

var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{2,63}$`)

// ValidateUsername enforces the shared username rule (same as setup).
func ValidateUsername(name string) error {
	if len(name) < minUsernameLen || len(name) > maxUsernameLen || !usernamePattern.MatchString(name) {
		return ErrInvalidUsername
	}
	return nil
}

// NormalizeUsername maps the display form to the uniqueness key.
func NormalizeUsername(name string) string { return strings.ToLower(name) }

// TimestampFormat is the fixed-width UTC format used for keyset pagination.
const TimestampFormat = "2006-01-02T15:04:05.000000000Z"

type Options struct {
	Now    func() time.Time
	Audit  *audit.Service
	Hasher *auth.PasswordHasher
}

type Service struct {
	db     *sql.DB
	now    func() time.Time
	hasher *auth.PasswordHasher
	audit  *audit.Service
}

func NewService(db *sql.DB, options Options) *Service {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Audit == nil {
		options.Audit = audit.NewService(audit.Options{})
	}
	if options.Hasher == nil {
		options.Hasher = auth.DefaultPasswordHasher()
	}
	return &Service{db: db, now: options.Now, hasher: options.Hasher, audit: options.Audit}
}

// User is the externally visible member representation. Status is derived:
// the storage column is active/disabled and must_change_password is a flag.
type User struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

func (u *User) setStatus(firstLogin bool, column string) {
	switch {
	case column == StatusDisabled:
		u.Status = StatusDisabled
	case firstLogin:
		u.Status = StatusMustChangePassword
	default:
		u.Status = StatusActive
	}
}

func scanUser(scanner interface{ Scan(...any) error }) (User, error) {
	var u User
	var status string
	var mustChange int
	if err := scanner.Scan(&u.ID, &u.Username, &u.Role, &status, &mustChange, &u.CreatedAt); err != nil {
		return User{}, err
	}
	u.setStatus(mustChange == 1, status)
	return u, nil
}

const userColumns = `id, username_display, role, status, must_change_password, created_at`

func requireAdmin(p *auth.Principal) error {
	if p == nil || p.Role != roleAdmin {
		return ErrForbidden
	}
	return nil
}

// authorizeWrite acquires the SQLite writer lock before checking live authority.
// The public session ID comes from Authenticate, never from a request DTO.
// Revocation and member mutations therefore serialize on the same writer lock.
func (s *Service) authorizeWrite(ctx context.Context, tx *sql.Tx, p *auth.Principal) error {
	at := s.now().UTC().Format(TimestampFormat)
	res, err := tx.ExecContext(ctx, `UPDATE users SET id=id
 WHERE id=? AND role='admin' AND status='active' AND must_change_password=0
 AND EXISTS (SELECT 1 FROM sessions WHERE public_id=? AND user_id=users.id
 AND revoked_at IS NULL AND absolute_expires_at>? AND idle_expires_at>?)`,
		p.UserID, p.Session.ID, at, at)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return auth.ErrUnauthorized
	}
	return nil
}

// CreateInput carries the member creation payload.
type CreateInput struct {
	Username        string
	InitialPassword string
}

// Create adds a member account that must change its password at first login.
// The claim, when present, is completed inside the same transaction as the
// insert so a replayed request can render the original resource.
func (s *Service) Create(ctx context.Context, admin *auth.Principal, input CreateInput, claim *idempotency.Claim) (User, error) {
	if err := requireAdmin(admin); err != nil {
		return User{}, err
	}
	if err := ValidateUsername(input.Username); err != nil {
		return User{}, err
	}
	if err := auth.ValidateNewPassword(input.InitialPassword); err != nil {
		return User{}, err
	}
	hash, err := s.hasher.Hash(input.InitialPassword)
	if err != nil {
		return User{}, err
	}

	id := ident.NewUUIDv7()
	at := s.now().UTC().Format(TimestampFormat)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	if err := s.authorizeWrite(ctx, tx, admin); err != nil {
		return User{}, err
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO users (id, username_norm, username_display, role, status, must_change_password, password_hash, created_at, updated_at)
		 VALUES (?, ?, ?, 'member', 'active', 1, ?, ?, ?)`,
		id, NormalizeUsername(input.Username), input.Username, hash, at, at); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") || strings.Contains(err.Error(), "constraint failed: users.username_norm") {
			return User{}, ErrUsernameTaken
		}
		return User{}, err
	}
	if claim != nil {
		if err := claim.Complete(ctx, tx, id); err != nil {
			return User{}, err
		}
	}
	if err := s.audit.Record(ctx, tx, audit.Event{
		Name: audit.EventUserCreated, ActorID: admin.UserID,
		TargetType: audit.TargetUser, TargetID: id, Result: audit.ResultSuccess,
	}); err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return User{ID: id, Username: input.Username, Role: roleMember, Status: StatusMustChangePassword, CreatedAt: at}, nil
}

// ReplayCreated revalidates the actor and renders a completed creation under
// one transaction, so a body delayed past revocation cannot replay data.
func (s *Service) ReplayCreated(ctx context.Context, admin *auth.Principal, id string) (User, error) {
	if err := requireAdmin(admin); err != nil {
		return User{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	if err := s.authorizeWrite(ctx, tx, admin); err != nil {
		return User{}, err
	}
	u, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

// GetByID renders one member for already-authorized member operations.
// HTTP idempotency replays must use ReplayCreated to recheck live authority.
func (s *Service) GetByID(ctx context.Context, admin *auth.Principal, id string) (User, error) {
	if err := requireAdmin(admin); err != nil {
		return User{}, err
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

// List pages members newest-first via the (created_at, id) keyset.
func (s *Service) List(ctx context.Context, admin *auth.Principal, beforeCreated, beforeID string, limit int) ([]User, error) {
	if err := requireAdmin(admin); err != nil {
		return nil, err
	}
	query := `SELECT ` + userColumns + ` FROM users`
	args := []any{}
	if beforeCreated != "" {
		query += ` WHERE (created_at < ? OR (created_at = ? AND id < ?))`
		args = append(args, beforeCreated, beforeCreated, beforeID)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, u)
	}
	return items, rows.Err()
}

// Disable revokes every session of the target in the same transaction that
// flips the status. Re-enabling never resurrects the revoked sessions.
func (s *Service) Disable(ctx context.Context, admin *auth.Principal, targetID string) (User, error) {
	if err := requireAdmin(admin); err != nil {
		return User{}, err
	}
	at := s.now().UTC().Format(TimestampFormat)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	if err := s.authorizeWrite(ctx, tx, admin); err != nil {
		return User{}, err
	}

	// The conditional update is the sole race-safe guard: the last-admin
	// subquery is evaluated while this transaction holds the writer lock.
	res, err := tx.ExecContext(ctx,
		`UPDATE users SET status='disabled', updated_at=?
		 WHERE id=? AND status='active'
		   AND (role != 'admin' OR
		        (SELECT COUNT(*) FROM users other WHERE other.role='admin' AND other.status='active' AND other.id != users.id) >= 1)`,
		at, targetID)
	if err != nil {
		return User{}, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return User{}, err
	}
	if affected != 1 {
		if err := s.disableBlockReason(ctx, tx, targetID); err != nil {
			return User{}, err
		}
		return scanUser(tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id=?`, targetID))
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE sessions SET revoked_at=? WHERE user_id=? AND revoked_at IS NULL`, at, targetID); err != nil {
		return User{}, err
	}
	if err := s.audit.Record(ctx, tx, audit.Event{
		Name: audit.EventUserDisabled, ActorID: admin.UserID,
		TargetType: audit.TargetUser, TargetID: targetID, Result: audit.ResultSuccess,
	}); err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return s.GetByID(ctx, admin, targetID)
}

// disableBlockReason distinguishes unknown target, already-disabled and
// last-admin cases after the conditional update matched nothing.
func (s *Service) disableBlockReason(ctx context.Context, tx *sql.Tx, targetID string) error {
	var role, status string
	err := tx.QueryRowContext(ctx, `SELECT role, status FROM users WHERE id=?`, targetID).Scan(&role, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if status == StatusDisabled {
		return nil // already disabled: idempotent no-op
	}
	var otherAdmins int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE role='admin' AND status='active' AND id != ?`, targetID).Scan(&otherAdmins); err != nil {
		return err
	}
	if role == roleAdmin && otherAdmins == 0 {
		return ErrLastAdminProtected
	}
	// Active non-admin matching nothing is impossible; treat as not found.
	return ErrNotFound
}

// Enable reactivates a disabled member; previously revoked sessions stay dead.
func (s *Service) Enable(ctx context.Context, admin *auth.Principal, targetID string) (User, error) {
	if err := requireAdmin(admin); err != nil {
		return User{}, err
	}
	at := s.now().UTC().Format(TimestampFormat)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	if err := s.authorizeWrite(ctx, tx, admin); err != nil {
		return User{}, err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE users SET status='active', updated_at=? WHERE id=? AND status='disabled'`, at, targetID)
	if err != nil {
		return User{}, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return User{}, err
	}
	if affected != 1 {
		var status string
		err := tx.QueryRowContext(ctx, `SELECT status FROM users WHERE id=?`, targetID).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, ErrNotFound
		}
		if err != nil {
			return User{}, err
		}
		if status == StatusActive {
			return scanUser(tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id=?`, targetID)) // idempotent no-op
		}
		return User{}, ErrNotFound
	}
	if err := s.audit.Record(ctx, tx, audit.Event{
		Name: audit.EventUserEnabled, ActorID: admin.UserID,
		TargetType: audit.TargetUser, TargetID: targetID, Result: audit.ResultSuccess,
	}); err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return s.GetByID(ctx, admin, targetID)
}

// RevokeSessions kills every live session of the target without changing the
// account status (admin-initiated revocation, distinct from disable).
func (s *Service) RevokeSessions(ctx context.Context, admin *auth.Principal, targetID string) (int, error) {
	if err := requireAdmin(admin); err != nil {
		return 0, err
	}
	if _, err := s.GetByID(ctx, admin, targetID); err != nil {
		return 0, err
	}
	at := s.now().UTC().Format(TimestampFormat)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := s.authorizeWrite(ctx, tx, admin); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE sessions SET revoked_at=? WHERE user_id=? AND revoked_at IS NULL`, at, targetID)
	if err != nil {
		return 0, err
	}
	revoked, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := s.audit.Record(ctx, tx, audit.Event{
		Name: audit.EventUserSessionsRevoked, ActorID: admin.UserID,
		TargetType: audit.TargetUser, TargetID: targetID, Result: audit.ResultSuccess,
	}); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(revoked), nil
}

// DeleteInput carries the delete confirmation.
type DeleteInput struct {
	ConfirmUsername string
}

// Delete permanently removes the member and every vault artifact they own or
// created, inside one transaction. The confirmation must repeat the target's
// display username exactly. Audit rows survive by design (no foreign key).
func (s *Service) Delete(ctx context.Context, admin *auth.Principal, targetID string, input DeleteInput) error {
	if err := requireAdmin(admin); err != nil {
		return err
	}
	if targetID == admin.UserID {
		// Deleting yourself is forbidden even with a valid confirmation (D05).
		return ErrForbidden
	}
	var display, role string
	err := s.db.QueryRowContext(ctx, `SELECT username_display, role FROM users WHERE id=?`, targetID).Scan(&display, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if input.ConfirmUsername != display {
		return ErrConfirmationMismatch
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.authorizeWrite(ctx, tx, admin); err != nil {
		return err
	}

	// The conditional delete re-checks last-admin protection under the
	// writer lock; sessions cascade via the foreign key.
	res, err := tx.ExecContext(ctx,
		`DELETE FROM users WHERE id=?
		   AND (role != 'admin' OR
		        (SELECT COUNT(*) FROM users other WHERE other.role='admin' AND other.status='active' AND other.id != users.id) >= 1)`,
		targetID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrLastAdminProtected
	}

	// Personal items and shared items created by the member, with their
	// encrypted payloads and history versions (item_versions cascades).
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM vault_items WHERE owner_user_id=? OR created_by_user_id=?`, targetID, targetID); err != nil {
		return err
	}
	// Idempotency claims are actor-bound by scope convention "<op>:<actor>".
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM idempotency_keys WHERE scope LIKE '%:' || ?`, targetID); err != nil {
		return err
	}
	if err := s.audit.Record(ctx, tx, audit.Event{
		Name: audit.EventUserDeleted, ActorID: admin.UserID,
		TargetType: audit.TargetUser, TargetID: targetID, Result: audit.ResultSuccess,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// FormatTime renders a timestamp in the users package's fixed-width format.
func FormatTime(t time.Time) string { return t.UTC().Format(TimestampFormat) }
