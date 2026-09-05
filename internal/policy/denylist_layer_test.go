// Purpose: hard requirement 1, proved against a REAL stack rather than
// argued from the layer order. A deny-list entry is the last line, so each
// of the four things that could plausibly release it gets its own attempt:
// a permissive autonomy profile, a matching standing grant, an approval
// queue, and a verified elevation attestation. The same file states which
// entries a same-turn authorization MAY override, and proves the ones it
// may not.
//
// SPORT: internal/policy denylist-overridability/CHANGE
// (P1-E09-W2-S17-T4).
package policy

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
)

// deniedAction is the command the configured row names in this file. It
// classifies at L0, so nothing but the deny-list row can be responsible
// for a refusal of it.
const deniedAction = "ls /srv"

// denyStackFixture is the engine under test with a REAL deny-list engine
// over a real store and a REAL same-turn ledger over a frozen clock.
type denyStackFixture struct {
	*engineFixture
	deny   *StoreDenyList
	ledger *SameTurnLedger
}

// newDenyStack builds the engine with both real collaborators attached and
// one configured row already written.
func newDenyStack(t *testing.T) *denyStackFixture {
	t.Helper()
	ef := layerFixture(t)
	deny, err := NewStoreDenyList(ef.db)
	if err != nil {
		t.Fatalf("NewStoreDenyList: %v", err)
	}
	if err := deny.Add(context.Background(), deniedAction, anyActionClass); err != nil {
		t.Fatalf("Add: %v", err)
	}
	ledger, err := NewSameTurnLedger(ef.clock)
	if err != nil {
		t.Fatalf("NewSameTurnLedger: %v", err)
	}
	ef.engine.WithDenyList(deny).WithSameTurnAuthorizer(ledger)
	return &denyStackFixture{engineFixture: ef, deny: deny, ledger: ledger}
}

// deniedRequest is the request the configured row names.
func deniedRequest() EvalRequest {
	return EvalRequest{
		Subject:    testSubject(),
		Capability: readCap().Name,
		Action:     deniedAction,
	}
}

// refusingQueue fails the test if the engine ever tries to file an
// approval for an action the deny-list refused. A deny-listed action must
// never become an ask, so this must never be called.
type refusingQueue struct {
	t *testing.T
	ApprovalQueue
}

func (q refusingQueue) Enqueue(context.Context, EnqueueRequest) (EnqueueResult, error) {
	q.t.Error("a deny-listed action was offered to the approval queue")
	return EnqueueResult{}, nil
}

// TestDenyListEntryIsNotDefeatable is hard requirement 1. Each case adds
// one more defeat attempt ON TOP of the ones before it, so the last row is
// a permissive profile AND a standing grant AND an approval queue AND a
// verified attestation, all at once, against one deny-list row.
func TestDenyListEntryIsNotDefeatable(t *testing.T) {
	ctx := context.Background()
	f := newDenyStack(t)
	// The profile in force is allow-everything (engineFixture installs it).
	if !f.ctrl.Profile().AllowsAutoAdvance(L0) {
		t.Fatal("the fixture profile is not the permissive one this test needs")
	}
	cases := []struct {
		name  string
		setUp func(req *EvalRequest)
	}{
		{"a permissive autonomy profile", func(*EvalRequest) {}},
		{"a matching standing grant", func(*EvalRequest) {
			err := f.store.Grant(ctx, Grant{
				Subject:    testSubject(),
				Capability: readCap().Name,
				ScopeClass: corpus.VisibilityTeam,
				Verdict:    VerdictAllow,
			})
			if err != nil {
				t.Fatalf("Grant: %v", err)
			}
		}},
		{"an approval queue that would file the ask", func(*EvalRequest) {
			f.engine.WithApprovalQueue(refusingQueue{t: t})
		}},
		{"a verified elevation attestation", func(req *EvalRequest) {
			f.engine.WithElevationVerifier(grantingElevation{})
			req.Verb = "vault.get"
			req.ElevationNonce = "a-live-nonce"
		}},
	}
	req := deniedRequest()
	for _, tc := range cases {
		tc.setUp(&req)
		out, err := f.engine.Evaluate(ctx, req)
		if err != nil {
			t.Errorf("%s: Evaluate: %v", tc.name, err)
			continue
		}
		if out.Verdict != VerdictDeny || out.Layer != LayerDenyList {
			t.Errorf("%s: outcome = %+v; the deny-list entry was defeated", tc.name, out)
		}
		if out.ApprovalRequestID != "" {
			t.Errorf("%s: a deny-listed action was filed as approval %q",
				tc.name, out.ApprovalRequestID)
		}
		if out.AutoAdvance {
			t.Errorf("%s: a refused action reported auto-advance", tc.name)
		}
	}
}

