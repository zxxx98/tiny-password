package vault

import (
	"context"
	"strings"

	"github.com/tiny-password/tiny-password/internal/auth"
)

// searchBatchSize bounds how many candidates are decrypted per SQL batch
// while scanning. The whole set never lives in memory at once.
const searchBatchSize = 200

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
		rows, err := s.repo.listRows(ctx, s.db, where, args, cursorUpdated, cursorID, searchBatchSize)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			_, typed, envelope, err := s.decryptRow(row)
			if err != nil {
				return nil, err
			}
			if match(row, typed, envelope.Tags) {
				matched = append(matched, row.toMeta())
			}
			cursorUpdated, cursorID = row.UpdatedAt, row.ID
		}
	}
	return matched, nil
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
