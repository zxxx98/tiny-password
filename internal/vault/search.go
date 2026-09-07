package vault

import (
	"context"
	"strings"
	"time"

	"github.com/tiny-password/tiny-password/internal/auth"
)

// searchBatchSize bounds how many candidates are decrypted per SQL batch
// while scanning. Five thousand keeps the 10,000-item search benchmark to
// two SQL passes while retaining a bounded working set for larger vaults.
const searchBatchSize = 5000

// SearchPhase identifies one measurable part of a search request. The hook is
// optional and intended for benchmark instrumentation; production callers can
// leave it nil so the hot path only pays for the nil check.
type SearchPhase string

const (
	SearchPhaseSQL     SearchPhase = "sql"
	SearchPhaseDecrypt SearchPhase = "decrypt"
	SearchPhaseJSON    SearchPhase = "json"
	SearchPhaseMatch   SearchPhase = "match"
)

// SearchProfileHook receives wall-clock duration samples for one search
// phase. It must not retain plaintext values; only phase and duration cross
// this boundary.
type SearchProfileHook func(SearchPhase, time.Duration)

// SearchInput carries the search request. The query matches name, username,
// URLs, tags and notes (design §7.1) case-insensitively; scope, type and tag
// narrow the candidate set.
type SearchInput struct {
	Query string
	Scope string
	Type  string
	Tag   string
}

// matcher decides whether one decrypted candidate belongs in the result.
type matcher func(row itemRow, payload any, tags []string) bool

// matchQuery returns a matcher implementing design §7.1: the query must
// appear case-insensitively in the title, username, one of the URLs, a tag
// or the notes/body. An empty query matches everything (pure filtering).
func matchQuery(query, tag string) matcher {
	q := strings.ToLower(query)
	return func(_ itemRow, payload any, tags []string) bool {
		if tag != "" && !containsTag(tags, tag) {
			return false
		}
		if q == "" {
			return true
		}
		return strings.Contains(strings.ToLower(searchableText(payload, tags)), q)
	}
}

// containsTag reports whether tags holds the exact tag (already normalized
// by the stored envelope's dedup).
func containsTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

// searchableText concatenates the fields design §7.1 searches: title,
// username, URLs, tags and notes. The secure note body and SSH key
// comment/fingerprint participate as their type's note-like fields.
func searchableText(payload any, tags []string) string {
	var sb strings.Builder
	sb.WriteString(strings.Join(tags, "\n"))
	switch p := payload.(type) {
	case *LoginPayload:
		sb.WriteString("\n")
		sb.WriteString(p.Name)
		sb.WriteString("\n")
		sb.WriteString(p.Username)
		for _, u := range p.URLs {
			sb.WriteString("\n")
			sb.WriteString(u)
		}
		sb.WriteString("\n")
		sb.WriteString(p.Notes)
	case *SSHKeyPayload:
		sb.WriteString("\n")
		sb.WriteString(p.Name)
		sb.WriteString("\n")
		sb.WriteString(p.Comment)
		sb.WriteString("\n")
		sb.WriteString(p.Fingerprint)
		sb.WriteString("\n")
		sb.WriteString(p.Notes)
	case *CreditCardPayload:
		sb.WriteString("\n")
		sb.WriteString(p.Name)
		sb.WriteString("\n")
		sb.WriteString(p.Cardholder)
		sb.WriteString("\n")
		sb.WriteString(p.Notes)
	case *IdentityPayload:
		sb.WriteString("\n")
		sb.WriteString(p.Name)
		sb.WriteString("\n")
		sb.WriteString(p.FullName)
		sb.WriteString("\n")
		sb.WriteString(p.Company)
		sb.WriteString("\n")
		sb.WriteString(p.Notes)
	case *SecureNotePayload:
		sb.WriteString("\n")
		sb.WriteString(p.Name)
		sb.WriteString("\n")
		sb.WriteString(p.Body)
	}
	return sb.String()
}

// scanMatches walks the authorized candidate set (the caller supplies the
// SQL predicate) newest-first, decrypting every candidate and keeping
// matches until limit+1 are collected or the set is exhausted. Decryption
// only ever happens inside the already-authorized candidate set.
func (s *Service) scanMatches(ctx context.Context, where string, args []any, match matcher, beforeUpdated, beforeID string, limit int) ([]Meta, error) {
	matched := make([]Meta, 0, limit+1)
	var (
		cursorUpdated = beforeUpdated
		cursorID      = beforeID
	)
	for len(matched) <= limit {
		var (
			rows []itemRow
			err  error
		)
		if s.searchProfile == nil {
			rows, err = s.repo.listRows(ctx, s.db, where, args, cursorUpdated, cursorID, searchBatchSize)
		} else {
			started := time.Now()
			rows, err = s.repo.listRows(ctx, s.db, where, args, cursorUpdated, cursorID, searchBatchSize)
			s.profile(SearchPhaseSQL, time.Since(started))
		}
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			var typed any
			var envelope *storedPayload
			if s.searchProfile == nil {
				_, typed, envelope, err = s.decryptRow(row)
			} else {
				_, typed, envelope, err = s.decryptRowWithProfile(row, s.searchProfile)
			}
			if err != nil {
				return nil, err
			}
			if s.searchProfile == nil {
				if match(row, typed, envelope.Tags) {
					meta := row.toMeta()
					meta.Title = TitleOf(typed)
					matched = append(matched, meta)
				}
			} else {
				started := time.Now()
				if match(row, typed, envelope.Tags) {
					meta := row.toMeta()
					meta.Title = TitleOf(typed)
					matched = append(matched, meta)
				}
				s.profile(SearchPhaseMatch, time.Since(started))
			}
			cursorUpdated, cursorID = row.UpdatedAt, row.ID
		}
	}
	return matched, nil
}

func (s *Service) profile(phase SearchPhase, elapsed time.Duration) {
	if s.searchProfile != nil {
		s.searchProfile(phase, elapsed)
	}
}

// Search returns the caller's readable items matching the query, newest
// first. The response carries no totals: other members' data is invisible,
// and page metadata cannot leak it.
func (s *Service) Search(ctx context.Context, actor *auth.Principal, input SearchInput, beforeUpdated, beforeID string, limit int) ([]Meta, error) {
	if actor == nil {
		return nil, ErrForbidden
	}
	if input.Scope != "" && !Scopes[input.Scope] {
		return nil, ErrInvalidScope
	}
	if input.Type != "" && !ItemTypes[input.Type] {
		return nil, ErrInvalidItemType
	}
	where := `deleted_at IS NULL`
	args := []any{}
	switch input.Scope {
	case "":
		where += ` AND ((vault_scope='personal' AND owner_user_id=?) OR vault_scope='shared')`
		args = append(args, actor.UserID)
	case string(ScopePersonal):
		where += ` AND vault_scope='personal' AND owner_user_id=?`
		args = append(args, actor.UserID)
	case string(ScopeShared):
		where += ` AND vault_scope='shared'`
	}
	if input.Type != "" {
		where += ` AND item_type=?`
		args = append(args, input.Type)
	}
	return s.scanMatches(ctx, where, args, matchQuery(input.Query, input.Tag), beforeUpdated, beforeID, limit)
}
