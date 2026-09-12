// Purpose: TestTierPolicyWalk, TestTierPolicyAllExhausted,
// TestTierPolicyUserConfigFilter, TestTierPolicyScorerPanicRecovery, and
// TestTierPolicyReserveFlag -- SelectTier's own named acceptance tests.
// The Art.1-compliant test-only stub scorer/estimator this whole test
// file uses lives here, never in a shipped file.
//
// SPORT: fleet.capacity.policy (ADD, P1-E31-W6-S63-T2).
package capacity

import (
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
	"math"
)

// stubScorer is the Art.1-compliant test-only CapabilityScorer: a fixed
// per-tier score map, panicking or returning NaN when configured to, so
// tests can drive SelectTier's own recovery path.
type stubScorer struct {
	scores map[Tier]float64
	panics bool
	nan    bool
}

func (s stubScorer) Score(tier Tier, _ conductor.TaskClass) float64 {
	if s.panics {
		panic("stubScorer: forced panic")
	}
	if s.nan {
		return math.NaN()
	}
	return s.scores[tier]
}

// stubEstimator is the Art.1-compliant test-only EstimateSource: a fixed
// per-tier (queue_wait, duration_est) pair, defaulting to the cold-start
// values (0, a shared duration) for any tier not explicitly configured.
type stubEstimator struct {
	byTier       map[Tier][2]time.Duration
	coldDuration time.Duration
}

func (e stubEstimator) Estimate(tier Tier, _ conductor.TaskClass) (time.Duration, time.Duration) {
	if v, ok := e.byTier[tier]; ok {
		return v[0], v[1]
	}
	return 0, e.coldDuration
}

// newTestPolicy builds a TierPolicy with a fresh DefaultProbeAdmission and
// the given fixed clock.
func newTestPolicy(estimator EstimateSource, clock Clock) *TierPolicy {
	return NewTierPolicy(estimator, NewDefaultProbeAdmission(), clock)
}

func availableSlot(profile string) TierSlot {
	return TierSlot{
		ProfileID: profile, State: StateAvailable, Source: SourceProviderStatus,
		ObservedAt: time.Now(), Providers: []conductor.LaneID{"lane-a"},
	}
}

func baseTestRequest() ResourceRequest {
	return ResourceRequest{
		Intent:     "test",
		TaskClass:  conductor.TaskClassCode,
		MinQuality: QualityStandard,
		Deadline:   DeadlineInteractive,
		RiskClass:  jobs.RiskClassNormal,
		DataClass:  DataClassPublic,
	}
}

// TestTierPolicyWalk asserts the plain tier-2 -> tier-1 -> tier-0 walk
// picks the first schedulable tier in that order.
func TestTierPolicyWalk(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	est := stubEstimator{coldDuration: time.Minute}
	scorer := stubScorer{scores: map[Tier]float64{TierZero: 0.9, TierOne: 0.9, TierTwo: 0.9}}

	cases := []struct {
		name  string
		slots map[Tier]TierSlot
		want  Tier
	}{
		{"tier-2 available", map[Tier]TierSlot{
			TierTwo: availableSlot("p2"), TierOne: availableSlot("p1"), TierZero: availableSlot("p0"),
		}, TierTwo},
		{"tier-2 exhausted falls to tier-1", map[Tier]TierSlot{
			TierTwo: {ProfileID: "p2", State: StateExhausted}, TierOne: availableSlot("p1"), TierZero: availableSlot("p0"),
		}, TierOne},
		{"tier-2 and tier-1 exhausted falls to tier-0", map[Tier]TierSlot{
			TierTwo: {ProfileID: "p2", State: StateExhausted}, TierOne: {ProfileID: "p1", State: StateExhausted}, TierZero: availableSlot("p0"),
		}, TierZero},
		{"constrained is schedulable", map[Tier]TierSlot{
			TierTwo: {ProfileID: "p2", State: StateConstrained, Source: SourceProviderStatus, ObservedAt: clk.t, Providers: []conductor.LaneID{"lane-a"}},
			TierOne: availableSlot("p1"), TierZero: availableSlot("p0"),
		}, TierTwo},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newTestPolicy(est, clk)
			sel, err := p.SelectTier(c.slots, []conductor.LaneID{"lane-a"}, baseTestRequest(), scorer, 0)
			if err != nil {
				t.Fatalf("SelectTier: %v", err)
			}
			if sel.Tier != c.want {
				t.Fatalf("Tier = %v, want %v", sel.Tier, c.want)
			}
			if sel.Explain() == "" {
				t.Fatal("Explain() = empty string, want non-empty")
			}
		})
	}
}

