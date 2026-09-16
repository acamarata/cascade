package supervision

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose (this file): the mandatory first-run dry-run gate's contract —
//   one simulation per {account, autonomy-profile}, durable across
//   restarts, re-triggered by a profile change (R-14.56), and the THREE
//   failure directions R-14.247 §4 keeps apart because they are not the
//   same question.
//
// Why the directions are tested separately: the cheap reading is "anything
//   goes wrong, block". That is fail-closed on preference rather than on
//   authorization (R-14.245), and it would turn an unreadable store into a
//   denial-of-service on the operator's own autonomy setting. Each
//   direction below asserts what actually happens, not what a slogan would
//   predict.
//
// Constraints: no network, no sleeping; the store is in-memory and the
//   simulator is a recording double. The double records the request it was
//   handed, so "the guard passed the router's own request through" is
//   asserted rather than assumed.
// SPORT: fleet.supervision dry-run-first tests (ADD) — P1-E18-W4-S39-T4.

// recordingSimulator records every Simulate call and answers with a
// canned result.
type recordingSimulator struct {
	calls []policy.DryRunInput
	res   policy.DryRunResult
	err   error
}

func (s *recordingSimulator) Simulate(_ context.Context, in policy.DryRunInput) (policy.DryRunResult, error) {
	s.calls = append(s.calls, in)
	return s.res, s.err
}

// staticProfiles reports one profile, swappable mid-test to simulate a
// config reload.
type staticProfiles struct{ p *policy.AutonomyProfile }

func (s *staticProfiles) Profile() *policy.AutonomyProfile { return s.p }

// capturingAttention records every queued item.
type capturingAttention struct{ items []AttentionItem }

func (a *capturingAttention) Push(_ context.Context, item AttentionItem) (AttentionItem, error) {
	a.items = append(a.items, item)
	return item, nil
}

// capturingAudit records every appended event.
type capturingAudit struct{ events []audit.Event }

func (w *capturingAudit) Append(_ context.Context, e audit.Event) (audit.Record, error) {
	w.events = append(w.events, e)
	return audit.Record{}, nil
}

// readBrokenStore answers Get with a transport failure and Put normally,
// so the flag-READ direction can be tested on its own.
type readBrokenStore struct{ provider.Store }

func (s readBrokenStore) Get(context.Context, string, string) ([]byte, error) {
	return nil, cascade.New(cascade.KindUnavailable, "store: unreachable")
}

// writeBrokenStore answers Get normally and refuses Put, so the flag-WRITE
// direction can be tested on its own.
type writeBrokenStore struct{ provider.Store }

func (s writeBrokenStore) Put(context.Context, string, string, []byte) error {
	return cascade.New(cascade.KindUnavailable, "store: read-only")
}

// namedProfile resolves one of the builtin autonomy profiles.
func namedProfile(t *testing.T, name string) *policy.AutonomyProfile {
	t.Helper()
	p, err := policy.Resolve(policy.Config{ProfileName: name})
	if err != nil {
		t.Fatalf("resolve profile %q: %v", name, err)
	}
	return p
}

// guardFixture is one wired guard and everything that can be inspected
// after a call.
type guardFixture struct {
	guard     *DryRunFirstGuard
	sim       *recordingSimulator
	profiles  *staticProfiles
	attention *capturingAttention
	trace     *capturingAudit
	store     provider.Store
}

// newGuard wires a guard over an in-memory store, or over the store the
// caller supplies when a specific failure has to be staged.
func newGuard(t *testing.T, store provider.Store) guardFixture {
	t.Helper()
	if store == nil {
		store = storetest.NewMemStore()
	}
	flags, err := NewFirstRunFlags(store)
	if err != nil {
		t.Fatal(err)
	}
	fx := guardFixture{
		sim:       &recordingSimulator{res: policy.DryRunResult{Verdict: policy.VerdictAllow}},
		profiles:  &staticProfiles{p: namedProfile(t, "balanced")},
		attention: &capturingAttention{},
		trace:     &capturingAudit{},
		store:     store,
	}
	g, err := EnforceDryRunFirst(fx.sim, flags, fx.profiles, fx.attention, fx.trace)
	if err != nil {
		t.Fatal(err)
	}
	fx.guard = g
	return fx
}

// sampleRequest is the request the router would have built.
func sampleRequest() policy.EvalRequest {
	return policy.EvalRequest{
		Subject:    policy.Subject{Kind: policy.SubjectAgent, ID: "agent-7"},
		Capability: "hooks.shell",
		Action:     "rm -rf ./build",
		Attributes: map[string]string{"ref": "act-001"},
	}
}

