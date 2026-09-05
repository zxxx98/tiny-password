package vault

import (
	"context"
	"strings"

	"github.com/tiny-password/tiny-password/internal/auth"
)

// WeakPasswordRunes is the local weakness rule: a login password shorter
// than this many code points is weak (design §7.2). No password fingerprints
// are stored and no external services are consulted — classification is
// computed on demand from decrypted payloads in memory.
const WeakPasswordRunes = 12

// HealthItem is one login item with at least one finding.
type HealthItem struct {
	ItemID  string   `json:"item_id"`
	Reasons []string `json:"reasons"`
}

// HealthReport summarizes the caller's readable login items. Shared items
// participate in the caller's local results; other members' personal items
// are invisible by construction (design §7.2).
type HealthReport struct {
	Weak    int          `json:"weak"`
	Reused  int          `json:"reused"`
	Expired int          `json:"expired"`
	Items   []HealthItem `json:"items"`
}

const (
	reasonWeak    = "weak"
	reasonReused  = "reused"
	reasonExpired = "expired"
)

// Health classifies the caller's readable login items on demand. The
// candidate set is narrowed in SQL first; only authorized payloads decrypt.
func (s *Service) Health(ctx context.Context, actor *auth.Principal) (HealthReport, error) {
	report := HealthReport{Items: []HealthItem{}}
	if actor == nil {
		return report, ErrForbidden
	}
	where := `deleted_at IS NULL AND item_type='login' AND ((vault_scope='personal' AND owner_user_id=?) OR vault_scope='shared')`
	rows, err := s.repo.listRows(ctx, s.db, where, []any{actor.UserID}, "", "", 0)
	if err != nil {
		return report, err
	}
	type finding struct {
		id      string
		reasons []string
	}
	passwordOwners := make(map[string][]string)
	findings := make([]finding, 0, len(rows))
	for _, row := range rows {
		_, typed, _, err := s.decryptRow(row)
		if err != nil {
			return report, err
		}
		login, ok := typed.(*LoginPayload)
		if !ok {
			continue
		}
		var reasons []string
		if weakPassword(login.Password) {
			reasons = append(reasons, reasonWeak)
		}
		if expiredPasswordDate(login.PasswordExpiresAt, s.now().UTC().Format("2006-01-02")) {
			reasons = append(reasons, reasonExpired)
		}
		if login.Password != "" {
			passwordOwners[login.Password] = append(passwordOwners[login.Password], row.ID)
		}
		findings = append(findings, finding{id: row.ID, reasons: reasons})
	}
	reusedByItem := make(map[string]bool)
	for _, ids := range passwordOwners {
		if len(ids) < 2 {
			continue
		}
		for _, id := range ids {
			reusedByItem[id] = true
		}
	}
	for _, f := range findings {
		if reusedByItem[f.id] {
			f.reasons = append(f.reasons, reasonReused)
		}
		if len(f.reasons) == 0 {
			continue
		}
		// Stable order: weak, reused, expired (matches the OpenAPI enum).
		ordered := make([]string, 0, 3)
		for _, reason := range []string{reasonWeak, reasonReused, reasonExpired} {
			for _, have := range f.reasons {
				if have == reason {
					ordered = append(ordered, reason)
					break
				}
			}
		}
		report.Items = append(report.Items, HealthItem{ItemID: f.id, Reasons: ordered})
		for _, reason := range ordered {
			switch reason {
			case reasonWeak:
				report.Weak++
			case reasonReused:
				report.Reused++
			case reasonExpired:
				report.Expired++
			}
		}
	}
	return report, nil
}

// weakPassword is the local weakness rule (design §7.2): fewer than
// WeakPasswordRunes code points.
func weakPassword(password string) bool {
	return len([]rune(password)) < WeakPasswordRunes
}

// expiredPasswordDate reports whether the YYYY-MM-DD expiry (when present)
// is strictly before today. Fixed-width dates make the string comparison
// sound; empty or absent values never expire.
func expiredPasswordDate(expires *string, today string) bool {
	if expires == nil || *expires == "" {
		return false
	}
	return strings.Compare(*expires, today) < 0
}
