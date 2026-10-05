package learn

// Purpose: the capability scorer's seed, posterior, scope-key, closed-db and
//   capacity-interface tests, plus the shared fixtures the other P1-CAP-03
//   tests use (an injected, steppable clock; direct row seed and read-back).
//   Every storage test opens real modernc sqlite under t.TempDir().
// SPORT: internal.learn.SQLiteCapabilityScorer/TESTED (P1-CAP-03).

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/pkg/cascade"
)

// stepClock is an injected, steppable clock.
type stepClock struct{ t time.Time }

func (c *stepClock) Now() time.Time          { return c.t }
func (c *stepClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newStepClock() *stepClock { return &stepClock{t: newTestClock().Now()} }

type storedScore struct {
	alpha, beta float64
	count       int
	last        int64
}

// seedScore inserts one observation row directly (bypassing Observe).
func seedScore(t *testing.T, scorer *SQLiteCapabilityScorer, scope ScopeKey, tc conductor.TaskClass, tier capacity.Tier, alpha, beta float64, count int, last time.Time) {
	t.Helper()
	_, err := scorer.db.Exec(`INSERT INTO `+tableCapabilityScore+
		` (scope_key, task_class, tier, alpha, beta, observation_count, last_updated) VALUES (?,?,?,?,?,?,?)`,
		string(scope), string(tc), string(tier), alpha, beta, count, last.Unix())
	if err != nil {
		t.Fatalf("seed score row: %v", err)
	}
}

// readStored reads one observation row back; found is false when absent.
func readStored(t *testing.T, scorer *SQLiteCapabilityScorer, scope ScopeKey, tc conductor.TaskClass, tier capacity.Tier) (storedScore, bool) {
	t.Helper()
	var r storedScore
	rows, err := scorer.db.Query(`SELECT alpha, beta, observation_count, last_updated FROM `+tableCapabilityScore+
		` WHERE scope_key = ? AND task_class = ? AND tier = ?`, string(scope), string(tc), string(tier))
	if err != nil {
		t.Fatalf("read stored score: %v", err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return r, false
	}
	if err := rows.Scan(&r.alpha, &r.beta, &r.count, &r.last); err != nil {
		t.Fatalf("scan stored score: %v", err)
	}
	return r, true
}

func newTestScorer(t *testing.T, clock *stepClock) *SQLiteCapabilityScorer {
	t.Helper()
	return NewSQLiteCapabilityScorer(newTestOutcomeDB(t), clock)
}

func near(a, b float64) bool { return math.Abs(a-b) <= 1e-9 }

// TestBetaSeed: every populated cell seeds alpha0+beta0 == 4 and
// alpha0/(alpha0+beta0) == the table value; cells the table does not hold
// report ok=false; and internal/learn declares no priors map of its own.
func TestBetaSeed(t *testing.T) {
	cells := 0
	for tier, row := range capacity.Priors {
		for tc, p := range row {
			a, b, ok := PriorAlphaBeta(tier, tc)
			if !ok || !near(a+b, 4) || !near(a/(a+b), p) {
				t.Errorf("PriorAlphaBeta(%s, %s) = (%v, %v, %v), want mass 4 and mean %v", tier, tc, a, b, ok, p)
			}
			cells++
		}
	}
	if cells != 28 {
		t.Fatalf("checked %d priors cells, want 28 (4 tiers x 7 classes)", cells)
	}
	for _, tc := range []conductor.TaskClass{conductor.TaskClassSegment, conductor.TaskClassChat, "bogus"} {
		if _, _, ok := PriorAlphaBeta(capacity.TierOne, tc); ok {
			t.Errorf("PriorAlphaBeta(tier-1, %q) ok = true, want false (no prior cell)", tc)
		}
	}
	if _, _, ok := PriorAlphaBeta("tier-9", conductor.TaskClassCode); ok {
		t.Error("PriorAlphaBeta(unknown tier) ok = true, want false")
	}
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob learn sources: %v (%d files)", err, len(files))
	}
	for _, f := range files {
		src, rerr := os.ReadFile(f)
		if rerr != nil {
			t.Fatal(rerr)
		}
		if !strings.HasSuffix(f, "_test.go") && strings.Contains(string(src), "map[capacity."+"Tier]") {
			t.Errorf("%s declares a map keyed by capacity.Tier: a second priors table (R-16.71)", f)
		}
	}
}

// TestCapabilityScore: an empty table scores the prior mean; Observe stores
// decayed mass and last_updated; the posterior mean follows the stored row and
// the 30-day half-life; Observe refuses a cell with no prior and stores nothing.
func TestCapabilityScore(t *testing.T) {
	clock := newStepClock()
	s := newTestScorer(t, clock)
	ctx := context.Background()
	tier, tc := capacity.TierOne, conductor.TaskClassCode
	if got, want := s.Score(scopeGlobal, tc, tier), capacity.Priors[tier][tc]; !near(got, want) {
		t.Fatalf("empty-table Score = %v, want the prior mean %v", got, want)
	}
	if err := s.Observe(ctx, scopeGlobal, tc, tier, true); err != nil {
		t.Fatalf("Observe success: %v", err)
	}
	row, found := readStored(t, s, scopeGlobal, tc, tier)
	if !found || row.alpha != 1 || row.beta != 0 || row.count != 1 || row.last != clock.t.Unix() {
		t.Fatalf("stored after one success = %+v (found %v), want alpha 1 beta 0 count 1 last now", row, found)
	}
	a0, b0, _ := PriorAlphaBeta(tier, tc)
	if got, want := s.Score(scopeGlobal, tc, tier), (a0+1)/(a0+b0+1); !near(got, want) {
		t.Errorf("Score after one success = %v, want %v", got, want)
	}
	clock.advance(30 * 24 * time.Hour)
	if got, want := s.Score(scopeGlobal, tc, tier), (a0+0.5)/(a0+b0+0.5); !near(got, want) {
		t.Errorf("Score 30 days later = %v, want %v (observation weighs 0.5)", got, want)
	}
	if err := s.Observe(ctx, scopeGlobal, tc, tier, false); err != nil {
		t.Fatalf("Observe failure: %v", err)
	}
	if row, _ = readStored(t, s, scopeGlobal, tc, tier); !near(row.alpha, 0.5) || row.beta != 1 || row.count != 2 || row.last != clock.t.Unix() {
		t.Errorf("stored after decayed fold = %+v, want alpha 0.5 beta 1 count 2 last now", row)
	}
}

// TestObserveRefusalsStoreNothing: a bad scope or a cell with no prior is
// refused (KindInvalidInput) and no row is written; a future last_updated is
// not amplified by the fold.
func TestObserveRefusalsStoreNothing(t *testing.T) {
	clock := newStepClock()
	s := newTestScorer(t, clock)
	ctx := context.Background()
	for _, c := range []struct {
		scope ScopeKey
		tc    conductor.TaskClass
		tier  capacity.Tier
	}{
		{scopeGlobal, conductor.TaskClassChat, capacity.TierOne},
		{scopeGlobal, conductor.TaskClassCode, "tier-9"},
		{"repo:bad/id", conductor.TaskClassCode, capacity.TierOne},
	} {
		if err := s.Observe(ctx, c.scope, c.tc, c.tier, true); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("Observe(%q, %q, %q) = %v, want KindInvalidInput", c.scope, c.tc, c.tier, err)
		}
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + tableCapabilityScore).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows after refused observations = %d (err %v), want 0", n, err)
	}
	seedScore(t, s, scopeGlobal, conductor.TaskClassCode, capacity.TierOne, 2, 0, 2, clock.t.Add(10*24*time.Hour))
	if err := s.Observe(ctx, scopeGlobal, conductor.TaskClassCode, capacity.TierOne, true); err != nil {
		t.Fatal(err)
	}
	if row, _ := readStored(t, s, scopeGlobal, conductor.TaskClassCode, capacity.TierOne); row.alpha != 3 {
		t.Errorf("alpha after folding onto a future timestamp = %v, want exactly 3 (never amplified)", row.alpha)
	}
}