// TestTheFirstAttemptIsSimulatedAndTheSecondIsNot is the whole point of the
// gate: exactly one simulation per pair, and the second attempt costs
// nothing.
func TestTheFirstAttemptIsSimulatedAndTheSecondIsNot(t *testing.T) {
	ctx := context.Background()
	fx := newGuard(t, nil)

	if err := fx.guard.Guard(ctx, sampleRequest()); err != nil {
		t.Fatalf("first attempt: %v", err)
	}
	if len(fx.sim.calls) != 1 {
		t.Fatalf("the first attempt ran %d simulations, want exactly 1", len(fx.sim.calls))
	}
	// The guard must hand the simulator the router's OWN request, not a
	// reconstruction: a rebuilt request is the reclassification R-21.236
	// forbids, and it would also silently simulate a different action.
	if got := fx.sim.calls[0].Request; got.Action != sampleRequest().Action ||
		got.Capability != sampleRequest().Capability || got.Subject != sampleRequest().Subject {
		t.Errorf("simulated %+v, want the request the router built verbatim", got)
	}

	if err := fx.guard.Guard(ctx, sampleRequest()); err != nil {
		t.Fatalf("second attempt: %v", err)
	}
	if len(fx.sim.calls) != 1 {
		t.Errorf("the second attempt ran %d simulations in total, want the first one only", len(fx.sim.calls))
	}
}

// TestTheFlagOutlivesTheProcess is the durability half. A guard rebuilt
// over the SAME store — which is what a daemon restart is — must not
// simulate again.
func TestTheFlagOutlivesTheProcess(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()

	first := newGuard(t, store)
	if err := first.guard.Guard(ctx, sampleRequest()); err != nil {
		t.Fatalf("before the restart: %v", err)
	}
	if len(first.sim.calls) != 1 {
		t.Fatalf("%d simulations before the restart, want 1", len(first.sim.calls))
	}

	restarted := newGuard(t, store)
	if err := restarted.guard.Guard(ctx, sampleRequest()); err != nil {
		t.Fatalf("after the restart: %v", err)
	}
	if len(restarted.sim.calls) != 0 {
		t.Errorf("the restarted guard simulated again; the flag did not survive the process")
	}
}

// TestAProfileChangeRetriggersTheDryRun is R-14.56. The flag is keyed per
// {account, autonomy-profile}, so an operator who loosens the profile has
// not carried the old profile's approval across to the new one.
func TestAProfileChangeRetriggersTheDryRun(t *testing.T) {
	ctx := context.Background()
	fx := newGuard(t, nil)

	if err := fx.guard.Guard(ctx, sampleRequest()); err != nil {
		t.Fatalf("under the first profile: %v", err)
	}
	if len(fx.sim.calls) != 1 {
		t.Fatalf("%d simulations under the first profile, want 1", len(fx.sim.calls))
	}

	fx.profiles.p = namedProfile(t, "strict")
	if err := fx.guard.Guard(ctx, sampleRequest()); err != nil {
		t.Fatalf("under the second profile: %v", err)
	}
	if len(fx.sim.calls) != 2 {
		t.Errorf("%d simulations after the profile changed, want a fresh one", len(fx.sim.calls))
	}
}

// TestADifferentAccountGetsItsOwnFirstRun is the other half of the key. One
// agent's completed dry run must not authorise another agent's first live
// action.
func TestADifferentAccountGetsItsOwnFirstRun(t *testing.T) {
	ctx := context.Background()
	fx := newGuard(t, nil)

	if err := fx.guard.Guard(ctx, sampleRequest()); err != nil {
		t.Fatal(err)
	}
	other := sampleRequest()
	other.Subject = policy.Subject{Kind: policy.SubjectAgent, ID: "agent-9"}
	if err := fx.guard.Guard(ctx, other); err != nil {
		t.Fatal(err)
	}
	if len(fx.sim.calls) != 2 {
		t.Errorf("%d simulations for two accounts, want one each", len(fx.sim.calls))
	}
}

// TestTheTraceCarriesTheVerdictAndNotTheCommand holds the disclosure rule.
// The trace exists so an operator can see WHY an action was allowed to
// proceed; it must not become a second copy of the command line, which is
// the thing the audit log is deliberately not a store of.
func TestTheTraceCarriesTheVerdictAndNotTheCommand(t *testing.T) {
	ctx := context.Background()
	fx := newGuard(t, nil)
	fx.sim.res = policy.DryRunResult{
		Verdict: policy.VerdictAllow, RiskLevel: policy.L1,
		MatchedRule: "autonomy-profile", Explanation: "L1 is auto-advance eligible",
	}

	if err := fx.guard.Guard(ctx, sampleRequest()); err != nil {
		t.Fatal(err)
	}
	if len(fx.trace.events) != 1 {
		t.Fatalf("%d trace rows, want exactly 1", len(fx.trace.events))
	}
	ev := fx.trace.events[0]
	if ev.Kind != audit.KindPolicyRoute {
		t.Errorf("trace kind = %v, want the frozen KindPolicyRoute", ev.Kind)
	}
	var row map[string]any
	if err := json.Unmarshal(ev.Explain, &row); err != nil {
		t.Fatalf("the trace rationale is not JSON: %v", err)
	}
	if row["matched_rule"] != "autonomy-profile" {
		t.Errorf("trace matched_rule = %v, want the engine's own layer name", row["matched_rule"])
	}
	blob := string(ev.Explain) + ev.Action + ev.Outcome
	if strings.Contains(blob, "rm -rf") {
		t.Errorf("the trace reproduced the command text: %s", blob)
	}
}
