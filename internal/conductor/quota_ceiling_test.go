// Purpose: tests for quota lane ceiling enforcement, atomicity, Apply, and sentinels (AUD-030).
// Inputs: frozen Clock, QuotaConfig with CeilingOverrides.
// Outputs: test assertions for acceptance criteria.
// Constraints: injected Clock, no sleep, file <= 300 lines, functions <= 50 lines.
// SPORT: conductor.quota/AUD-030 (P1-CAP-14).

package conductor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// errIdentity reports whether target appears in err's chain by pointer
// identity, walking errors.Unwrap by hand. cascade.(*Error).Is compares
// Kind only (pkg/cascade/errors.go), so errors.Is(err, ErrAllLanesExhausted)
// also passes for ErrLaneCeilingReached, ErrInvalidQuotaConfig or any other
// KindQuotaExhausted sentinel -- the wrong assertion for "this exact
// sentinel is in the chain." Assertions naming a specific sentinel use this
// instead; the errors.Is(...cascade.ErrQuotaExhausted) call in
// TestQuotaCeilingSentinelsAndProbe is deliberately the Kind-only check,
// documenting that collision rather than asserting past it.
func errIdentity(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		err = errors.Unwrap(err)
	}
	return false
}

func TestQuotaCeilingEnforced(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	cfg := QuotaConfig{
		SpillOrder:       []LaneID{"laneA", "laneB"},
		CeilingOverrides: map[LaneID]int64{"laneA": 2},
	}
	p := NewQuotaPolicy(cfg, clock)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		lane, err := p.NextLane(ctx, nil)
		if err != nil || lane != "laneA" {
			t.Fatalf("call %d: got (%q, %v), want (laneA, nil)", i+1, lane, err)
		}
	}

	lane, err := p.NextLane(ctx, nil)
	if err != nil || lane != "laneB" {
		t.Fatalf("third call: got (%q, %v), want (laneB, nil)", lane, err)
	}

	testSingleLaneExhausted(t, clock)

	clock.Advance(CeilingWindow + time.Second)
	lane, err = p.NextLane(ctx, nil)
	if err != nil || lane != "laneA" {
		t.Fatalf("after window: got (%q, %v), want (laneA, nil)", lane, err)
	}
}

func testSingleLaneExhausted(t *testing.T, clock *testkit.FrozenClock) {
	t.Helper()
	ctx := context.Background()
	pSingle := NewQuotaPolicy(QuotaConfig{
		SpillOrder:       []LaneID{"laneA"},
		CeilingOverrides: map[LaneID]int64{"laneA": 2},
	}, clock)
	_, _ = pSingle.NextLane(ctx, nil)
	_, _ = pSingle.NextLane(ctx, nil)
	_, err := pSingle.NextLane(ctx, nil)
	if !errIdentity(err, ErrAllLanesExhausted) {
		t.Fatalf("pSingle: got %v, want ErrAllLanesExhausted (identity)", err)
	}
	if !strings.Contains(err.Error(), "laneA") {
		t.Fatalf("pSingle: error message %q does not name skipped laneA", err.Error())
	}
}

func TestQuotaCeilingAtomicUnderConcurrency(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	cfg := QuotaConfig{
		SpillOrder:       []LaneID{"laneA"},
		CeilingOverrides: map[LaneID]int64{"laneA": 5},
	}
	p := NewQuotaPolicy(cfg, clock)
	ctx := context.Background()

	const total = 64
	barrier := make(chan struct{})
	type result struct {
		lane LaneID
		err  error
	}
	results := make(chan result, total)

	var wg sync.WaitGroup
	wg.Add(total)
	for i := 0; i < total; i++ {
		go func() {
			defer wg.Done()
			<-barrier
			lane, err := p.NextLane(ctx, nil)
			results <- result{lane: lane, err: err}
		}()
	}
	close(barrier)
	wg.Wait()
	close(results)

	var successCount, exhaustedCount int
	for res := range results {
		if res.err == nil && res.lane == "laneA" {
			successCount++
		} else if errIdentity(res.err, ErrAllLanesExhausted) {
			exhaustedCount++
		} else {
			t.Fatalf("unexpected result: lane=%q, err=%v", res.lane, res.err)
		}
	}
	if successCount != 5 || exhaustedCount != 59 {
		t.Fatalf("got success=%d exhausted=%d; want 5 success and 59 exhausted", successCount, exhaustedCount)
	}
}

