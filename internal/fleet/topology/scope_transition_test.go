package topology

import (
	"context"
	"testing"
	"time"
)

// TestScopeTransitionRollsBackHeldReservations asserts OnScopeTransition
// invokes the injected CascadeFn and returns its Cascade value, and that
// no reservation type or economics import exists in this package (the
// import check runs at the package-graph level; here we only assert the
// seam invokes exactly what was injected).
func TestScopeTransitionRollsBackHeldReservations(t *testing.T) {
	var calls int
	c := NewChooser(newTestClock(), 1, alwaysAllow, recordingCascade(&calls))
	casc, err := c.OnScopeTransition(context.Background(), "scope:a1", ScopeStateRateLimited, newTestClock().Now().Add(time.Minute))
	if err != nil {
		t.Fatalf("OnScopeTransition: %v", err)
	}
	if casc.Scope != "scope:a1" || len(casc.AffectedStates) != 1 || casc.AffectedStates[0] != string(ScopeStateRateLimited) {
		t.Fatalf("unexpected cascade: %+v", casc)
	}
	if calls != 1 {
		t.Fatalf("expected exactly one CascadeFn invocation, got %d", calls)
	}
}

// TestCommittedReservationsSurviveScopeTransition documents (via the
// CascadeFn seam) that this package never inspects or holds reservation
// state itself -- OnScopeTransition passes the transition through
// unconditionally and returns whatever the injected function reports;
// deciding which reservations are committed vs held is entirely
// AO/S-79.T4's reserve_cascade.go responsibility.
func TestCommittedReservationsSurviveScopeTransition(t *testing.T) {
	fn := func(_ context.Context, scope LimitScopeID, _ ScopeState) (Cascade, error) {
		// A real reserve_cascade.go would roll back held/parked and leave
		// committed reservations untouched; this fake reports that shape
		// so the test asserts the seam carries it through unmodified.
		return Cascade{Scope: scope, AffectedStates: []string{"held:r1:rolled_back", "committed:r2:untouched"}}, nil
	}
	c := NewChooser(newTestClock(), 1, alwaysAllow, fn)
	casc, err := c.OnScopeTransition(context.Background(), "scope:a1", ScopeStateRateLimited, time.Time{})
	if err != nil {
		t.Fatalf("OnScopeTransition: %v", err)
	}
	found := map[string]bool{}
	for _, s := range casc.AffectedStates {
		found[s] = true
	}
	if !found["held:r1:rolled_back"] || !found["committed:r2:untouched"] {
		t.Fatalf("cascade did not carry the injected function's report through: %+v", casc)
	}
}

// TestRateLimitedEmittedOncePerScope asserts a repeated OnScopeTransition
// call for the SAME (scope, state) invokes the injected CascadeFn only
// once -- "RATE_LIMITED emitted once per scope transition, never once per
// call" (R-21.126).
func TestRateLimitedEmittedOncePerScope(t *testing.T) {
	var calls int
	c := NewChooser(newTestClock(), 1, alwaysAllow, recordingCascade(&calls))

	if _, err := c.OnScopeTransition(context.Background(), "scope:a1", ScopeStateRateLimited, time.Time{}); err != nil {
		t.Fatalf("first transition: %v", err)
	}
	if _, err := c.OnScopeTransition(context.Background(), "scope:a1", ScopeStateRateLimited, time.Time{}); err != nil {
		t.Fatalf("repeat transition: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected exactly one CascadeFn invocation across two identical transitions, got %d", calls)
	}

	// A genuinely different transition (quarantined) DOES invoke it again.
	if _, err := c.OnScopeTransition(context.Background(), "scope:a1", ScopeStateQuarantined, time.Time{}); err != nil {
		t.Fatalf("second distinct transition: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected a genuinely new transition to invoke CascadeFn again, got %d calls", calls)
	}
}

// TestOnDomainQuarantinedDelegatesToScopeTransition asserts
// OnDomainQuarantined resolves the domain's scope and drives it to
// quarantined via the same OnScopeTransition path -- no second cascade
// implementation.
func TestOnDomainQuarantinedDelegatesToScopeTransition(t *testing.T) {
	var calls int
	acct := Account{ID: "a1", Role: AccountRoleWorkforce}
	dom := QuotaDomain{ID: "d1", AccountRef: "a1", Kind: QuotaDomainAPIProject}
	c := NewChooser(newTestClock(), 1, alwaysAllow, recordingCascade(&calls))
	c.SetTopology([]Account{acct}, []QuotaDomain{dom}, nil, nil)

	casc, err := c.OnDomainQuarantined(context.Background(), "d1")
	if err != nil {
		t.Fatalf("OnDomainQuarantined: %v", err)
	}
	if casc.Scope != ResolveScope(acct, dom) {
		t.Fatalf("cascade scope = %v, want %v", casc.Scope, ResolveScope(acct, dom))
	}
	if calls != 1 {
		t.Fatalf("expected exactly one CascadeFn invocation, got %d", calls)
	}
	if !c.domainQuarantined["d1"] {
		t.Fatal("expected the domain to be marked quarantined")
	}
}

// TestScopeTransitionFailsClosedWithoutCascadeFn asserts an unconfigured
// CascadeFn refuses rather than silently succeeding with no rollback.
func TestScopeTransitionFailsClosedWithoutCascadeFn(t *testing.T) {
	c := NewChooser(newTestClock(), 1, alwaysAllow, nil)
	if _, err := c.OnScopeTransition(context.Background(), "scope:a1", ScopeStateRateLimited, time.Time{}); err == nil {
		t.Fatal("expected OnScopeTransition to fail closed with no CascadeFn configured")
	}
}