// TestSameTurnHitContinuesToLayerTwo is R-21.231 driven end to end through
// the REAL ledger: a live authorization overrides the ENTRY, layer 1
// records the override without deciding, and the verdict comes from a
// later layer. It also proves the single-use rule at the engine boundary.
func TestSameTurnHitContinuesToLayerTwo(t *testing.T) {
	ctx := context.Background()
	f := newDenyStack(t)
	if _, err := f.ledger.Authorize(ctx, testSubject(), deniedAction); err != nil {
		t.Fatalf("Authorize: %v", err)
	}

	out, err := f.engine.Evaluate(ctx, deniedRequest())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if out.Layer == LayerDenyList {
		t.Fatalf("layer 1 decided despite a live authorization: %+v", out)
	}
	if out.Layer != LayerAutonomyProfile || out.Verdict != VerdictAllow {
		t.Fatalf("outcome = %+v; the verdict must come from a later layer", out)
	}
	if len(out.Trace.Layers) < 2 || out.Trace.Layers[1].Layer != LayerDenyList {
		t.Fatalf("the trace does not record layer 1: %s", out.Trace.Explanation)
	}
	entry := out.Trace.Layers[1]
	if entry.Decided || !strings.Contains(entry.Rule, "same-turn") {
		t.Fatalf("layer 1 = %+v, want an undecided same-turn override", entry)
	}

	// Single use: the authorization was spent by that evaluation.
	out, err = f.engine.Evaluate(ctx, deniedRequest())
	if err != nil {
		t.Fatalf("second Evaluate: %v", err)
	}
	if out.Verdict != VerdictDeny || out.Layer != LayerDenyList {
		t.Fatalf("the authorization was replayable: %+v", out)
	}
}

// TestSameTurnOverrideIsNotAnAllow asserts the override does not release
// what the rest of the stack refuses. An L4 action that a live
// authorization carried past layer 1 is still denied, because §5.15's
// hard floor clamps that rung whatever the profile says.
func TestSameTurnOverrideIsNotAnAllow(t *testing.T) {
	ctx := context.Background()
	f := newDenyStack(t)
	req := deniedRequest()
	req.Action = commandForLevel(L4)
	if _, err := f.ledger.Authorize(ctx, testSubject(), req.Action); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	out, err := f.engine.Evaluate(ctx, req)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if out.Verdict != VerdictDeny {
		t.Fatalf("an L4 action was allowed by a same-turn override: %+v", out)
	}
	if out.Layer == LayerDenyList {
		t.Fatalf("layer 1 decided despite the override: %+v", out)
	}
}