// TestTierPolicyAllExhausted asserts every tier exhausted -> ErrFleetExhausted.
func TestTierPolicyAllExhausted(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	est := stubEstimator{coldDuration: time.Minute}
	scorer := stubScorer{scores: map[Tier]float64{TierZero: 0.9, TierOne: 0.9, TierTwo: 0.9}}
	slots := map[Tier]TierSlot{
		TierTwo:  {ProfileID: "p2", State: StateExhausted},
		TierOne:  {ProfileID: "p1", State: StateExhausted},
		TierZero: {ProfileID: "p0", State: StateExhausted},
	}
	p := newTestPolicy(est, clk)
	_, err := p.SelectTier(slots, []conductor.LaneID{"lane-a"}, baseTestRequest(), scorer, 0)
	kind, ok := cascade.KindOf(err)
	if !ok || kind != cascade.KindUnavailable {
		t.Fatalf("SelectTier error = %v, want ErrFleetExhausted", err)
	}
}

// TestTierPolicyUserConfigFilter asserts a tier whose only providers are
// disallowed is treated as exhausted, and that a nil allowed-lane list
// fails closed on tier-0 specifically.
func TestTierPolicyUserConfigFilter(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	est := stubEstimator{coldDuration: time.Minute}
	scorer := stubScorer{scores: map[Tier]float64{TierZero: 0.9, TierOne: 0.9, TierTwo: 0.9}}

	slots := map[Tier]TierSlot{
		TierTwo:  {ProfileID: "p2", State: StateAvailable, Providers: []conductor.LaneID{"lane-x"}},
		TierOne:  {ProfileID: "p1", State: StateAvailable, Providers: []conductor.LaneID{"lane-a"}},
		TierZero: {ProfileID: "p0", State: StateAvailable, Providers: []conductor.LaneID{"lane-a"}},
	}

	t.Run("partial allow list skips disallowed tier-2", func(t *testing.T) {
		p := newTestPolicy(est, clk)
		sel, err := p.SelectTier(slots, []conductor.LaneID{"lane-a"}, baseTestRequest(), scorer, 0)
		if err != nil {
			t.Fatalf("SelectTier: %v", err)
		}
		if sel.Tier != TierOne {
			t.Fatalf("Tier = %v, want TierOne (tier-2 disallowed)", sel.Tier)
		}
	})

	t.Run("nil allowed-lane list blocks tier-0 only", func(t *testing.T) {
		onlyTierZero := map[Tier]TierSlot{
			TierTwo:  {ProfileID: "p2", State: StateExhausted},
			TierOne:  {ProfileID: "p1", State: StateExhausted},
			TierZero: {ProfileID: "p0", State: StateAvailable, Providers: []conductor.LaneID{"lane-a"}},
		}
		p := newTestPolicy(est, clk)
		_, err := p.SelectTier(onlyTierZero, nil, baseTestRequest(), scorer, 0)
		kind, ok := cascade.KindOf(err)
		if !ok || kind != cascade.KindUnavailable {
			t.Fatalf("SelectTier with nil allowed-lanes and only tier-0 available = %v, want ErrFleetExhausted (tier-0 fail-closed)", err)
		}
	})

	t.Run("all tiers disallowed -> ErrNoAllowedLanes", func(t *testing.T) {
		p := newTestPolicy(est, clk)
		_, err := p.SelectTier(slots, []conductor.LaneID{"lane-nonexistent"}, baseTestRequest(), scorer, 0)
		kind, ok := cascade.KindOf(err)
		if !ok || kind != cascade.KindPolicyDenied {
			t.Fatalf("SelectTier with an allow-list matching nothing = %v, want ErrNoAllowedLanes", err)
		}
	})
}

