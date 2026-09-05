// Purpose: the engine's vocabulary and its wiring — the layer enum's
// names and indexes, the three seam options, the data-class ladder, and
// the terminal refusals that never reach a layer.
//
// SPORT: internal/policy policy-engine/ADDED (P1-E09-W2-S17-T2).
package policy

import (
	"context"
	"testing"
)

// TestDecisionLayerVocabulary asserts the seven layers against the
// normative order written out by hand, indexes included.
func TestDecisionLayerVocabulary(t *testing.T) {
	want := []struct {
		layer DecisionLayer
		name  string
		index uint8
	}{
		{LayerDataClass, "data-class", 0},
		{LayerDenyList, "deny-list", 1},
		{LayerElevation, "elevation", 2},
		{LayerStandingGrant, "standing-grant", 3},
		{LayerCapabilityDefault, "capability-default", 4},
		{LayerAutonomyProfile, "autonomy-profile", 5},
		{LayerFailClosed, "fail-closed", 6},
	}
	for _, w := range want {
		if got := w.layer.String(); got != w.name {
			t.Errorf("layer %d renders as %q, want %q", w.index, got, w.name)
		}
		if got := w.layer.Index(); got != w.index {
			t.Errorf("%s has index %d, want %d", w.name, got, w.index)
		}
	}
	for _, bad := range []DecisionLayer{0, 8, 200} {
		if got := bad.String(); got != "invalid-decision-layer" {
			t.Errorf("DecisionLayer(%d) renders as %q", bad, got)
		}
		if got := bad.Index(); got != LayerFailClosed.Index() {
			t.Errorf("DecisionLayer(%d) indexes as %d, want the last index", bad, got)
		}
	}
}

// TestDataClassLadder pins the R-21.27 ordering and the fail-closed
// direction: an unset or out-of-range class reads as the most restricted
// material, never as publishable.
func TestDataClassLadder(t *testing.T) {
	order := []DataClass{DataClassPublic, DataClassInternal, DataClassConfidential, DataClassSecret}
	names := []string{"public", "internal", "confidential", "secret"}
	for i, c := range order {
		if c.String() != names[i] {
			t.Errorf("DataClass %d renders as %q, want %q", i, c.String(), names[i])
		}
		if !c.Valid() {
			t.Errorf("%s is not valid", names[i])
		}
		if i > 0 && order[i-1] >= c {
			t.Errorf("%s does not sort below %s", names[i-1], names[i])
		}
		if safeDataClass(c) != c {
			t.Errorf("safeDataClass changed the valid class %s", names[i])
		}
	}
	for _, bad := range []DataClass{0, 5, 200} {
		if safeDataClass(bad) != DataClassSecret {
			t.Errorf("safeDataClass(%d) = %s, want secret", bad, safeDataClass(bad))
		}
		if bad.String() != "invalid-data-class" {
			t.Errorf("DataClass(%d) renders as %q", bad, bad.String())
		}
	}
}

// TestSeamOptionsAttachAndDetach covers the three wiring options, the nil
// receiver each tolerates, and the detach case.
func TestSeamOptionsAttachAndDetach(t *testing.T) {
	f := layerFixture(t)
	e := f.engine
	if e.WithDenyList(nil) != e || e.WithSameTurnAuthorizer(nil) != e ||
		e.WithElevationVerifier(nil) != e {
		t.Fatal("an option returned another engine")
	}
	e.WithSameTurnAuthorizer(alwaysSameTurn{}).WithElevationVerifier(refusingElevation{})
	if e.sameTurn == nil || e.elevation == nil {
		t.Error("the seams did not attach")
	}
	e.WithSameTurnAuthorizer(nil).WithElevationVerifier(nil)
	if e.sameTurn != nil || e.elevation != nil {
		t.Error("the seams did not detach")
	}
	var nilEngine *Engine
	if nilEngine.WithDenyList(nil) != nil || nilEngine.WithSameTurnAuthorizer(nil) != nil ||
		nilEngine.WithElevationVerifier(nil) != nil {
		t.Error("an option on a nil engine returned something")
	}
}

// deniedList is a deny-lister that lists everything, standing in for the
// configurable portion S-17.T4 owns.
type deniedList struct{ err error }

func (d deniedList) Denied(context.Context, string) (bool, error) {
	return d.err == nil, d.err
}

// TestConfiguredDenyListRefusesAndFailsClosed covers layer 1's
// configurable portion in both directions: a listed action is denied, and
// a deny-lister that cannot answer is treated as a match.
func TestConfiguredDenyListRefusesAndFailsClosed(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		list DenyLister
	}{
		{"the list names the action", deniedList{}},
		{"the list cannot be read", deniedList{err: context.Canceled}},
	} {
		f := layerFixture(t)
		f.engine.WithDenyList(tc.list)
		out, err := f.engine.Evaluate(ctx, readRequest(L0))
		if err != nil {
			t.Errorf("%s: Evaluate: %v", tc.name, err)
			continue
		}
		if out.Verdict != VerdictDeny || out.Layer != LayerDenyList {
			t.Errorf("%s: outcome = %+v, want a layer 1 deny", tc.name, out)
		}
	}
}

// TestTerminalRefusalsCarryATrace covers the paths that answer before any
// layer runs. Each must still hand back a trace, because a refusal with no
// explanation is the one answer a user cannot act on.
func TestTerminalRefusalsCarryATrace(t *testing.T) {
	ctx := context.Background()
	f := layerFixture(t)
	var nilEngine *Engine
	cases := []struct {
		name   string
		engine *Engine
		req    EvalRequest
	}{
		{"no engine", nilEngine, readRequest(L0)},
		{"the subject names nobody", f.engine, EvalRequest{Capability: readCap().Name}},
		{"the capability is not registered", f.engine, EvalRequest{
			Subject: testSubject(), Capability: "memory.forge",
		}},
	}
	for _, tc := range cases {
		out, err := tc.engine.Evaluate(ctx, tc.req)
		if err == nil {
			t.Errorf("%s: refused with no error to explain it", tc.name)
		}
		if out.Verdict != VerdictDeny || out.Level != L4 {
			t.Errorf("%s: outcome = %+v, want a deny at L4", tc.name, out)
		}
		if len(out.Trace.Layers) != 1 || out.Trace.MatchedRule != LayerFailClosed.String() {
			t.Errorf("%s: trace = %+v, want one fail-closed layer", tc.name, out.Trace)
		}
		if out.Trace.Explanation == "" {
			t.Errorf("%s: a refusal with no explanation", tc.name)
		}
	}
}
