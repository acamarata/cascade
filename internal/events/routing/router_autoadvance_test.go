package routing

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
)

// Purpose (this file): the ask-resolution stage AS WIRED — that it runs
//   inside the one middleware, that it can only turn an ask into an allow,
//   and that nothing it does can escape into the decision path.
// SPORT: internal/events/routing autoadvance tests (ADD) — P1-E18-W4-S39-T2.

// stubAdvance returns a fixed answer and records what it was asked.
type stubAdvance struct {
	verdict  policy.Verdict
	advance  supervision.Verdict
	err      error
	panics   bool
	saw      supervision.ActionDescriptor
	sawTrust supervision.TrustTag
	calls    int
}

func (s *stubAdvance) Resolve(_ context.Context, action supervision.ActionDescriptor,
	_ supervision.PolicyDecision, trust supervision.TrustTag,
) (policy.Verdict, supervision.Verdict, error) {
	s.calls++
	s.saw, s.sawTrust = action, trust
	if s.panics {
		panic("stage exploded")
	}
	return s.verdict, s.advance, s.err
}

// recordingSink captures what the router reported.
type recordingSink struct {
	verdicts []supervision.Verdict
	err      error
}

func (r *recordingSink) Record(_ context.Context, _ supervision.ActionDescriptor,
	_ supervision.PolicyDecision, _ supervision.TrustTag, verdict supervision.Verdict,
) error {
	r.verdicts = append(r.verdicts, verdict)
	return r.err
}

// askingEngine always returns the verdict it was built with.
type askingEngine struct {
	verdict policy.Verdict
	level   policy.RiskLevel
}

func (e askingEngine) Evaluate(context.Context, policy.EvalRequest) (policy.EvalOutcome, error) {
	return policy.EvalOutcome{Verdict: e.verdict, AutoAdvance: true, Level: e.level}, nil
}

// routerWith builds a router over engine with the stage attached.
func routerWith(t *testing.T, engine PolicyEngine, advance AutoAdvance, sink AutoAdvanceSink) *ActionRouter {
	t.Helper()
	r, err := NewActionRouter(engine, newAuditLog())
	if err != nil {
		t.Fatal(err)
	}
	if advance != nil {
		r = r.WithAutoAdvance(advance, sink)
	}
	return r
}

// trustedAction is a routable action from a trusted source.
func trustedAction() Action {
	return Action{
		Subject:     policy.Subject{Kind: policy.SubjectUser, ID: "u1"},
		Capability:  "shell.exec",
		Command:     "go test ./...",
		Origin:      OriginHook,
		Ref:         "hook-1",
		Summary:     "run tests",
		SourceTrust: corpus.TrustTrusted,
	}
}

// TestAnEligibleAskResolvesToAllow is the stage doing its job inside the
// middleware — the only way an action proceeds with no human turn.
func TestAnEligibleAskResolvesToAllow(t *testing.T) {
	advance := &stubAdvance{verdict: policy.VerdictAllow, advance: supervision.VerdictAutoApproved}
	sink := &recordingSink{}

	got, _, err := routerWith(t, askingEngine{verdict: policy.VerdictAsk, level: policy.L0},
		advance, sink).RouteAction(context.Background(), trustedAction())
	if err != nil {
		t.Fatalf("RouteAction: %v", err)
	}
	if got != policy.VerdictAllow {
		t.Errorf("verdict = %v, want allow", got)
	}
	if advance.sawTrust != corpus.TrustTrusted {
		t.Errorf("the stage saw trust %q, want the action's own tag", advance.sawTrust)
	}
	if advance.saw.Ref != "hook-1" || advance.saw.Origin != string(OriginHook) {
		t.Errorf("the stage saw %+v, want the action's ref and origin", advance.saw)
	}
	if len(sink.verdicts) != 1 || sink.verdicts[0] != supervision.VerdictAutoApproved {
		t.Errorf("recorded %v, want one auto_approved", sink.verdicts)
	}
}

// TestTheStageIsNeverConsultedForAllowOrDeny is the narrowing property at
// the wiring level: it is an ask-RESOLUTION stage, not a second decision
// point, so it must not even run for a verdict the engine already settled.
func TestTheStageIsNeverConsultedForAllowOrDeny(t *testing.T) {
	for _, verdict := range []policy.Verdict{policy.VerdictAllow, policy.VerdictDeny} {
		advance := &stubAdvance{verdict: policy.VerdictAllow, advance: supervision.VerdictAutoApproved}
		got, _, _ := routerWith(t, askingEngine{verdict: verdict, level: policy.L0},
			advance, nil).RouteAction(context.Background(), trustedAction())
		if advance.calls != 0 {
			t.Errorf("%v: the stage ran %d times, want never", verdict, advance.calls)
		}
		if got != verdict {
			t.Errorf("the stage changed %v into %v", verdict, got)
		}
	}
}

// TestAStageFailureLeavesTheAskStanding: an unreadable auto-advance answer
// must become neither an approval nor a denial of something the engine was
// willing to ask about.
func TestAStageFailureLeavesTheAskStanding(t *testing.T) {
	advance := &stubAdvance{verdict: policy.VerdictAllow, advance: supervision.VerdictFailClosedDenied,
		err: errors.New("could not decide")}

	got, _, err := routerWith(t, askingEngine{verdict: policy.VerdictAsk, level: policy.L0},
		advance, nil).RouteAction(context.Background(), trustedAction())
	if err != nil {
		t.Fatalf("RouteAction: %v", err)
	}
	if got != policy.VerdictAsk {
		t.Errorf("verdict = %v, want the ask left standing", got)
	}
}

// TestAnUnwiredRouterBehavesExactlyAsBefore: attaching nothing changes
// nothing, which is what makes this safe to land ahead of its config.
func TestAnUnwiredRouterBehavesExactlyAsBefore(t *testing.T) {
	got, _, err := routerWith(t, askingEngine{verdict: policy.VerdictAsk, level: policy.L0},
		nil, nil).RouteAction(context.Background(), trustedAction())
	if err != nil {
		t.Fatalf("RouteAction: %v", err)
	}
	if got != policy.VerdictAsk {
		t.Errorf("verdict = %v, want ask", got)
	}
}

// TestASinkFailureDoesNotChangeTheDecision: the router writes its own
// routing record unconditionally, so failing an action because a
// supplementary record could not be written would trade a working system
// for a tidier log.
func TestASinkFailureDoesNotChangeTheDecision(t *testing.T) {
	advance := &stubAdvance{verdict: policy.VerdictAllow, advance: supervision.VerdictAutoApproved}
	sink := &recordingSink{err: errors.New("audit down")}

	got, _, err := routerWith(t, askingEngine{verdict: policy.VerdictAsk, level: policy.L0},
		advance, sink).RouteAction(context.Background(), trustedAction())
	if err != nil {
		t.Fatalf("RouteAction: %v", err)
	}
	if got != policy.VerdictAllow {
		t.Errorf("verdict = %v, want the decision unaffected by the sink failure", got)
	}
}

// TestAnUnregisteredVerbIsTreatedAsElevated holds the fail-closed lookup:
// a name nobody can prove is harmless never auto-advances.
func TestAnUnregisteredVerbIsTreatedAsElevated(t *testing.T) {
	if !verbIsElevated("definitely.not.a.registered.verb") {
		t.Error("an unregistered verb was treated as non-elevated")
	}
	if verbIsElevated("") {
		t.Error("an action with no verb was treated as an elevated verb call")
	}
}
