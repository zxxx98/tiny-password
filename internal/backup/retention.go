package backup

import (
	"sort"
	"strconv"
	"time"
)

// RetentionCandidate is one delivered backup on one target, identified by
// its opaque reference (local file name stem / R2 object key) and creation
// time. Retention works exclusively on this closed set — failed or
// interrupted runs never enter it, so a failing backup can never cause the
// deletion of the last usable backup (T24).
type RetentionCandidate struct {
	Ref       string
	CreatedAt time.Time
}

// RetentionKeepSet computes the grandfather-father-son keep set (design
// §11.4): the newest backup of each of the most recent `daily` distinct UTC
// days, plus the newest of each of the most recent `weekly` distinct ISO
// weeks, plus the newest of each of the most recent `monthly` distinct
// months. Buckets that contain the same backup contribute it once — the
// result is the union of the three bucket keep sets. A zero number disables
// that bucket level. Buckets are UTC calendar buckets, so DST transitions
// never shift a backup between buckets.
func RetentionKeepSet(candidates []RetentionCandidate, daily, weekly, monthly int) map[string]bool {
	if len(candidates) == 0 {
		return map[string]bool{}
	}
	// Newest first: the head of any bucket listing is that bucket's keep.
	sorted := make([]RetentionCandidate, len(candidates))
	copy(sorted, candidates)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].CreatedAt.Equal(sorted[j].CreatedAt) {
			return sorted[i].Ref > sorted[j].Ref
		}
		return sorted[i].CreatedAt.After(sorted[j].CreatedAt)
	})

	keep := map[string]bool{}
	buckets := []struct {
		limit int
		key   func(time.Time) string
	}{
		{daily, func(t time.Time) string { return t.UTC().Format("2006-01-02") }},
		{weekly, func(t time.Time) string { y, w := t.UTC().ISOWeek(); return strconv.Itoa(y) + "-W" + strconv.Itoa(w) }},
		{monthly, func(t time.Time) string { return t.UTC().Format("2006-01") }},
	}
	for _, b := range buckets {
		if b.limit <= 0 {
			continue
		}
		seen := map[string]bool{}
		for _, c := range sorted {
			key := b.key(c.CreatedAt)
			if seen[key] {
				continue
			}
			seen[key] = true
			keep[c.Ref] = true
			if len(seen) == b.limit {
				break
			}
		}
	}
	return keep
}
