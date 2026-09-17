package supervision

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the dispatcher's routing — which tier answers, and
//   what "not wired" means for each.
//
// The property under test that carries the most weight: a tier the
//   operator selected but nobody wired REFUSES. The tempting alternative,
//   falling back to tier 1, would quietly give them LESS supervision than
//   they asked for, which is the single worst direction for this
//   particular mistake.
// SPORT: fleet.supervision tier-dispatch tests (ADD) — P1-E18-W4-S39-T3.

// fixedTier returns a reader pinned to one tier.
func fixedTier(t Tier) TierReader { return func() Tier { return t } }

// dispatchRequest is the action under test.
func dispatchRequest() policy.EvalRequest {
	return policy.EvalRequest{
		Subject:    policy.Subject{Kind: policy.SubjectAgent, ID: "agent-d"},
		Capability: "hooks.shell",
		Attributes: map[string]string{"ref": "act-dispatch"},
	}
}

// TestTierOneIsNotHandledHere is what keeps this seam from changing
// anything for the operators who never configure it. Tier 1 reports
// handled=false, so the auto-advance stage below decides exactly as it did
// before this existed.
func TestTierOneIsNotHandledHere(t *testing.T) {
	d, err := NewTierDispatcher(fixedTier(TierHookMediated), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	handled, verdict, err := d.Supervise(context.Background(), dispatchRequest(), policy.EvalOutcome{Level: policy.L4})
	if handled {
		t.Error("tier 1 reported handled; the auto-advance stage must still decide")
	}
	if err != nil {
		t.Errorf("tier 1 returned an error: %v", err)
	}
	if verdict != policy.VerdictAsk {
		t.Errorf("verdict = %v, want the ask left standing", verdict)
	}
}

// TestTierThreeAnswersAndRefuses holds tier 3's contract at the seam: it
// handles the action, it denies, and the refusal says the action was
// recorded rather than merely rejected — an agent told only "denied"
// retries.
func TestTierThreeAnswersAndRefuses(t *testing.T) {
	queue := &capturingAttention{}
	d, err := NewTierDispatcher(fixedTier(TierSuggestOnly), nil, NewTier3Supervisor(queue, nil))
	if err != nil {
		t.Fatal(err)
	}
	handled, verdict, err := d.Supervise(context.Background(), dispatchRequest(), policy.EvalOutcome{Level: policy.L1})
	if !handled {
		t.Fatal("tier 3 did not handle the action")
	}
	if verdict != policy.VerdictDeny {
		t.Errorf("verdict = %v, want deny", verdict)
	}
	if !cascade.HasKind(err, cascade.KindPolicyDenied) {
		t.Fatalf("err = %v, want KindPolicyDenied", err)
	}
	if !strings.Contains(err.Error(), "NOT run") || !strings.Contains(err.Error(), "act-dispatch") {
		t.Errorf("err = %q, want it to name the action and say it was not run", err)
	}
	if len(queue.items) != 1 {
		t.Errorf("%d suggestions queued, want 1", len(queue.items))
	}
}

// TestTierThreeAppliesAtEveryRung is the difference between a tier and a
// risk threshold. Tier 3 suggests everything, including an L0 read, which
// is exactly what an operator who chose suggest-only asked for.
func TestTierThreeAppliesAtEveryRung(t *testing.T) {
	queue := &capturingAttention{}
	d, err := NewTierDispatcher(fixedTier(TierSuggestOnly), nil, NewTier3Supervisor(queue, nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, level := range []policy.RiskLevel{policy.L0, policy.L1, policy.L2, policy.L3, policy.L4} {
		handled, verdict, _ := d.Supervise(context.Background(), dispatchRequest(), policy.EvalOutcome{Level: level})
		if !handled || verdict != policy.VerdictDeny {
			t.Errorf("%v: handled=%v verdict=%v, want handled and denied", level, handled, verdict)
		}
	}
	if len(queue.items) != 5 {
		t.Errorf("%d suggestions for 5 rungs, want one each", len(queue.items))
	}
}

// TestTierTwoApprovesAndDeniesThroughTheDispatcher drives the whole tier-2
// path at the seam, so the dispatcher's attach/ask/close sequence is
// exercised rather than only the supervisor's decision half.
func TestTierTwoApprovesAndDeniesThroughTheDispatcher(t *testing.T) {
	for answer, wantVerdict := range map[string]policy.Verdict{
		"y\n": policy.VerdictAllow,
		"n\n": policy.VerdictDeny,
	} {
		att := &scriptedAttacher{answer: answer}
		s, err := NewTier2Supervisor(att, noEnv, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		d, err := NewTierDispatcher(fixedTier(TierPTYAttached), s, nil)
		if err != nil {
			t.Fatal(err)
		}
		handled, verdict, serr := d.Supervise(context.Background(), dispatchRequest(), policy.EvalOutcome{Level: policy.L3})
		if !handled {
			t.Fatalf("%q: tier 2 did not handle the action", answer)
		}
		if verdict != wantVerdict {
			t.Errorf("%q: verdict = %v, want %v", answer, verdict, wantVerdict)
		}
		if wantVerdict == policy.VerdictAllow && serr != nil {
			t.Errorf("%q: err = %v, want none", answer, serr)
		}
		if wantVerdict == policy.VerdictDeny && serr == nil {
			t.Errorf("%q: no error alongside the deny", answer)
		}
		// The terminal is released whichever way the answer went: a tier
		// that leaked a device per held action would exhaust the host.
		if att.closed != 1 {
			t.Errorf("%q: the session was closed %d times, want exactly 1", answer, att.closed)
		}
	}
}

// TestTierTwoAttachesNothingBelowItsRung is the cost argument made
// concrete. An L0 read must not open a device, and it must fall through to
// the stage below rather than being answered here.
func TestTierTwoAttachesNothingBelowItsRung(t *testing.T) {
	att := &scriptedAttacher{answer: "n\n"}
	s, err := NewTier2Supervisor(att, noEnv, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewTierDispatcher(fixedTier(TierPTYAttached), s, nil)
	if err != nil {
		t.Fatal(err)
	}
	handled, verdict, serr := d.Supervise(context.Background(), dispatchRequest(), policy.EvalOutcome{Level: policy.L0})
	if !handled || verdict != policy.VerdictAsk || serr != nil {
		t.Errorf("handled=%v verdict=%v err=%v, want the ask left standing", handled, verdict, serr)
	}
	if att.closed != 0 || att.out != nil {
		t.Error("tier 2 attached a terminal for an action below its held rung")
	}
}

// TestAConfiguredTierWithNoSupervisorRefuses is the property this file
// exists for. Falling back to tier 1 here would give the operator less
// supervision than they configured, silently.
func TestAConfiguredTierWithNoSupervisorRefuses(t *testing.T) {
	for name, tier := range map[string]Tier{
		"tier 2": TierPTYAttached,
		"tier 3": TierSuggestOnly,
	} {
		d, err := NewTierDispatcher(fixedTier(tier), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		handled, verdict, serr := d.Supervise(context.Background(), dispatchRequest(), policy.EvalOutcome{Level: policy.L3})
		if !handled {
			t.Errorf("%s: fell through to tier 1; the operator configured more supervision than that", name)
		}
		if verdict != policy.VerdictDeny {
			t.Errorf("%s: verdict = %v, want deny", name, verdict)
		}
		if !cascade.HasKind(serr, cascade.KindUnavailable) {
			t.Errorf("%s: err = %v, want KindUnavailable naming the gap", name, serr)
		}
	}
}

// TestAnInvalidTierDeniesRatherThanGuessing covers the reader returning
// something the config parser would have rejected. The answer is the ask
// the engine already reached, never an approval.
func TestAnInvalidTierDeniesRatherThanGuessing(t *testing.T) {
	d, err := NewTierDispatcher(fixedTier(Tier(9)), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	handled, verdict, serr := d.Supervise(context.Background(), dispatchRequest(), policy.EvalOutcome{Level: policy.L0})
	if !handled || verdict != policy.VerdictDeny {
		t.Errorf("handled=%v verdict=%v, want a handled deny", handled, verdict)
	}
	if !cascade.HasKind(serr, cascade.KindInvalidInput) {
		t.Errorf("err = %v, want KindInvalidInput", serr)
	}
}

// TestTheTierIsReadLive holds R-16.54's hot-reload half: a tier change is
// observed by the NEXT action, not at the next daemon restart.
func TestTheTierIsReadLive(t *testing.T) {
	current := TierHookMediated
	d, err := NewTierDispatcher(func() Tier { return current },
		nil, NewTier3Supervisor(&capturingAttention{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if handled, _, _ := d.Supervise(context.Background(), dispatchRequest(), policy.EvalOutcome{Level: policy.L1}); handled {
		t.Fatal("tier 1 handled the action")
	}
	current = TierSuggestOnly
	handled, verdict, _ := d.Supervise(context.Background(), dispatchRequest(), policy.EvalOutcome{Level: policy.L1})
	if !handled || verdict != policy.VerdictDeny {
		t.Error("the reload was not observed; the dispatcher captured the tier instead of reading it")
	}
}

// TestTheDispatcherRefusesWithoutATierReader holds Art.1 at the
// constructor: a dispatcher that could not read the tier would have to
// assume one.
func TestTheDispatcherRefusesWithoutATierReader(t *testing.T) {
	got, err := NewTierDispatcher(nil, nil, nil)
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("err = %v, want KindInvalidInput", err)
	}
	if got != nil {
		t.Error("a refused build returned a dispatcher")
	}
}
