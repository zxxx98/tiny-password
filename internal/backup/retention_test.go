package backup

import (
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t
}

func refs(keep map[string]bool) []string {
	var out []string
	for ref := range keep {
		out = append(out, ref)
	}
	return out
}

func has(keep map[string]bool, refs ...string) bool {
	if len(keep) != len(refs) {
		return false
	}
	set := map[string]bool{}
	for ref := range keep {
		set[ref] = true
	}
	for _, ref := range refs {
		if !set[ref] {
			return false
		}
	}
	return true
}

// The canonical GFS case: daily, weekly and monthly buckets overlap and the
// keep set is exactly the union, computed against explicit expectations.
func TestRetentionKeepSetUnion(t *testing.T) {
	// Candidate timeline (UTC): 10 consecutive daily runs in September 2026,
	// plus older weekly/monthly anchors.
	candidates := []RetentionCandidate{
		{Ref: "d0901", CreatedAt: at("2026-09-01T03:00:00Z")},
		{Ref: "d0902", CreatedAt: at("2026-09-02T03:00:00Z")},
		{Ref: "d0903", CreatedAt: at("2026-09-03T03:00:00Z")},
		{Ref: "d0904", CreatedAt: at("2026-09-04T03:00:00Z")},
		{Ref: "d0905", CreatedAt: at("2026-09-05T03:00:00Z")},
		{Ref: "d0906", CreatedAt: at("2026-09-06T03:00:00Z")},
		{Ref: "d0907", CreatedAt: at("2026-09-07T03:00:00Z")},
		{Ref: "d0908", CreatedAt: at("2026-09-08T03:00:00Z")},
		{Ref: "d0909", CreatedAt: at("2026-09-09T03:00:00Z")},
		{Ref: "d0910", CreatedAt: at("2026-09-10T03:00:00Z")},
		// August 2026 (previous month; ISO week 35).
		{Ref: "m0831", CreatedAt: at("2026-08-31T03:00:00Z")},
		// June 2026 (a second, older month).
		{Ref: "m0601", CreatedAt: at("2026-06-01T03:00:00Z")},
	}
	// daily=7 → newest of each of the 7 most recent days: 0910..0904.
	// weekly=4 → the four most recent ISO weeks PRESENT in the set:
	// W37 (0910), W36 (0906), W35 (0831), W23 (0601).
	// monthly=6 → the six most recent months present: 2026-09 (d0910),
	// 2026-08 (0831), 2026-06 (0601) — three present, all within the limit.
	// The union of the three bucket keep sets keeps d0910..d0904 plus the
	// two anchor artifacts; d0903 and older day-only artifacts are dropped.
	keep := RetentionKeepSet(candidates, 7, 4, 6)
	if !has(keep, "d0910", "d0909", "d0908", "d0907", "d0906", "d0905", "d0904", "m0831", "m0601") {
		t.Fatalf("keep set: %v", refs(keep))
	}
	if keep["d0903"] {
		t.Fatalf("d0903 must be dropped: %v", refs(keep))
	}
}

// One backup that is simultaneously the newest of its day, week and month
// is kept exactly once.
func TestRetentionKeepSetSingleArtifactMultipleBuckets(t *testing.T) {
	candidates := []RetentionCandidate{
		{Ref: "lone", CreatedAt: at("2026-09-06T03:00:00Z")},
	}
	keep := RetentionKeepSet(candidates, 7, 4, 6)
	if !has(keep, "lone") {
		t.Fatalf("keep set: %v", refs(keep))
	}
}

// Fewer backups than buckets: everything is kept.
func TestRetentionKeepSetFewBackups(t *testing.T) {
	candidates := []RetentionCandidate{
		{Ref: "a", CreatedAt: at("2026-01-05T00:00:00Z")},
		{Ref: "b", CreatedAt: at("2025-12-30T00:00:00Z")},
	}
	keep := RetentionKeepSet(candidates, 7, 4, 6)
	if !has(keep, "a", "b") {
		t.Fatalf("keep set: %v", refs(keep))
	}
}

