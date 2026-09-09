package audit

import (
	"context"
	"database/sql"
	"time"

	"github.com/tiny-password/tiny-password/internal/platform/ident"
	"github.com/tiny-password/tiny-password/internal/requestid"
)

// fixedTimestamp is fixed-width UTC: audit rows are paginated with string
// keyset comparisons, which stay lexicographically sound only for uniform
// widths (the auth service writes the same format).
const fixedTimestamp = "2006-01-02T15:04:05.000000000Z"

// TimestampLayout is the canonical UTC representation used by audit rows
// and range filters. Fixed width keeps keyset ordering lexicographically
// stable across pages.
const TimestampLayout = fixedTimestamp

// FormatTimestamp normalizes a time to the audit row representation.
func FormatTimestamp(t time.Time) string { return t.UTC().Format(fixedTimestamp) }

// Execer abstracts *sql.DB and *sql.Tx so records join the caller's
// transaction.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Event is one audit record. ActorID is an opaque user id or Anonymous.
type Event struct {
	Name        string
	ActorID     string
	TargetType  string
	TargetID    string
	Result      string
	SourceID    string
	SourceScope string
	TargetScope string
	// RequestID overrides the correlation id; empty derives it from ctx.
	RequestID string
}

// Options allows clock, id and failure injection for tests.
type Options struct {
	Now   func() time.Time
	NewID func() string
	// Fault, when set, short-circuits Record with its error; used to prove
	// that a failing audit write rolls back the surrounding business
	// transaction.
	Fault func() error
}

// Service writes audit rows.
type Service struct {
	now   func() time.Time
	newID func() string
	fault func() error
}

func NewService(options Options) *Service {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.NewID == nil {
		options.NewID = ident.NewUUIDv7
	}
	return &Service{now: options.Now, newID: options.NewID, fault: options.Fault}
}

