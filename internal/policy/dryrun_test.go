package policy

// Purpose: the dry-run simulator's verdict contract — every canonical
//   verdict (R-14.27) plus the separate ElevationRequired flag, the two
//   §5.15/§5.16 fail-closed guarantees. The error paths are next door in
//   dryrun_errors_test.go.
// Constraints: the fixture is the REAL stack (Art.2) — providers/sqlite on
//   a t.TempDir file, the real StoreGrants, the real StoreApprovals and the
//   real audit.Log — because a simulator asserted against doubles proves
//   fidelity to the doubles. Art.7.3: the clock is frozen, never the wall
//   clock.
// SPORT: internal/policy DryRunInput/ADDED, DryRunResult/ADDED,
//   GrantRef/ADDED (P1-E09-W2-S18-T4).

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/providers/sqlite"
)

// --- fixture --------------------------------------------------------------

// dryRunFixture is an engine with every real collaborator attached, over
// one real database file.
type dryRunFixture struct {
	db     *sqlite.Driver
	reg    *MemoryRegistry
	grants *StoreGrants
	clock  *testkit.FrozenClock
	log    *audit.Log
	queue  *StoreApprovals
	ctrl   *Controller
	engine *Engine
}

// newDryRunFixture builds the stack: three registered capabilities on the
// L0, L2 and L4 rungs, the §5.15 balanced profile, a real approval queue
// writing to a real audit log.
func newDryRunFixture(t *testing.T) *dryRunFixture {
	t.Helper()
	ctx := context.Background()
	f := &dryRunFixture{clock: testkit.NewFrozenClock(baseTime), reg: NewMemoryRegistry()}
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "cascade.db"))
	if err != nil {
		t.Fatalf("opening the real SQLite store: %v", err)
	}
	f.db = db
	t.Cleanup(func() { _ = db.Close() })
	for _, c := range []Capability{readCap(), approvalCap(), destructiveCap()} {
		if addErr := f.reg.Add(ctx, c); addErr != nil {
			t.Fatalf("registering %s: %v", c.Name, addErr)
		}
	}
	if f.grants, err = NewStoreGrants(db, f.reg, f.clock); err != nil {
		t.Fatalf("NewStoreGrants: %v", err)
	}
	f.log = audit.New(db, f.clock, nil)
	f.queue, err = NewApprovalQueue(ApprovalQueueConfig{
		Store: db, Registry: f.reg, Grants: f.grants, Clock: f.clock,
		Batching: ApprovalBatching{WindowSeconds: 10, Cap: 3}, Recorder: f.log,
	})
	if err != nil {
		t.Fatalf("NewApprovalQueue: %v", err)
	}
	f.ctrl = NewController(nil)
	f.ctrl.profile.Store(balancedProfile())
	eng, err := NewEngine(f.reg, f.grants, f.ctrl)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	f.engine = eng.WithApprovalQueue(f.queue)
	return f
}

// simInput is the canonical simulation request for capability at level.
func simInput(capability string, level RiskLevel) DryRunInput {
	return DryRunInput{Request: EvalRequest{
		Subject:    testSubject(),
		Capability: capability,
		Level:      level,
		Action:     "write a.txt",
		Params:     []byte(`{"path":"a.txt"}`),
		Summary:    "write a.txt",
	}}
}

// simulate runs one simulation, failing the test on an unexpected error.
func (f *dryRunFixture) simulate(t *testing.T, in DryRunInput) DryRunResult {
	t.Helper()
	res, err := f.engine.Simulate(context.Background(), in)
	if err != nil {
		t.Fatalf("Simulate(%s): %v", in.Request.Capability, err)
	}
	return res
}

// --- the canonical verdicts ----------------------------------------------

// TestDryRunVerdictAllow covers the allow rung: a read the balanced
// profile permits, reported with the layer that decided it.
func TestDryRunVerdictAllow(t *testing.T) {
	f := newDryRunFixture(t)
	res := f.simulate(t, simInput(readCap().Name, L0))
	if res.Verdict != VerdictAllow {
		t.Fatalf("Verdict = %s, want allow (%+v)", res.Verdict, res)
	}
	if res.RiskLevel != L0 || res.MatchedRule != LayerAutonomyProfile.String() {
		t.Errorf("report = %+v, want L0 decided by the autonomy profile", res)
	}
	if !res.AutoAdvance {
		t.Error("a permitted L0 read did not report auto-advance")
	}
	if res.ElevationRequired {
		t.Error("a capability with no verb reported elevation")
	}
	if res.WouldEmitAudit {
		t.Error("an allow verdict would emit no audit row, but the report says it would")
	}
	if res.Explanation == "" {
		t.Error("the report carries no explanation")
	}
}

// TestDryRunVerdictAsk covers the ask rung, which is the only verdict that
// reaches the approval queue at all, and the WouldEmitAudit flag that goes
// with it.
func TestDryRunVerdictAsk(t *testing.T) {
	f := newDryRunFixture(t)
	res := f.simulate(t, simInput(approvalCap().Name, L2))
	if res.Verdict != VerdictAsk || res.RiskLevel != L2 {
		t.Fatalf("report = %+v, want an ask at L2", res)
	}
	if !res.WouldEmitAudit {
		t.Error("the live path would have written an approval.enqueue row; the report denies it")
	}
	if res.AutoAdvance {
		t.Error("an ask verdict auto-advanced")
	}
}

