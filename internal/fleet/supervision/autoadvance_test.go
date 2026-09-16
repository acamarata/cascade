package supervision

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): every branch of the tier-1 ceiling, one test per
//   reason an action can be refused — because each reason has a different
//   remedy and an operator has to be able to tell them apart.
// SPORT: internal/fleet/supervision autoadvance tests (ADD) — P1-E18-W4-S39-T2.

// fixedCeiling is a profile whose master switch never changes.
type fixedCeiling struct{ ceiling policy.Ceiling }

func (f fixedCeiling) AutoAdvanceCeiling() policy.Ceiling { return f.ceiling }

// panickingCeiling models a profile source that blows up mid-decision.
type panickingCeiling struct{}

func (panickingCeiling) AutoAdvanceCeiling() policy.Ceiling {
	panic("the profile source exploded")
}

// tier1 is an evaluator with auto-advance switched on.
func tier1(t *testing.T) *AutoAdvanceEvaluator {
	t.Helper()
	return NewAutoAdvanceEvaluator(fixedCeiling{ceiling: policy.CeilingTier1})
}

// admissible is the decision an engine produces for an L0 action a
// tier-1 profile permits: an ask, with the engine's own ceiling satisfied.
func admissible() PolicyDecision {
	return PolicyDecision{Verdict: policy.VerdictAsk, AutoAdvance: true, Level: policy.L0}
}

// plainAction is a non-elevated action from a hook.
func plainAction() ActionDescriptor {
	return ActionDescriptor{Ref: "hook-1", Origin: "hook", Verb: "status.get"}
}

// evaluate runs one case.
func evaluate(t *testing.T, e *AutoAdvanceEvaluator, a ActionDescriptor, d PolicyDecision, trust TrustTag) Verdict {
	t.Helper()
	v, err := e.Evaluate(context.Background(), a, d, trust)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	return v
}

// TestAllFourConditionsMetIsTheOnlyApproval covers the single path that
// permits an action to proceed with no human turn — at both L0 and L1.
func TestAllFourConditionsMetIsTheOnlyApproval(t *testing.T) {
	for _, level := range []policy.RiskLevel{policy.L0, policy.L1} {
		decision := admissible()
		decision.Level = level
		got := evaluate(t, tier1(t), plainAction(), decision, corpus.TrustTrusted)
		if !got.Approved() {
			t.Errorf("level %v: verdict = %s, want auto_approved", level, got)
		}
	}
}

// TestTheCeilingRefusesWhatTheEngineRefused covers condition 1. The engine
// folds the risk level and the profile's per-level slot into one answer;
// this stage must not second-guess it in either direction.
func TestTheCeilingRefusesWhatTheEngineRefused(t *testing.T) {
	decision := admissible()
	decision.Level = policy.L2
	decision.AutoAdvance = false

	if got := evaluate(t, tier1(t), plainAction(), decision, corpus.TrustTrusted); got != VerdictCeilingRefused {
		t.Errorf("verdict = %s, want ceiling_refused", got)
	}
}

// TestAnUntrustedSourceNeverAutoAdvances covers condition 3, at the
// LOWEST risk level — the point is that trust overrides the level, not
// that a high-risk action is refused.
func TestAnUntrustedSourceNeverAutoAdvances(t *testing.T) {
	for _, trust := range []TrustTag{corpus.TrustUntrustedSource, "", "something-else"} {
		got := evaluate(t, tier1(t), plainAction(), admissible(), trust)
		if got != VerdictUntrustedRefused {
			t.Errorf("trust %q: verdict = %s, want untrusted_refused", trust, got)
		}
	}
}

// TestAnElevatedVerbNeverAutoAdvances covers condition 4, again at L0 and
// with everything else satisfied: elevation is not a risk level, it is a
// separate bar that auto-advance can never clear.
func TestAnElevatedVerbNeverAutoAdvances(t *testing.T) {
	action := plainAction()
	action.Elevated = true

	if got := evaluate(t, tier1(t), action, admissible(), corpus.TrustTrusted); got != VerdictElevatedRefused {
		t.Errorf("verdict = %s, want elevated_refused", got)
	}
}

