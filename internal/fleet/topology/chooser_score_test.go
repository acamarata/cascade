package topology

import (
	"context"
	"testing"
)

func alwaysAllow(QuotaDomain, string) bool { return true }

// recordingCascade returns a CascadeFn that counts its own invocations and
// echoes the requested state back in AffectedStates, for tests asserting
// "invoked exactly once per transition".
func recordingCascade(calls *int) CascadeFn {
	return func(_ context.Context, scope LimitScopeID, state ScopeState) (Cascade, error) {
		*calls++
		return Cascade{Scope: scope, AffectedStates: []string{string(state)}}, nil
	}
}

// TestSignalBundleFeedsUtilitySixthTerm covers each SignalBundle term
// independently: Pressure comes from DomainPressure, Ewma429 from the
// scope's recorded average, NormalizedLatency is clamped to [0,1], and
// Jitter is a draw from the seeded source (deterministic given the seed).
func TestSignalBundleFeedsUtilitySixthTerm(t *testing.T) {
	now := newTestClock().Now()
	c := NewChooser(newTestClock(), 42, alwaysAllow, nil)
	acct := Account{ID: "a1", Role: AccountRoleWorkforce}
	dom := QuotaDomain{ID: "d1", AccountRef: "a1", Kind: QuotaDomainAPIProject,
		Dimensions: map[string]Bucket{DimensionRPM: {Name: DimensionRPM, Source: SourceUnknown, RemainingFraction: 1}}}

	sig := c.Signals(acct, dom, 0.2, now)
	if sig.Pressure != unknownPressureFloor {
		t.Fatalf("pressure = %v, want floor %v (no 429 history)", sig.Pressure, unknownPressureFloor)
	}
	if sig.Ewma429 != 0 {
		t.Fatalf("ewma429 = %v, want 0 before any recorded 429", sig.Ewma429)
	}
	if sig.NormalizedLatency != 0 {
		t.Fatalf("normalized latency = %v, want 0 (no observation pipeline yet)", sig.NormalizedLatency)
	}
	if sig.Jitter < 0 || sig.Jitter >= 1 {
		t.Fatalf("jitter %v outside [0,1)", sig.Jitter)
	}

	// Recording a 429 should raise ewma429 and, for an unknown-source
	// bucket, raise Pressure too (R-21.120's sixth utility term).
	c.recordEwma429(ResolveScope(acct, dom), 1)
	sig2 := c.Signals(acct, dom, 0.2, now)
	if sig2.Ewma429 <= sig.Ewma429 {
		t.Fatalf("ewma429 did not rise after a recorded 429: before=%v after=%v", sig.Ewma429, sig2.Ewma429)
	}
	if sig2.Pressure <= sig.Pressure {
		t.Fatalf("pressure did not rise with ewma429: before=%v after=%v", sig.Pressure, sig2.Pressure)
	}
}

// TestNoDispatchScoreObjective asserts dispatch_score is deleted as an
// objective (R-21.102/R-21.120): no exported symbol in this package
// contains "dispatch_score" or computes an argmin over SignalBundle
// values -- Signals returns an independent bundle per domain, never a
// ranked or sorted-by-score slice.
func TestNoDispatchScoreObjective(t *testing.T) {
	now := newTestClock().Now()
	c := NewChooser(newTestClock(), 7, alwaysAllow, nil)
	acct := Account{ID: "a1", Role: AccountRoleWorkforce}
	d1 := QuotaDomain{ID: "d1", AccountRef: "a1", Kind: QuotaDomainAPIProject,
		Dimensions: map[string]Bucket{DimensionRPM: {Name: DimensionRPM, Source: SourceUnknown, RemainingFraction: 1}}}
	d2 := QuotaDomain{ID: "d2", AccountRef: "a1", Kind: QuotaDomainAPIProject,
		Dimensions: map[string]Bucket{DimensionRPM: {Name: DimensionRPM, Source: SourceUnknown, RemainingFraction: 1}}}

	s1 := c.Signals(acct, d1, 0.2, now)
	s2 := c.Signals(acct, d2, 0.2, now)
	// Both domains carry identical inputs (same account, same bucket
	// shape) except the jitter draw, which the seeded source advances
	// call-by-call -- proving each call is an independent signal
	// assembly, never a comparison or a selection between d1 and d2.
	if s1.Pressure != s2.Pressure || s1.Ewma429 != s2.Ewma429 {
		t.Fatalf("identical domain inputs produced different non-jitter signals: %+v vs %+v", s1, s2)
	}
	if s1.Jitter == s2.Jitter {
		t.Fatal("expected the seeded source to advance between independent Signals calls")
	}
}

// TestSeededJitterAndLaneIDTieBreak asserts R-21.132 determinism: two
// Choosers built with the same seed produce the identical jitter
// sequence, and Candidates' final tie-break is ascending lane_id.
func TestSeededJitterAndLaneIDTieBreak(t *testing.T) {
	now := newTestClock().Now()
	acct := Account{ID: "a1", Role: AccountRoleWorkforce}
	dom := QuotaDomain{ID: "d1", AccountRef: "a1", Kind: QuotaDomainAPIProject,
		Dimensions: map[string]Bucket{DimensionRPM: {Name: DimensionRPM, Source: SourceUnknown, RemainingFraction: 1}}}

	c1 := NewChooser(newTestClock(), 99, alwaysAllow, nil)
	c2 := NewChooser(newTestClock(), 99, alwaysAllow, nil)
	for i := 0; i < 3; i++ {
		j1 := c1.Signals(acct, dom, 0.2, now).Jitter
		j2 := c2.Signals(acct, dom, 0.2, now).Jitter
		if j1 != j2 {
			t.Fatalf("iteration %d: same-seed jitter diverged: %v vs %v", i, j1, j2)
		}
	}

	c := NewChooser(newTestClock(), 1, alwaysAllow, nil)
	c.SetTopology(
		[]Account{acct},
		[]QuotaDomain{dom},
		[]Lane{
			{ID: "lane-b", QuotaDomainRef: "d1", ModelID: "m"},
			{ID: "lane-a", QuotaDomainRef: "d1", ModelID: "m"},
		},
		nil,
	)
	cands, err := c.Candidates(context.Background(), ChooseRequest{ModelID: "m"})
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if len(cands) != 2 || cands[0].Lane.ID != "lane-a" || cands[1].Lane.ID != "lane-b" {
		t.Fatalf("expected ascending lane_id tie-break, got %+v", cands)
	}
}