// TestDryRunVerdictDeny covers the deny rung, including the one-directional
// fold: a capability whose own class is destructive raises an L0 request to
// L4 rather than being lowered to it.
func TestDryRunVerdictDeny(t *testing.T) {
	f := newDryRunFixture(t)
	res := f.simulate(t, simInput(destructiveCap().Name, L0))
	if res.Verdict != VerdictDeny || res.RiskLevel != L4 {
		t.Fatalf("report = %+v, want a deny at L4", res)
	}
	if res.MatchedRule != LayerCapabilityDefault.String() {
		t.Errorf("MatchedRule = %q, want the capability-default layer", res.MatchedRule)
	}
	if res.WouldEmitAudit {
		t.Error("a deny reached the queue")
	}
}

// TestDryRunElevationRequired proves ElevationRequired is a FLAG and not a
// verdict (R-14.27): the same elevation-class verb is reported alongside an
// allow on the read rung and alongside a deny at the ask rung, where the
// queue refuses it as local-only.
func TestDryRunElevationRequired(t *testing.T) {
	f := newDryRunFixture(t)

	read := simInput(readCap().Name, L0)
	read.Request.Verb = "vault.get"
	allowed := f.simulate(t, read)
	if !allowed.ElevationRequired || allowed.Verdict != VerdictAllow {
		t.Errorf("report = %+v, want an allow that still flags elevation", allowed)
	}

	ask := simInput(approvalCap().Name, L2)
	ask.Request.Verb = "vault.get"
	refused := f.simulate(t, ask)
	if !refused.ElevationRequired {
		t.Error("an elevation-class verb was not flagged at the ask rung")
	}
	if refused.Verdict != VerdictDeny {
		t.Errorf("Verdict = %s, want the deny a local-only queue refusal produces", refused.Verdict)
	}
	if refused.WouldEmitAudit {
		t.Error("a refused enqueue reported that it would write an audit row")
	}
}

// --- fail-closed ----------------------------------------------------------

// TestDryRunFailClosedClassification covers §5.15: an action whose class
// could not be resolved lands on L4 and denies, and does so from the
// enum's own fail-closed mapping rather than from a permissive zero value.
func TestDryRunFailClosedClassification(t *testing.T) {
	f := newDryRunFixture(t)
	cases := []struct {
		name  string
		level RiskLevel
	}{
		{"unset level (the classifier could not parse the action)", 0},
		{"out-of-range level", RiskLevel(200)},
	}
	for _, tc := range cases {
		name, level := tc.name, tc.level
		res := f.simulate(t, simInput(readCap().Name, level))
		if res.Verdict != VerdictDeny || res.RiskLevel != L4 {
			t.Errorf("%s: report = %+v, want a deny at L4", name, res)
		}
		if res.AutoAdvance {
			t.Errorf("%s: an unclassifiable action auto-advanced", name)
		}
	}
	// An unregistered capability is the other unclassifiable input, and it
	// is a terminal deny that carries an error and names no grants.
	res, err := f.engine.Simulate(context.Background(),
		simInput("memory.forge", L0))
	if !errors.Is(err, ErrCapabilityNotFound) {
		t.Fatalf("error = %v, want ErrCapabilityNotFound", err)
	}
	if res.Verdict != VerdictDeny || res.RiskLevel != L4 || res.ApplicableGrants != nil {
		t.Errorf("report = %+v, want a bare L4 deny naming no grants", res)
	}
}

// TestDryRunFailClosedSensitivity covers §5.16: an unresolvable tier is the
// restricted one, and no tier and no grant combination widens the reported
// reach beyond what the grant itself allows.
func TestDryRunFailClosedSensitivity(t *testing.T) {
	ctx := context.Background()
	f := newDryRunFixture(t)
	if err := f.grants.Grant(ctx, Grant{
		Subject: testSubject(), Capability: readCap().Name, ScopeClass: corpus.VisibilityShared,
	}); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	cases := []struct {
		name     string
		override corpus.VisibilityClass
		want     corpus.VisibilityClass
	}{
		{"unset tier is unresolvable, so restricted", "", corpus.VisibilityPrivate},
		{"unrecognised tier is unresolvable, so restricted", "world", corpus.VisibilityPrivate},
		{"a narrower tier narrows", corpus.VisibilityScopeLocal, corpus.VisibilityScopeLocal},
		{"a tier at the grant's own reach holds", corpus.VisibilityShared, corpus.VisibilityShared},
		{"a wider tier cannot widen the grant", corpus.VisibilityTeam, corpus.VisibilityShared},
	}
	for _, tc := range cases {
		in := simInput(readCap().Name, L0)
		in.SensitivityOverride = tc.override
		res := f.simulate(t, in)
		if res.EffectiveScope != tc.want {
			t.Errorf("%s: EffectiveScope = %q, want %q", tc.name, res.EffectiveScope, tc.want)
		}
	}
}
