package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/tiny-password/tiny-password/internal/audit"
	"github.com/tiny-password/tiny-password/internal/auth"
	"github.com/tiny-password/tiny-password/internal/idempotency"
	tpcrypto "github.com/tiny-password/tiny-password/internal/platform/crypto"
	"github.com/tiny-password/tiny-password/internal/platform/ident"
	tpsqlite "github.com/tiny-password/tiny-password/internal/platform/sqlite"
)

// Setup audit events live in the centralized audit package (T08).
const (
	EventSetupSuccess = audit.EventSetupSuccess
	EventSetupFailure = audit.EventSetupFailure
)

const systemStateInitialized = "initialized"

var (
	// ErrSetupTokenInvalid reports a wrong or missing setup token.
	ErrSetupTokenInvalid = errors.New("setup token invalid")
	// ErrAlreadyInitialized reports that the instance is initialized; the
	// setup entry point is permanently closed at that point.
	ErrAlreadyInitialized = errors.New("instance already initialized")
	// ErrUsernameTaken reports a username collision during setup.
	ErrUsernameTaken = errors.New("username already taken")
	// ErrInvalidUsername reports a username violating the format rules.
	ErrInvalidUsername = errors.New("username must be 3-64 characters (letters, digits, '.', '_', '-')")

	usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{2,63}$`)
)

const (
	minUsernameLen = 3
	maxUsernameLen = 64
)

// Service owns the one-time initialization state and transaction.
type Service struct {
	db        *tpsqlite.DB
	logger    *slog.Logger
	token     *SetupToken
	masterKey *tpcrypto.MasterKey
	hasher    *auth.PasswordHasher
	audit     *audit.Service
}

// NewService inspects the database state: on an uninitialized instance it
// generates the one-time setup token and logs it exactly once (D01); on an
// initialized instance no token is ever created.
func NewService(db *tpsqlite.DB, masterKey *tpcrypto.MasterKey, logger *slog.Logger, hashers ...*auth.PasswordHasher) (*Service, error) {
	initialized, err := isInitialized(db.DB)
	if err != nil {
		return nil, err
	}
	hasher := auth.DefaultPasswordHasher()
	if len(hashers) > 0 && hashers[0] != nil {
		hasher = hashers[0]
	}
	s := &Service{db: db, logger: logger, masterKey: masterKey, hasher: hasher, audit: audit.NewService(audit.Options{})}
	if !initialized {
		token, err := NewSetupToken(logger)
		if err != nil {
			return nil, err
		}
		s.token = token
	}
	return s, nil
}

func isInitialized(db *sql.DB) (bool, error) {
	var value string
	err := db.QueryRow(
		"SELECT value FROM system_state WHERE key = ?", systemStateInitialized,
	).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read system_state: %w", err)
	}
	return value == "1", nil
}

// Initialized reports whether the setup entry point is closed.
func (s *Service) Initialized() bool {
	v, err := isInitialized(s.db.DB)
	if err != nil {
		// Fail closed: treat unreadable state as initialized.
		s.logger.Error("cannot read initialization state", "error", err.Error())
		return true
	}
	return v
}

// TokenRequired reports whether a setup token exists (uninitialized instance).
func (s *Service) TokenRequired() bool { return s.token != nil }

// InitializeInput carries the setup form values. Claim, when non-nil, is a
// claimed idempotency key that is completed inside the same transaction as
// the administrator creation.
type InitializeInput struct {
	Token    string
	Username string
	Password string
	Claim    *idempotency.Claim
}

// InitializeResult reports the created administrator.
type InitializeResult struct {
	UserID   string
	Username string
}

// Initialize creates the first administrator in a single transaction that
// also flips the initialized flag; the conditional update guarantees that
// exactly one concurrent request can succeed. On success the token material
// is destroyed and the setup entry point stays closed permanently.
func (s *Service) Initialize(input InitializeInput) (*InitializeResult, error) {
	if s.Initialized() {
		return nil, ErrAlreadyInitialized
	}
	if s.masterKey == nil {
		return nil, ErrMasterKeyUnavailable
	}
	if !s.token.Matches(input.Token) {
		s.auditFailure(EventSetupFailure)
		return nil, ErrSetupTokenInvalid
	}
	if !validUsername(input.Username) {
		s.auditFailure(EventSetupFailure)
		return nil, ErrInvalidUsername
	}
	if err := auth.ValidateNewPassword(input.Password); err != nil {
		s.auditFailure(EventSetupFailure)
		return nil, err
	}

	passwordHash, err := s.hasher.Hash(input.Password)
	if err != nil {
		return nil, err
	}

	userID := ident.NewUUIDv7()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	norm := strings.ToLower(input.Username)

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // read-only rollback use

	// The conditional UPDATE below is the sole winner among concurrent
	// requests: exactly one transaction can flip value '0' → '1'.
	res, err := tx.Exec(
		"UPDATE system_state SET value = '1', updated_at = ? WHERE key = ? AND value = '0'",
		now, systemStateInitialized,
	)
	if err != nil {
		return nil, err
	}
	if affected, _ := res.RowsAffected(); affected != 1 {
		return nil, ErrAlreadyInitialized
	}

	if _, err := tx.Exec(
		`INSERT INTO users (id, username_norm, username_display, role, status, must_change_password, password_hash, created_at, updated_at)
		 VALUES (?, ?, ?, 'admin', 'active', 0, ?, ?, ?)`,
		userID, norm, input.Username, passwordHash, now, now,
	); err != nil {
		return nil, ErrUsernameTaken
	}

	if _, err := tx.Exec(
		"INSERT INTO system_state (key, value, updated_at) VALUES (?, ?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at",
		MasterKeyMarkerName(), s.masterKey.Marker(), now,
	); err != nil {
		return nil, err
	}

	if _, err := tx.Exec(
		`INSERT INTO audit_events (id, event, actor_id, target_type, target_id, result, request_id, created_at)
		 VALUES (?, ?, ?, 'user', ?, 'success', NULL, ?)`,
		ident.NewUUIDv7(), EventSetupSuccess, userID, userID, now,
	); err != nil {
		return nil, err
	}

	// The idempotency completion rides the same transaction: a crash between
	// user creation and record completion cannot leave one without the other.
	if input.Claim != nil {
		if err := input.Claim.Complete(context.Background(), tx, userID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	// Success: destroy token material; the entry point can never reopen.
	s.token.Destroy()
	s.logger.Info("administrator created; setup entry point permanently closed")
	return &InitializeResult{UserID: userID, Username: input.Username}, nil
}

// UsernameByID resolves the display username for idempotent replay rendering.
// The error is deliberately opaque: callers report a generic conflict.
func (s *Service) UsernameByID(id string) (string, error) {
	var display string
	err := s.db.QueryRow("SELECT username_display FROM users WHERE id = ?", id).Scan(&display)
	return display, err
}

func (s *Service) auditFailure(event string) {
	err := s.audit.Record(context.Background(), s.db.DB, audit.Event{
		Name:    event,
		ActorID: audit.Anonymous,
		Result:  audit.ResultFailure,
	})
	if err != nil {
		s.logger.Error("audit write failed", "error", err.Error())
	}
}

func validUsername(name string) bool {
	if len(name) < minUsernameLen || len(name) > maxUsernameLen {
		return false
	}
	return usernamePattern.MatchString(name)
}
