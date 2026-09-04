package policy

// Purpose: fidelity — the property the whole ticket exists for. Every row
//   of the matrix is driven through the LIVE Engine.Evaluate on one stack
//   and through Simulate on an identically seeded second stack, and the
//   decisions must agree: verdict, rung, deciding layer, auto-advance,
//   explanation, and the identity of any error. The refusal paths are in
//   the matrix, including the two the approval queue owns.
// Constraints: the two stacks are separate real databases, because the
//   live run WRITES and would otherwise change the state the simulated run
//   reads. Both are seeded by the same code, so any divergence is the
//   simulator's and not the fixture's.
// SPORT: internal/policy Engine/CHANGED (P1-E09-W2-S18-T4).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
)

// fidelityCase is one row of the matrix.
type fidelityCase struct {
	name string
	in   DryRunInput
	// fill queues n real approvals on both stacks before the comparison,
	// so a case can reach a state the queue only refuses when it is busy.
	fill int
}

// fidelityMatrix spans every canonical verdict, both fail-closed inputs,
// both terminal denies, the elevation-class refusal and the queue-full
// refusal.
func fidelityMatrix() []fidelityCase {
	elevated := simInput(approvalCap().Name, L2)
	elevated.Request.Verb = "vault.get"
	stranger := simInput(approvalCap().Name, L2)
	stranger.Request.Subject = Subject{Kind: SubjectAgent, ID: "lane-z"}
	nobody := simInput(readCap().Name, L0)
	nobody.Request.Subject = Subject{}
	return []fidelityCase{
		{name: "allow: a granted read", in: simInput(readCap().Name, L0)},
		{name: "ask: a workspace mutation", in: simInput(approvalCap().Name, L2)},
		{name: "deny: a destructive capability", in: simInput(destructiveCap().Name, L0)},
		{name: "fail closed: the level was never set", in: simInput(readCap().Name, 0)},
		{name: "fail closed: the level is out of range", in: simInput(readCap().Name, RiskLevel(200))},
		{name: "terminal deny: the capability is not registered", in: simInput("memory.forge", L0)},
		{name: "terminal deny: the subject names nobody", in: nobody},
		{name: "refusal: an elevation-class verb is local only", in: elevated},
		{name: "a subject holding no grant", in: stranger},
		{name: "refusal: the queue is full", in: simInput(approvalCap().Name, L2), fill: 24},
	}
}

// seedFidelity builds one stack and puts the fixture subject's grant in
// place, so the standing-grant layer is reachable on both sides.
func seedFidelity(t *testing.T, fill int) *dryRunFixture {
	t.Helper()
	ctx := context.Background()
	f := newDryRunFixture(t)
	if err := f.grants.Grant(ctx, Grant{
		Subject: testSubject(), Capability: readCap().Name, ScopeClass: corpus.VisibilityShared,
	}); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	for i := 0; i < fill; i++ {
		if _, err := f.queue.Enqueue(ctx, askRequest(fmt.Sprintf("fill-%02d.txt", i))); err != nil {
			t.Fatalf("filling the queue at %d: %v", i, err)
		}
	}
	return f
}

// TestDryRunFidelityMatchesLivePath is the assertion that the simulator is
// not a second implementation: it agrees with the live engine on every row,
// refusals included.
func TestDryRunFidelityMatchesLivePath(t *testing.T) {
	ctx := context.Background()
	for _, tc := range fidelityMatrix() {
		live := seedFidelity(t, tc.fill)
		sim := seedFidelity(t, tc.fill)

		want, wantErr := live.engine.Evaluate(ctx, tc.in.Request)
		got, gotErr := sim.engine.Simulate(ctx, tc.in)

		if (wantErr == nil) != (gotErr == nil) || (wantErr != nil && !errors.Is(gotErr, wantErr)) {
			t.Errorf("%s: error = %v, live = %v", tc.name, gotErr, wantErr)
		}
		if got.Verdict != safeVerdict(want.Verdict) {
			t.Errorf("%s: Verdict = %s, live = %s", tc.name, got.Verdict, want.Verdict)
		}
		if got.RiskLevel != safeLevel(want.Level) {
			t.Errorf("%s: RiskLevel = %s, live = %s", tc.name, got.RiskLevel, want.Level)
		}
		if got.MatchedRule != want.Layer.String() {
			t.Errorf("%s: MatchedRule = %q, live = %q", tc.name, got.MatchedRule, want.Layer)
		}
		if got.AutoAdvance != want.AutoAdvance {
			t.Errorf("%s: AutoAdvance = %v, live = %v", tc.name, got.AutoAdvance, want.AutoAdvance)
		}
		if got.Explanation != want.Reason {
			t.Errorf("%s: Explanation = %q, live = %q", tc.name, got.Explanation, want.Reason)
		}
	}
}