func TestQuotaPolicyApply(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	p := NewQuotaPolicy(QuotaConfig{
		SpillOrder:       []LaneID{"laneA", "laneB"},
		CeilingOverrides: map[LaneID]int64{"laneA": 5, "laneB": 5},
	}, clock)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if lane, err := p.NextLane(ctx, nil); err != nil || lane != "laneA" {
			t.Fatalf("admit %d: got (%q, %v), want (laneA, nil)", i, lane, err)
		}
	}
	p.markLimited("laneB")

	p.Apply(QuotaConfig{
		SpillOrder:       []LaneID{"laneB", "laneA"},
		CeilingOverrides: map[LaneID]int64{"laneA": 2, "laneB": 5},
	})

	_, err := p.NextLane(ctx, nil)
	if !errIdentity(err, ErrAllLanesExhausted) {
		t.Fatalf("after Apply: expected ErrAllLanesExhausted (identity), got %v", err)
	}
	if !strings.Contains(err.Error(), "laneA") {
		t.Fatalf("after Apply: expected error message to name laneA, got %q", err.Error())
	}

	clock.Advance(defaultRateLimitWindow + time.Second)
	lane, err := p.NextLane(ctx, nil)
	if err != nil || lane != "laneB" {
		t.Fatalf("after demotion clear: got (%q, %v), want (laneB, nil)", lane, err)
	}

	testApplySwapsOrder(t, clock)
}

// testApplySwapsOrder proves Apply actually replaces p.order rather than
// leaving the constructor's order in place: with no ceilings in play,
// order alone decides which lane NextLane returns, so a policy built
// [laneA, laneB] that is Applied [laneB, laneA] must return laneB next.
// Mutation check: dropping `p.order = order` in Apply turns this red
// (NextLane would keep returning laneA).
func testApplySwapsOrder(t *testing.T, clock *testkit.FrozenClock) {
	t.Helper()
	ctx := context.Background()
	p := NewQuotaPolicy(QuotaConfig{SpillOrder: []LaneID{"laneA", "laneB"}}, clock)
	p.Apply(QuotaConfig{SpillOrder: []LaneID{"laneB", "laneA"}})

	lane, err := p.NextLane(ctx, nil)
	if err != nil || lane != "laneB" {
		t.Fatalf("after Apply order swap: got (%q, %v), want (laneB, nil)", lane, err)
	}
}

func TestQuotaCeilingSentinelsAndProbe(t *testing.T) {
	k, _ := cascade.KindOf(ErrLaneCeilingReached)
	if !cascade.HasKind(ErrLaneCeilingReached, cascade.KindQuotaExhausted) {
		t.Fatalf("ErrLaneCeilingReached kind = %v, want KindQuotaExhausted", k)
	}
	if !errors.Is(ErrLaneCeilingReached, cascade.ErrQuotaExhausted) {
		t.Fatal("ErrLaneCeilingReached does not match cascade.ErrQuotaExhausted via errors.Is")
	}

	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	pZero := NewQuotaPolicy(QuotaConfig{
		SpillOrder:       []LaneID{"laneZero"},
		CeilingOverrides: map[LaneID]int64{"laneZero": 0},
	}, clock)
	if err := pZero.AdmitLane("laneZero"); err != ErrLaneCeilingReached {
		t.Fatalf("AdmitLane ceiling 0: got %v, want ErrLaneCeilingReached (identity)", err)
	}
	_, err := pZero.NextLane(context.Background(), nil)
	if !errIdentity(err, ErrAllLanesExhausted) {
		t.Fatalf("NextLane ceiling 0: got %v, want ErrAllLanesExhausted (identity)", err)
	}
	if !strings.Contains(err.Error(), "laneZero") {
		t.Fatalf("NextLane ceiling 0: expected error message to name laneZero, got %q", err.Error())
	}
}