// Record inserts one audit row using db, which should be the caller's
// transaction for change events (same-tx commit, design §7.3) or the pool for
// standalone failures. Validation fails closed: unknown event names, invalid
// results and oversized fields are errors, never silently rewritten.
func (s *Service) Record(ctx context.Context, db Execer, e Event) error {
	if s.fault != nil {
		if err := s.fault(); err != nil {
			return err
		}
	}
	if !allowlist[e.Name] {
		return ErrEventNotAllowed
	}
	if e.Result != ResultSuccess && e.Result != ResultFailure {
		return ErrInvalidResult
	}
	if len(e.Name) > MaxEventLength || len(e.TargetType) > MaxTargetTypeLength ||
		len(e.TargetID) > MaxIDLength || len(e.ActorID) > MaxIDLength || len(e.SourceID) > MaxIDLength {
		return ErrFieldTooLong
	}
	if e.SourceScope != "" && e.SourceScope != "personal" && e.SourceScope != "shared" {
		return ErrInvalidScope
	}
	if e.TargetScope != "" && e.TargetScope != "personal" && e.TargetScope != "shared" {
		return ErrInvalidScope
	}
	actor := e.ActorID
	if actor == "" {
		actor = Anonymous
	}
	reqID := e.RequestID
	if reqID == "" {
		reqID = requestid.FromContext(ctx)
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO audit_events
			(id, event, actor_id, target_type, target_id, result, request_id, created_at,
			 source_id, source_scope, target_scope)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.newID(), e.Name, actor, e.TargetType, e.TargetID, e.Result, reqID,
		s.now().UTC().Format(fixedTimestamp), nullableString(e.SourceID),
		nullableString(e.SourceScope), nullableString(e.TargetScope))
	return err
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// Entry is one read-side audit row. Empty strings are omitted when rendered.
type Entry struct {
	ID          string `json:"id"`
	Event       string `json:"event"`
	ActorID     string `json:"actor_id,omitempty"`
	TargetType  string `json:"target_type,omitempty"`
	TargetID    string `json:"target_id,omitempty"`
	Result      string `json:"result"`
	RequestID   string `json:"request_id,omitempty"`
	SourceID    string `json:"source_id,omitempty"`
	SourceScope string `json:"source_scope,omitempty"`
	TargetScope string `json:"target_scope,omitempty"`
	CreatedAt   string `json:"created_at"`
}

func scanEntry(scanner interface{ Scan(...any) error }) (Entry, error) {
	var e Entry
	var actor, targetType, targetID, requestID, sourceID, sourceScope, targetScope sql.NullString
	err := scanner.Scan(&e.ID, &e.Event, &actor, &targetType, &targetID, &e.Result, &requestID, &e.CreatedAt,
		&sourceID, &sourceScope, &targetScope)
	if err != nil {
		return Entry{}, err
	}
	e.ActorID, e.TargetType, e.TargetID, e.RequestID = actor.String, targetType.String, targetID.String, requestID.String
	e.SourceID, e.SourceScope, e.TargetScope = sourceID.String, sourceScope.String, targetScope.String
	return e, nil
}

// Page carries one keyset page.
type Page struct {
	Entries     []Entry
	LastCreated string
	LastID      string
}

// Activity returns the actor's own events, newest first. Pagination uses the
// (created_at, id) keyset: rows strictly before the cursor position.
func (s *Service) Activity(ctx context.Context, db *sql.DB, actorID, beforeCreated, beforeID string, limit int) (Page, error) {
	return s.query(ctx, db, `actor_id = ?`, []any{actorID}, beforeCreated, beforeID, limit)
}

// System returns all events (admin only), optionally filtered by exact event
// name. The filter string is server-checked against the allowlist upstream.
// System queries the redacted audit trail for admins. event and result
// (success|failure) are optional filters (T27 audit page).
func (s *Service) System(ctx context.Context, db *sql.DB, event, result, beforeCreated, beforeID string, limit int) (Page, error) {
	return s.SystemInRange(ctx, db, event, result, "", "", beforeCreated, beforeID, limit)
}

// SystemInRange returns system audit events with optional inclusive UTC
// bounds. Callers validate and normalize user-facing values before passing
// them here; the strings must use TimestampLayout.
func (s *Service) SystemInRange(ctx context.Context, db *sql.DB, event, result, from, to, beforeCreated, beforeID string, limit int) (Page, error) {
	predicate := `1 = 1`
	var args []any
	if event != "" {
		predicate += ` AND event = ?`
		args = append(args, event)
	}
	if result != "" {
		predicate += ` AND result = ?`
		args = append(args, result)
	}
	if from != "" {
		predicate += ` AND created_at >= ?`
		args = append(args, from)
	}
	if to != "" {
		predicate += ` AND created_at <= ?`
		args = append(args, to)
	}
	return s.query(ctx, db, predicate, args, beforeCreated, beforeID, limit)
}

func (s *Service) query(ctx context.Context, db *sql.DB, predicate string, predArgs []any, beforeCreated, beforeID string, limit int) (Page, error) {
	query := `SELECT id,event,actor_id,target_type,target_id,result,request_id,created_at,source_id,source_scope,target_scope FROM audit_events WHERE ` + predicate
	args := predArgs
	if beforeCreated != "" {
		query += ` AND (created_at < ? OR (created_at = ? AND id < ?))`
		args = append(args, beforeCreated, beforeCreated, beforeID)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	page := Page{Entries: []Entry{}}
	for rows.Next() {
		entry, err := scanEntry(rows)
		if err != nil {
			return Page{}, err
		}
		if len(page.Entries) == limit {
			// One extra row proves another page exists; do not return it.
			return page, nil
		}
		page.Entries = append(page.Entries, entry)
		page.LastCreated, page.LastID = entry.CreatedAt, entry.ID
	}
	page.LastCreated, page.LastID = "", ""
	return page, rows.Err()
}