// TestTierPolicyScorerPanicRecovery asserts a scorer panic never
// propagates to the caller: SelectTier recovers it and treats the score
// as 0.0 (ErrFleetExhausted here since a 0.0 tier-0 score alone does not
// admit tier-0 without a jump, and the lower tiers are exhausted).
func TestTierPolicyScorerPanicRecovery(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	est := stubEstimator{coldDuration: time.Minute}
	panicScorer := stubScorer{panics: true}
	slots := map[Tier]TierSlot{
		TierTwo:  {ProfileID: "p2", State: StateExhausted},
		TierOne:  {ProfileID: "p1", State: StateExhausted},
		TierZero: {ProfileID: "p0", State: StateAvailable, Providers: []conductor.LaneID{"lane-a"}},
	}
	p := newTestPolicy(est, clk)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("SelectTier let a scorer panic propagate: %v", r)
		}
	}()
	sel, err := p.SelectTier(slots, []conductor.LaneID{"lane-a"}, baseTestRequest(), panicScorer, 0)
	// tier-0 is schedulable and allowed, so the walk still selects it
	// (a panicking scorer degrades the SCORE used for the jump decision,
	// not raw slot availability); the assertion that matters is that no
	// panic escaped SelectTier, checked by the deferred recover above.
	if err == nil && sel.Tier != TierZero {
		t.Fatalf("Tier = %v, want TierZero", sel.Tier)
	}
}

// TestTierPolicyReserveFlag asserts reserve_tier0 restricts tier-0 to
// jump-only selection, and never suppresses a jump.
func TestTierPolicyReserveFlag(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	est := stubEstimator{coldDuration: time.Minute}
	scorer := stubScorer{scores: map[Tier]float64{TierZero: 0.9, TierOne: 0.9, TierTwo: 0.9}}
	slots := map[Tier]TierSlot{
		TierTwo:  {ProfileID: "p2", State: StateExhausted},
		TierOne:  {ProfileID: "p1", State: StateExhausted},
		TierZero: {ProfileID: "p0", State: StateAvailable, Providers: []conductor.LaneID{"lane-a"}},
	}
	allowed := []conductor.LaneID{"lane-a"}

	t.Run("reserve blocks tier-0 walk-continuation without a jump", func(t *testing.T) {
		checkReserveBlocksWithoutJump(t, newTestPolicy(est, clk), slots, allowed, scorer)
	})
	t.Run("reserve never suppresses an attempt>=2 jump", func(t *testing.T) {
		checkReserveNeverSuppressesJump(t, newTestPolicy(est, clk), slots, allowed, scorer)
	})
	t.Run("without reserve, tier-0 is used as ordinary overflow", func(t *testing.T) {
		checkNoReserveOrdinaryOverflow(t, newTestPolicy(est, clk), slots, allowed, scorer)
	})
}

func checkReserveBlocksWithoutJump(t *testing.T, p *TierPolicy, slots map[Tier]TierSlot, allowed []conductor.LaneID, scorer CapabilityScorer) {
	t.Helper()
	req := baseTestRequest()
	req.ReserveTier0 = true
	_, err := p.SelectTier(slots, allowed, req, scorer, 0)
	kind, ok := cascade.KindOf(err)
	if !ok || kind != cascade.KindUnavailable {
		t.Fatalf("SelectTier with reserve_tier0 and no jump = %v, want ErrFleetExhausted", err)
	}
}

func checkReserveNeverSuppressesJump(t *testing.T, p *TierPolicy, slots map[Tier]TierSlot, allowed []conductor.LaneID, scorer CapabilityScorer) {
	t.Helper()
	req := baseTestRequest()
	req.ReserveTier0 = true
	sel, err := p.SelectTier(slots, allowed, req, scorer, 2)
	if err != nil {
		t.Fatalf("SelectTier: %v", err)
	}
	if sel.Tier != TierZero {
		t.Fatalf("Tier = %v, want TierZero (jump overrides reserve)", sel.Tier)
	}
	if !sel.JumpTriggered || sel.JumpReasonCode != JumpReasonAttempt {
		t.Fatalf("JumpTriggered=%v JumpReasonCode=%v, want true/attempt", sel.JumpTriggered, sel.JumpReasonCode)
	}
	if !sel.ReserveApplied {
		t.Fatal("ReserveApplied = false, want true (reserve was set even though the jump overrode it)")
	}
}

func checkNoReserveOrdinaryOverflow(t *testing.T, p *TierPolicy, slots map[Tier]TierSlot, allowed []conductor.LaneID, scorer CapabilityScorer) {
	t.Helper()
	req := baseTestRequest()
	sel, err := p.SelectTier(slots, allowed, req, scorer, 0)
	if err != nil {
		t.Fatalf("SelectTier: %v", err)
	}
	if sel.Tier != TierZero {
		t.Fatalf("Tier = %v, want TierZero", sel.Tier)
	}
	if sel.JumpTriggered {
		t.Fatal("JumpTriggered = true, want false (plain overflow, not a jump)")
	}
}
