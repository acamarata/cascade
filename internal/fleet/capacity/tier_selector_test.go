// Purpose: the interim Selector picks the tier through TierPolicy.SelectTier
// and the first schedulable lane of that tier in LaneID order, and never
// re-implements the walk.
//
// SPORT: fleet.capacity.lease.

package capacity

import (
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
)

func selectorFixture() (Selector, *fakeClock) {
	clock := &fakeClock{t: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
	return NewTierPolicySelector(newTestPolicy(stubEstimator{coldDuration: time.Minute}, clock)), clock
}

func laneSlot(id string, tier Tier, state State, at time.Time) ProviderSlot {
	return ProviderSlot{LaneID: conductor.LaneID(id), Tier: tier, State: state, ProfileRef: id, UpdatedAt: at}
}

func TestTierPolicySelectorPicksFirstSchedulableLaneOfTheTier(t *testing.T) {
	sel, clock := selectorFixture()
	snap := FleetSnapshot{Lanes: map[conductor.LaneID]ProviderSlot{
		"a-exhausted": laneSlot("a-exhausted", TierTwo, StateExhausted, clock.t),
		"b-constr":    laneSlot("b-constr", TierTwo, StateConstrained, clock.t),
		"c-avail":     laneSlot("c-avail", TierTwo, StateAvailable, clock.t),
	}}
	in := SelectInput{Snapshot: snap, Eligible: []conductor.LaneID{"c-avail", "a-exhausted", "b-constr"}, Request: baseTestRequest(),
		Scorer: stubScorer{scores: map[Tier]float64{TierZero: 0.9, TierTwo: 0.85}}, Attempt: 1}
	got, err := sel.Select(t.Context(), in)
	if err != nil || got.Lane != "b-constr" || got.Tier.Tier != TierTwo || got.Score != 0.85 {
		t.Fatalf("Select = (%+v, %v), want tier-2 on b-constr with score 0.85", got, err)
	}
	if in.Eligible[0] != "c-avail" {
		t.Fatalf("Eligible was reordered to %v: a Selector must not mutate its input", in.Eligible)
	}
}

func TestTierPolicySelectorPropagatesExhaustion(t *testing.T) {
	sel, clock := selectorFixture()
	snap := FleetSnapshot{Lanes: map[conductor.LaneID]ProviderSlot{"x": laneSlot("x", TierTwo, StateExhausted, clock.t)}}
	in := SelectInput{Snapshot: snap, Eligible: []conductor.LaneID{"x"}, Request: baseTestRequest(), Scorer: stubScorer{}, Attempt: 1}
	if _, err := sel.Select(t.Context(), in); !hasSentinel(err, ErrFleetExhausted) {
		t.Fatalf("Select(all exhausted) = %v, want ErrFleetExhausted by identity", err)
	}
}

func TestTierPolicySelectorNeverProbesAnUnknownLane(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
	probes := NewDefaultProbeAdmission()
	sel := NewTierPolicySelector(NewTierPolicy(stubEstimator{coldDuration: time.Minute}, probes, clock))
	snap := FleetSnapshot{Lanes: map[conductor.LaneID]ProviderSlot{"p": laneSlot("p", TierTwo, StateUnknown, clock.t)}}
	in := SelectInput{Snapshot: snap, Eligible: []conductor.LaneID{"p"}, Request: baseTestRequest(), Scorer: stubScorer{scores: map[Tier]float64{TierZero: 0.9}}, Attempt: 1}
	if got, err := sel.Select(t.Context(), in); !hasSentinel(err, ErrFleetExhausted) {
		t.Fatalf("Select = (%+v, %v), want ErrFleetExhausted: an unknown lane is not leased", got, err)
	}
	if !probes.TryAdmit("p", true, clock.t) {
		t.Fatal("the refused Select left a probe in flight on p: a later probe is refused for good")
	}
	// A status refresh later makes the lane available; the earlier refusal left nothing behind.
	in.Snapshot = FleetSnapshot{Lanes: map[conductor.LaneID]ProviderSlot{"p": laneSlot("p", TierTwo, StateAvailable, clock.t)}}
	if got, err := sel.Select(t.Context(), in); err != nil || got.Lane != "p" || got.Tier.Probe {
		t.Fatalf("Select after the refresh = (%+v, %v), want p without a probe", got, err)
	}
}

func TestFirstLaneOfTierRefusesWhenNoLaneFits(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
	snap := FleetSnapshot{Lanes: map[conductor.LaneID]ProviderSlot{"x": laneSlot("x", TierOne, StateAvailable, clock.t)}}
	if lane, ok := firstLaneOfTier(snap, []conductor.LaneID{"x", "missing"}, TierTwo); ok {
		t.Fatalf("firstLaneOfTier = %q, want none: no usable lane backs tier-2", lane)
	}
	if lane, ok := firstLaneOfTier(snap, []conductor.LaneID{"x"}, TierOne); !ok || lane != "x" {
		t.Fatalf("firstLaneOfTier = (%q, %v), want x", lane, ok)
	}
}

func TestTierPolicySelectorJudgesEachLaneOnItsOwnProfile(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
	probes := NewDefaultProbeAdmission()
	probes.Classify("a-blocked", StateExhausted, clock.t) // one-minute backoff on a-blocked only
	sel := NewTierPolicySelector(NewTierPolicy(stubEstimator{coldDuration: time.Minute}, probes, clock))
	snap := FleetSnapshot{Lanes: map[conductor.LaneID]ProviderSlot{
		"a-blocked": laneSlot("a-blocked", TierTwo, StateConstrained, clock.t),
		"b-stale":   laneSlot("b-stale", TierTwo, StateAvailable, clock.t.Add(-48*time.Hour)),
		"c-fresh":   laneSlot("c-fresh", TierTwo, StateAvailable, clock.t),
	}}
	in := SelectInput{Snapshot: snap, Eligible: []conductor.LaneID{"a-blocked", "b-stale", "c-fresh"}, Request: baseTestRequest(),
		Scorer: stubScorer{scores: map[Tier]float64{TierZero: 0.9, TierTwo: 0.85}}, Attempt: 1}
	got, err := sel.Select(t.Context(), in)
	if err != nil || got.Lane != "c-fresh" || got.Tier.Probe {
		t.Fatalf("Select = (%+v, %v), want c-fresh: a-blocked is in backoff and b-stale degraded to unknown", got, err)
	}
	delete(in.Snapshot.Lanes, "c-fresh")
	in.Eligible = []conductor.LaneID{"a-blocked", "b-stale"}
	if got, err := sel.Select(t.Context(), in); err == nil {
		t.Fatalf("Select = (%+v, nil), want a refusal: no lane is schedulable on its own profile", got)
	}
}

func TestTierPolicySelectorBackoffOnOneProfileKeepsTheHealthySibling(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
	probes := NewDefaultProbeAdmission()
	probes.Classify("a-blocked", StateExhausted, clock.t) // a-blocked sorts first and is available in the snapshot
	sel := NewTierPolicySelector(NewTierPolicy(stubEstimator{coldDuration: time.Minute}, probes, clock))
	snap := FleetSnapshot{Lanes: map[conductor.LaneID]ProviderSlot{
		"a-blocked": laneSlot("a-blocked", TierTwo, StateAvailable, clock.t),
		"z-healthy": laneSlot("z-healthy", TierTwo, StateAvailable, clock.t),
	}}
	in := SelectInput{Snapshot: snap, Eligible: []conductor.LaneID{"a-blocked", "z-healthy"}, Request: baseTestRequest(),
		Scorer: stubScorer{scores: map[Tier]float64{TierZero: 0.9, TierTwo: 0.85}}, Attempt: 1}
	if got, err := sel.Select(t.Context(), in); err != nil || got.Lane != "z-healthy" || got.Tier.Tier != TierTwo {
		t.Fatalf("Select = (%+v, %v), want tier-2 on z-healthy: only a-blocked is in backoff", got, err)
	}
}

// flipProbes answers the first TryAdmit of each profile from open and every later one from "now closed".
type flipProbes struct {
	ProbeAdmission
	closed map[string]bool
	asked  map[string]int
}

func (f *flipProbes) TryAdmit(profile string, _ bool, _ time.Time) bool {
	f.asked[profile]++
	return f.asked[profile] == 1 && !f.closed[profile]
}

func TestTierPolicySelectorBackoffThatMovesMidCallKeepsOneReading(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
	snap := FleetSnapshot{Lanes: map[conductor.LaneID]ProviderSlot{
		"a": laneSlot("a", TierTwo, StateAvailable, clock.t),
		"z": laneSlot("z", TierTwo, StateAvailable, clock.t),
	}}
	in := SelectInput{Snapshot: snap, Eligible: []conductor.LaneID{"a", "z"}, Request: baseTestRequest(),
		Scorer: stubScorer{scores: map[Tier]float64{TierZero: 0.9, TierTwo: 0.85}}, Attempt: 1}
	// Each profile admits once and then reads as backed off, as if another
	// goroutine classified it after the lane prefilter looked.
	probes := &flipProbes{closed: map[string]bool{}, asked: map[string]int{}}
	sel := NewTierPolicySelector(NewTierPolicy(stubEstimator{coldDuration: time.Minute}, probes, clock))
	if got, err := sel.Select(t.Context(), in); err != nil || got.Tier.Tier != TierTwo || got.Lane != "a" {
		t.Fatalf("Select = (%+v, %v), want tier-2 on a: the walk must keep the reading the prefilter took", got, err)
	}
	// With tier-0 reserved the same interleaving must not turn into a reserve refusal.
	probes = &flipProbes{closed: map[string]bool{}, asked: map[string]int{}}
	sel = NewTierPolicySelector(NewTierPolicy(stubEstimator{coldDuration: time.Minute}, probes, clock))
	in.Request.ReserveTier0 = true
	in.Snapshot.Lanes["t0"] = laneSlot("t0", TierZero, StateAvailable, clock.t)
	in.Eligible = []conductor.LaneID{"a", "t0", "z"}
	if got, err := sel.Select(t.Context(), in); err != nil || got.Tier.Tier != TierTwo {
		t.Fatalf("Select with reserve_tier0 = (%+v, %v), want tier-2: it is schedulable", got, err)
	}
}

// panicOnceScorer panics on its first ask and returns 0.9 afterwards.
type panicOnceScorer struct{ asked int }

func (p *panicOnceScorer) Score(Tier, conductor.TaskClass) float64 {
	p.asked++
	if p.asked == 1 {
		panic("scorer failed once")
	}
	return 0.9
}

func TestWalkViewKeepsTheRecoveredScoreOfAPanickingScorer(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
	v := newWalkView(newTestPolicy(stubEstimator{}, clock), &panicOnceScorer{})
	first := scoreSafe(v.scorer, TierZero, "code")
	if second := scoreSafe(v.scorer, TierZero, "code"); first != 0 || second != first {
		t.Fatalf("scores = %v then %v, want the recovered 0 both times: the walk and the decision must see one score", first, second)
	}
}

func reserveInput(clock *fakeClock, t0 State, attempt int) SelectInput {
	req := baseTestRequest()
	req.ReserveTier0 = true
	snap := FleetSnapshot{Lanes: map[conductor.LaneID]ProviderSlot{
		"t0": laneSlot("t0", TierZero, t0, clock.t),
		"t2": laneSlot("t2", TierTwo, StateExhausted, clock.t),
	}}
	return SelectInput{Snapshot: snap, Eligible: []conductor.LaneID{"t0", "t2"}, Request: req,
		Scorer: stubScorer{scores: map[Tier]float64{TierZero: 0.9}}, Attempt: attempt}
}

func TestTierPolicySelectorTier0ReservedCause(t *testing.T) {
	cases := []struct {
		name     string
		t0       State
		attempt  int
		backoff  bool
		want     error
		wantNone bool
	}{
		{name: "reserved and schedulable with no jump", t0: StateAvailable, attempt: 1, want: ErrTier0Reserved},
		{name: "tier-0 exhausted is plain exhaustion", t0: StateExhausted, attempt: 1, want: ErrFleetExhausted},
		{name: "a jump admits tier-0", t0: StateAvailable, attempt: 2, wantNone: true},
		{name: "a jump into a tier-0 in backoff is plain exhaustion", t0: StateAvailable, attempt: 2, backoff: true, want: ErrFleetExhausted},
		{name: "backoff alone is plain exhaustion", t0: StateAvailable, attempt: 1, backoff: true, want: ErrFleetExhausted},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clock := &fakeClock{t: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
			probes := NewDefaultProbeAdmission()
			if c.backoff {
				probes.Classify("t0", StateExhausted, clock.t)
			}
			sel := NewTierPolicySelector(NewTierPolicy(stubEstimator{coldDuration: time.Minute}, probes, clock))
			got, err := sel.Select(t.Context(), reserveInput(clock, c.t0, c.attempt))
			if c.wantNone {
				if err != nil || got.Tier.Tier != TierZero {
					t.Fatalf("Select = (%+v, %v), want tier-0 admitted by the jump", got, err)
				}
				return
			}
			requireSentinel(t, err, c.want)
			if c.want == ErrFleetExhausted && hasSentinel(err, ErrTier0Reserved) {
				t.Fatalf("err = %v, want plain exhaustion, not the reserve refusal", err)
			}
		})
	}
}