// TestDryRunQuotesNoApprovalIDForAnEntryThatDoesNotExist pins the ONE
// deliberate difference between a report and a live outcome: the live path
// files an entry and quotes its request id, and a simulation files
// nothing, so there is no id to quote. Minting one would hand the caller a
// reference to an approval nobody can ever answer.
func TestDryRunQuotesNoApprovalIDForAnEntryThatDoesNotExist(t *testing.T) {
	ctx := context.Background()
	live := seedFidelity(t, 0)
	out, err := live.engine.Evaluate(ctx, simInput(approvalCap().Name, L2).Request)
	if err != nil || out.Verdict != VerdictAsk {
		t.Fatalf("the live ask = %+v, %v", out, err)
	}
	if out.ApprovalRequestID == "" {
		t.Fatal("the live path filed no request id, so there is no difference to pin")
	}
	if got := len(live.pending(t)); got != 1 {
		t.Fatalf("the live path left %d pending entries, want 1", got)
	}

	sim := seedFidelity(t, 0)
	if res := sim.simulate(t, simInput(approvalCap().Name, L2)); res.Verdict != VerdictAsk {
		t.Fatalf("the simulated ask = %+v", res)
	}
	if got := len(sim.pending(t)); got != 0 {
		t.Fatalf("the simulation left %d pending entries, want none", got)
	}
}

// TestDryRunIsDeterministic proves identical inputs produce identical
// reports, byte for byte in rendered form. A report that varied run to run
// could not be diffed, and a diff is what a caller uses it for.
func TestDryRunIsDeterministic(t *testing.T) {
	f := seedFidelity(t, 0)
	for _, in := range []DryRunInput{
		simInput(readCap().Name, L0),
		simInput(approvalCap().Name, L2),
		simInput(destructiveCap().Name, L0),
	} {
		in.SensitivityOverride = corpus.VisibilityShared
		first := f.simulate(t, in)
		wire, err := json.Marshal(first)
		if err != nil {
			t.Fatalf("rendering the report: %v", err)
		}
		for i := 0; i < 8; i++ {
			again := f.simulate(t, in)
			if !reflect.DeepEqual(first, again) {
				t.Fatalf("%s run %d: %+v, first run %+v", in.Request.Capability, i, again, first)
			}
			repeat, mErr := json.Marshal(again)
			if mErr != nil {
				t.Fatalf("rendering the report: %v", mErr)
			}
			if string(repeat) != string(wire) {
				t.Fatalf("%s run %d rendered %s, first run %s", in.Request.Capability, i, repeat, wire)
			}
		}
	}
}

// TestDryRunSurvivesAClosedStore covers the "closed engine" path: with the
// database shut, the grant store cannot answer, and the simulation must
// still agree with the live path — which falls through to the autonomy
// default rather than releasing anything.
func TestDryRunSurvivesAClosedStore(t *testing.T) {
	ctx := context.Background()
	live := seedFidelity(t, 0)
	sim := seedFidelity(t, 0)
	if err := live.db.Close(); err != nil {
		t.Fatalf("closing the live store: %v", err)
	}
	if err := sim.db.Close(); err != nil {
		t.Fatalf("closing the simulated store: %v", err)
	}
	want, _ := live.engine.Evaluate(ctx, simInput(readCap().Name, L0).Request)
	got, err := sim.engine.Simulate(ctx, simInput(readCap().Name, L0))
	if err != nil {
		t.Fatalf("Simulate over a closed store: %v", err)
	}
	if got.Verdict != want.Verdict || got.RiskLevel != want.Level {
		t.Errorf("report = %+v, live = %+v", got, want)
	}
	if len(got.ApplicableGrants) != 0 {
		t.Errorf("a report built from an unreadable store named %d grants", len(got.ApplicableGrants))
	}
}
