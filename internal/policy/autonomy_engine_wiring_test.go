// Purpose: the engine's construction contract and its annotation
// vocabulary — an engine that cannot consult a layer must refuse to exist
// rather than answer for it, and the layer names an audit row carries.
// Split from autonomy_engine_test.go per Art.10.3 (the 300-line file cap).
//
// SPORT: internal/policy Engine/ADDED (P1-E09-W2-S18-T1).
package policy

import (
	"context"
	"testing"
)

// TestNewEngineRequiresEveryCollaborator covers the constructor's refusals
// and the nil-engine path: an engine that cannot consult a layer must not
// answer for it.
func TestNewEngineRequiresEveryCollaborator(t *testing.T) {
	f := newGrantFixture(t)
	ctrl := NewController(nil)
	if _, err := NewEngine(nil, f.store, ctrl); err == nil {
		t.Error("NewEngine accepted a nil registry")
	}
	if _, err := NewEngine(f.reg, nil, ctrl); err == nil {
		t.Error("NewEngine accepted a nil grant store")
	}
	if _, err := NewEngine(f.reg, f.store, nil); err == nil {
		t.Error("NewEngine accepted a nil autonomy controller")
	}
	var nilEngine *Engine
	out, err := nilEngine.Evaluate(context.Background(), readRequest(L0))
	if err == nil || out.Verdict != VerdictDeny {
		t.Errorf("the nil engine answered %+v, %v; want a deny with an error", out, err)
	}
	if nilEngine.HasProfile() {
		t.Error("the nil engine reports a profile")
	}
}

// TestDecisionLayerNames covers the layer annotation's rendering.
func TestDecisionLayerNames(t *testing.T) {
	cases := map[DecisionLayer]string{
		LayerStandingGrant:     "standing-grant",
		LayerCapabilityDefault: "capability-default",
		LayerAutonomyProfile:   "autonomy-profile",
		LayerFailClosed:        "fail-closed",
		DecisionLayer(0):       "invalid-decision-layer",
		DecisionLayer(9):       "invalid-decision-layer",
	}
	for layer, want := range cases {
		if got := layer.String(); got != want {
			t.Errorf("DecisionLayer(%d).String() = %q, want %q", layer, got, want)
		}
	}
}