// movingClock advances on every read.
type movingClock struct {
	t    time.Time
	step time.Duration
}

func (c *movingClock) Now() time.Time { c.t = c.t.Add(c.step); return c.t }

func TestTierPolicySelectorTier0ReservedCauseIsFrozen(t *testing.T) {
	start := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	// Tier-0 was observed 10m-1ns before the walk reads the clock: fresh at the
	// walk, past the staleness limit by the next read. The cause must name the
	// walk's decision.
	clock := &movingClock{t: start.Add(-time.Nanosecond), step: 2 * time.Nanosecond}
	sel := NewTierPolicySelector(NewTierPolicy(stubEstimator{coldDuration: time.Minute}, NewDefaultProbeAdmission(), clock))
	in := reserveInput(&fakeClock{t: start.Add(-10*time.Minute + time.Nanosecond)}, StateAvailable, 1)
	_, err := sel.Select(t.Context(), in)
	requireSentinel(t, err, ErrTier0Reserved)

}

func TestWalkViewAnswersFromTheFirstRead(t *testing.T) {
	start := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	scorer := &countingScorer{}
	est := &countingEstimator{}
	v := newWalkView(&TierPolicy{Estimator: est, Probes: NewDefaultProbeAdmission(), Clock: &movingClock{t: start, step: time.Second}}, scorer)
	if v.now != start.Add(time.Second) || v.policy.Clock.Now() != v.now || v.policy.Clock.Now() != v.now {
		t.Fatalf("view time = %v, want one frozen read", v.now)
	}
	first := v.scorer.Score(TierZero, conductor.TaskClassCode)
	if again := v.scorer.Score(TierZero, conductor.TaskClassCode); again != first || scorer.calls != 1 {
		t.Fatalf("scorer asked %d times, answered %v then %v, want one ask", scorer.calls, first, again)
	}
	qw, de := v.policy.Estimator.Estimate(TierTwo, conductor.TaskClassCode)
	if qw2, de2 := v.policy.Estimator.Estimate(TierTwo, conductor.TaskClassCode); qw2 != qw || de2 != de || est.calls != 1 {
		t.Fatalf("estimator asked %d times, want one ask", est.calls)
	}
	if !v.probes.TryAdmit("p", false, v.now) {
		t.Fatal("first TryAdmit refused")
	}
	if !v.probes.TryAdmit("p", false, v.now) {
		t.Fatal("a repeat before the freeze must read the first answer")
	}
	v.freeze()
	if !v.probes.TryAdmit("p", false, v.now) || v.probes.TryAdmit("p", true, v.now) {
		t.Fatal("replay must repeat the recorded answer and refuse a question never asked")
	}
}

// countingScorer and countingEstimator return a new value on every real ask.
type countingScorer struct{ calls int }

func (c *countingScorer) Score(Tier, conductor.TaskClass) float64 {
	c.calls++
	return float64(c.calls) / 10
}

type countingEstimator struct{ calls int }

func (c *countingEstimator) Estimate(Tier, conductor.TaskClass) (time.Duration, time.Duration) {
	c.calls++
	return time.Duration(c.calls) * time.Second, time.Minute
}