// Cross-year and cross-month boundaries: December and January anchors are
// kept by their monthly buckets even when outside the daily window.
func TestRetentionKeepSetCrossYear(t *testing.T) {
	candidates := []RetentionCandidate{
		{Ref: "jan31", CreatedAt: at("2027-01-31T03:00:00Z")},
		{Ref: "jan02", CreatedAt: at("2027-01-02T03:00:00Z")},
		{Ref: "dec15", CreatedAt: at("2026-12-15T03:00:00Z")},
		{Ref: "nov20", CreatedAt: at("2026-11-20T03:00:00Z")},
		{Ref: "oct10", CreatedAt: at("2026-10-10T03:00:00Z")},
		{Ref: "sep05", CreatedAt: at("2026-09-05T03:00:00Z")},
	}
	// daily=2 → jan31, jan02. weekly=4 → W05'27 (jan31), W01'27 (jan02),
	// W51'26 (dec15), W47'26 (nov20). monthly=6 → 01'27 (jan31), 12'26
	// (dec15), 11'26 (nov20), 10'26 (oct10), 09'26 (sep05).
	keep := RetentionKeepSet(candidates, 2, 4, 6)
	if !has(keep, "jan31", "jan02", "dec15", "nov20", "oct10", "sep05") {
		t.Fatalf("keep set: %v", refs(keep))
	}
}

// Bucket counts are configurable; tightening retention drops older buckets
// immediately.
func TestRetentionKeepSetAdjustableCounts(t *testing.T) {
	candidates := []RetentionCandidate{
		{Ref: "d1", CreatedAt: at("2026-09-06T03:00:00Z")},
		{Ref: "d2", CreatedAt: at("2026-09-05T03:00:00Z")},
		{Ref: "d3", CreatedAt: at("2026-09-04T03:00:00Z")},
	}
	strict := RetentionKeepSet(candidates, 1, 1, 1)
	if !has(strict, "d1") {
		t.Fatalf("strict keep set: %v", refs(strict))
	}
	// Zero disables the level: only the other buckets decide.
	dailyOff := RetentionKeepSet(candidates, 0, 1, 1)
	if !has(dailyOff, "d1") {
		t.Fatalf("daily-off keep set: %v", refs(dailyOff))
	}
	allOff := RetentionKeepSet(candidates, 0, 0, 0)
	if len(allOff) != 0 {
		t.Fatalf("all-off keep set: %v", refs(allOff))
	}
}

// Buckets are UTC calendar buckets: a backup created at a US DST boundary
// lands in the same bucket an equivalent UTC instant always occupies; the
// keep set is identical to the non-DST case.
func TestRetentionKeepSetDSTStable(t *testing.T) {
	// 2026-03-08: New York jumps 02:00→03:00 EST→EDT (07:00 UTC).
	normal := []RetentionCandidate{
		{Ref: "early", CreatedAt: at("2026-03-08T05:00:00Z")}, // 00:00 EST
		{Ref: "late", CreatedAt: at("2026-03-08T08:00:00Z")},  // 04:00 EDT
		{Ref: "prev", CreatedAt: at("2026-03-07T05:00:00Z")},
	}
	duringDST := []RetentionCandidate{
		{Ref: "early", CreatedAt: at("2026-03-08T05:00:00Z")},
		{Ref: "late", CreatedAt: at("2026-03-08T08:00:00Z")},
		{Ref: "prev", CreatedAt: at("2026-03-07T05:00:00Z")},
	}
	keepA := RetentionKeepSet(normal, 1, 1, 1)
	keepB := RetentionKeepSet(duringDST, 1, 1, 1)
	if len(keepA) != len(keepB) {
		t.Fatalf("DST changed the keep set: %v vs %v", refs(keepA), refs(keepB))
	}
	for ref := range keepA {
		if !keepB[ref] {
			t.Fatalf("DST dropped %s", ref)
		}
	}
}

// Ties on creation time resolve deterministically by reference.
func TestRetentionKeepSetTieBreakDeterministic(t *testing.T) {
	a := []RetentionCandidate{
		{Ref: "aaa", CreatedAt: at("2026-09-06T03:00:00Z")},
		{Ref: "zzz", CreatedAt: at("2026-09-06T03:00:00Z")},
	}
	keep := RetentionKeepSet(a, 1, 1, 1)
	if len(keep) != 1 {
		t.Fatalf("keep set size: %v", refs(keep))
	}
	keep2 := RetentionKeepSet(a, 1, 1, 1)
	if refs(keep)[0] != refs(keep2)[0] {
		t.Fatal("tie-break not deterministic")
	}
}