// TestAnUnreadableDenyListIsNeverOverridable states the one entry class a
// same-turn authorization may NOT override, and proves it. Nobody can
// authorize past an entry nobody can read, and an unreadable list is
// exactly the state an attacker would arrange for.
func TestAnUnreadableDenyListIsNeverOverridable(t *testing.T) {
	ctx := context.Background()
	f := newDenyStack(t)
	// alwaysSameTurn authorizes EVERYTHING, which is stronger than any
	// authorization a real ledger could hold.
	f.engine.WithDenyList(deniedList{err: context.Canceled}).
		WithSameTurnAuthorizer(alwaysSameTurn{})

	out, err := f.engine.Evaluate(ctx, deniedRequest())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if out.Verdict != VerdictDeny || out.Layer != LayerDenyList {
		t.Fatalf("an unreadable deny-list was overridden: %+v", out)
	}
	if !strings.Contains(out.Reason, "could not be read") {
		t.Errorf("the refusal does not say why: %q", out.Reason)
	}

	// An authorizer that ERRORS has not authorized anything either.
	f.engine.WithDenyList(deniedList{}).WithSameTurnAuthorizer(erroringSameTurn{})
	out, err = f.engine.Evaluate(ctx, deniedRequest())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if out.Verdict != VerdictDeny || out.Layer != LayerDenyList {
		t.Fatalf("an erroring authorizer released a deny-list entry: %+v", out)
	}
}

// erroringSameTurn cannot answer. An error is not an authorization.
type erroringSameTurn struct{}

func (erroringSameTurn) Authorized(context.Context, Subject, string) (bool, error) {
	return true, context.Canceled
}

// TestUnconditionalDenyListMatchesTheSpecSentence is hard requirement 3:
// layer 1's unconditional entry is asserted against the §5.15 prose
// transcribed in types_test.go, not against a second copy of the rule. The
// spec reserves exactly ONE rung for same-turn authorization; every rung
// below it must pass layer 1 when no row is configured.
func TestUnconditionalDenyListMatchesTheSpecSentence(t *testing.T) {
	ctx := context.Background()
	rungs := parseSpecLadder(t)
	if len(rungs) != 5 {
		t.Fatalf("the spec ladder has %d rungs, want 5", len(rungs))
	}
	for _, rung := range rungs {
		class, ok := specDescriptionToClass[rung.description]
		if !ok {
			t.Fatalf("no class translation for %q", rung.description)
		}
		level := class.Risk()
		// The spec's own words decide the expectation.
		wantListed := strings.Contains(rung.disposition, "deny")

		f := layerFixture(t)
		req := deniedRequest()
		req.Action = commandForLevel(level)
		out, err := f.engine.Evaluate(ctx, req)
		if err != nil {
			t.Errorf("%s: Evaluate: %v", level, err)
			continue
		}
		gotListed := out.Layer == LayerDenyList
		if gotListed != wantListed {
			t.Errorf("%s: layer 1 decided=%v, but the spec says %q (%+v)",
				level, gotListed, rung.disposition, out)
		}
		if wantListed && out.Verdict != VerdictDeny {
			t.Errorf("%s: the reserved rung yielded %s", level, out.Verdict)
		}
	}
}

// TestEngineDefaultsAreTheSafeOnes asserts NewEngine attaches the complete
// named defaults (Art.1) rather than leaving the seams nil, and that they
// change no verdict.
func TestEngineDefaultsAreTheSafeOnes(t *testing.T) {
	ctx := context.Background()
	f := layerFixture(t)
	if f.engine.denyList == nil || f.engine.sameTurn == nil {
		t.Fatal("NewEngine left the layer 1 seams unattached")
	}
	if ok, err := f.engine.sameTurn.Authorized(ctx, testSubject(), deniedAction); ok || err != nil {
		t.Errorf("the default authorizer authorized something: %v, %v", ok, err)
	}
	denied, err := f.engine.denyList.Denied(ctx, deniedAction)
	if denied || err != nil {
		t.Errorf("the default deny-list matched: %v, %v", denied, err)
	}
	out, err := f.engine.Evaluate(ctx, deniedRequest())
	if err != nil || out.Verdict != VerdictAllow {
		t.Fatalf("the defaults changed a verdict: %+v, %v", out, err)
	}
}