// TestParseScopeKey: exactly the three forms parse; anything else is
// KindInvalidInput and the message never echoes the input.
func TestParseScopeKey(t *testing.T) {
	for _, ok := range []string{"global", "repo:repo-opaque-1", "lang:go", "lang:unknown"} {
		if k, err := ParseScopeKey(ok); err != nil || string(k) != ok {
			t.Errorf("ParseScopeKey(%q) = (%q, %v), want ok", ok, k, err)
		}
	}
	for _, bad := range []string{"", "repo:", "repo:a/b", "repo:a b", "lang:", "lang:klingon", "Global", "global:x", "x:y",
		"repo:" + strings.Repeat("a", 65)} {
		_, err := ParseScopeKey(bad)
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("ParseScopeKey(%q) err = %v, want KindInvalidInput", bad, err)
		} else if _, payload, _ := strings.Cut(bad, ":"); len(payload) > 1 && strings.Contains(err.Error(), payload) {
			t.Errorf("ParseScopeKey(%q) error echoes the input: %v", bad, err)
		}
	}
}

// TestScorerSatisfiesCapacityInterface: For(scopeKey) is a
// capacity.CapabilityScorer equal to Score(scopeKey, tc, tier); an empty
// table or an unobserved class scores the prior mean; a class with no prior
// and an unparseable scope key score 0.0.
func TestScorerSatisfiesCapacityInterface(t *testing.T) {
	clock := newStepClock()
	s := newTestScorer(t, clock)
	seedScore(t, s, scopeGlobal, conductor.TaskClassCode, capacity.TierTwo, 6, 1, 7, clock.t)
	cs := s.For(string(scopeGlobal))
	for tier, row := range capacity.Priors {
		for tc, p := range row {
			got := cs.Score(tier, tc)
			if want := s.Score(scopeGlobal, tc, tier); got != want {
				t.Errorf("For(global).Score(%s, %s) = %v, want Score = %v", tier, tc, got, want)
			}
			if (tier != capacity.TierTwo || tc != conductor.TaskClassCode) && !near(got, p) {
				t.Errorf("unobserved (%s, %s) scores %v, want the prior mean %v", tier, tc, got, p)
			}
		}
	}
	a0, b0, _ := PriorAlphaBeta(capacity.TierTwo, conductor.TaskClassCode)
	if got, want := cs.Score(capacity.TierTwo, conductor.TaskClassCode), (a0+6)/(a0+b0+7); !near(got, want) {
		t.Errorf("observed cell scores %v, want %v", got, want)
	}
	for _, tc := range []conductor.TaskClass{conductor.TaskClassChat, conductor.TaskClassSegment, "bogus"} {
		if got := cs.Score(capacity.TierOne, tc); got != 0.0 {
			t.Errorf("class %q with no prior scores %v, want 0.0", tc, got)
		}
	}
	if got := s.For("not-a-scope").Score(capacity.TierOne, conductor.TaskClassCode); got != 0.0 {
		t.Errorf("unparseable scope key scores %v, want 0.0", got)
	}
}

// TestCapabilityScorer_ClosedDB: a closed database is KindUnavailable for
// Posterior and Observe and scores 0.0; an unconstructed scorer is refused.
func TestCapabilityScorer_ClosedDB(t *testing.T) {
	s := newTestScorer(t, newStepClock())
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := s.Posterior(ctx, scopeGlobal, conductor.TaskClassCode, capacity.TierOne); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("Posterior on a closed db = %v, want KindUnavailable", err)
	}
	if err := s.Observe(ctx, scopeGlobal, conductor.TaskClassCode, capacity.TierOne, true); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("Observe on a closed db = %v, want KindUnavailable", err)
	}
	if got := s.Score(scopeGlobal, conductor.TaskClassCode, capacity.TierOne); got != 0.0 {
		t.Errorf("Score on a closed db = %v, want 0.0", got)
	}
	var nilScorer *SQLiteCapabilityScorer
	if _, err := nilScorer.Posterior(ctx, scopeGlobal, conductor.TaskClassCode, capacity.TierOne); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("nil scorer Posterior = %v, want KindInvalidInput", err)
	}
}