// TestAProfileThatDidNotOptInIsDisabled covers condition 2, including the
// default: a profile with no ceiling set must not auto-advance, even when
// every other condition holds.
func TestAProfileThatDidNotOptInIsDisabled(t *testing.T) {
	for name, reader := range map[string]CeilingReader{
		"explicitly disabled": fixedCeiling{ceiling: policy.CeilingDisabled},
		"unset":               fixedCeiling{},
		"unrecognised":        fixedCeiling{ceiling: policy.Ceiling("tier9")},
	} {
		got := evaluate(t, NewAutoAdvanceEvaluator(reader), plainAction(), admissible(), corpus.TrustTrusted)
		if got != VerdictProfileDisabled {
			t.Errorf("%s: verdict = %s, want profile_disabled", name, got)
		}
	}
	// And no reader at all — an unwired daemon runs with the feature off.
	if got := evaluate(t, NewAutoAdvanceEvaluator(nil), plainAction(), admissible(), corpus.TrustTrusted); got != VerdictProfileDisabled {
		t.Errorf("nil reader: verdict = %s, want profile_disabled", got)
	}
}

// TestAnUnreadableLevelFailsClosed covers the classifier-error path: a rung
// nobody can read is treated as the top of the ladder, never as low.
func TestAnUnreadableLevelFailsClosed(t *testing.T) {
	decision := admissible()
	decision.Level = policy.RiskLevel(200)

	if got := evaluate(t, tier1(t), plainAction(), decision, corpus.TrustTrusted); got != VerdictFailClosedDenied {
		t.Errorf("verdict = %s, want fail_closed_denied", got)
	}
}

// TestAPanicBecomesADenialAndDoesNotEscape is the guard that matters most:
// this runs inside the one authorization middleware, so a panic escaping
// would take the decision path down for every action origin at once.
func TestAPanicBecomesADenialAndDoesNotEscape(t *testing.T) {
	e := NewAutoAdvanceEvaluator(panickingCeiling{})

	got, err := e.Evaluate(context.Background(), plainAction(), admissible(), corpus.TrustTrusted)
	if got != VerdictFailClosedDenied {
		t.Errorf("verdict = %s, want fail_closed_denied", got)
	}
	if !cascade.HasKind(err, cascade.KindInternal) {
		t.Fatalf("err = %v, want KindInternal", err)
	}
	// The panic value must not reach the message: it can carry anything,
	// and this message goes to the audit log.
	if got := err.Error(); got == "" || contains(got, "exploded") {
		t.Errorf("err = %q, want it to report the recovery without the panic value", got)
	}
}

// contains is a small substring check, kept local.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// TestACancelledContextFailsClosed covers the remaining error path.
func TestACancelledContextFailsClosed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := tier1(t).Evaluate(ctx, plainAction(), admissible(), corpus.TrustTrusted)
	if got != VerdictFailClosedDenied {
		t.Errorf("verdict = %s, want fail_closed_denied", got)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// TestResolveCanOnlyTurnAskIntoAllow is the narrowing property, asserted
// on the integration surface the router actually calls. An allow and a
// deny must both pass through untouched: this stage exists to resolve an
// ask, not to re-decide anything.
func TestResolveCanOnlyTurnAskIntoAllow(t *testing.T) {
	e := tier1(t)
	for _, verdict := range []policy.Verdict{policy.VerdictAllow, policy.VerdictDeny} {
		decision := admissible()
		decision.Verdict = verdict
		got, _, err := e.Resolve(context.Background(), plainAction(), decision, corpus.TrustTrusted)
		if err != nil {
			t.Fatal(err)
		}
		if got != verdict {
			t.Errorf("Resolve changed a %v into a %v", verdict, got)
		}
	}
	// An eligible ask resolves upward.
	got, verdict, err := e.Resolve(context.Background(), plainAction(), admissible(), corpus.TrustTrusted)
	if err != nil {
		t.Fatal(err)
	}
	if got != policy.VerdictAllow || !verdict.Approved() {
		t.Errorf("Resolve(ask) = %v/%s, want allow/auto_approved", got, verdict)
	}
	// A refused ask stays an ask — never a deny.
	refused := admissible()
	refused.AutoAdvance = false
	if got, _, _ := e.Resolve(context.Background(), plainAction(), refused, corpus.TrustTrusted); got != policy.VerdictAsk {
		t.Errorf("a refused ask became %v, want it left as ask", got)
	}
}
