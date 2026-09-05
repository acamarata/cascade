// Purpose: the explain-why output. The property under test is that the
// explanation is produced BY the evaluation that produced the verdict: it
// names the layer that actually decided, on every layer including the
// refusals, and it is byte-identical across calls with identical inputs.
//
// SPORT: internal/policy explain-why-trace/ADDED (P1-E09-W2-S17-T2).
package policy

import (
	"context"
	"strings"
	"testing"
)

// TestExplainNamesTheLayerThatDecided walks every layer that can decide
// and asserts the rendered explanation marks that layer, and only that
// layer, as the deciding one.
func TestExplainNamesTheLayerThatDecided(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		build func(t *testing.T) (*engineFixture, EvalRequest)
		want  DecisionLayer
	}{
		{"deny-list", func(t *testing.T) (*engineFixture, EvalRequest) {
			return layerFixture(t), readRequest(L4)
		}, LayerDenyList},
		{"elevation", func(t *testing.T) (*engineFixture, EvalRequest) {
			req := readRequest(L0)
			req.Verb = "vault.get"
			return layerFixture(t), req
		}, LayerElevation},
		{"standing grant", func(t *testing.T) (*engineFixture, EvalRequest) {
			f := layerFixture(t)
			if err := f.store.Grant(ctx, validGrant()); err != nil {
				t.Fatalf("Grant: %v", err)
			}
			return f, readRequest(L0)
		}, LayerStandingGrant},
		{"capability default", func(t *testing.T) (*engineFixture, EvalRequest) {
			req := readRequest(L0)
			req.Capability = mutatingCap().Name
			return layerFixture(t), req
		}, LayerCapabilityDefault},
		{"autonomy profile", func(t *testing.T) (*engineFixture, EvalRequest) {
			return layerFixture(t), readRequest(L2)
		}, LayerAutonomyProfile},
		{"fail closed", func(t *testing.T) (*engineFixture, EvalRequest) {
			f := layerFixture(t)
			f.ctrl.profile.Store(nil)
			return f, readRequest(L2)
		}, LayerFailClosed},
	}
	for _, tc := range cases {
		f, req := tc.build(t)
		out, err := f.engine.Evaluate(ctx, req)
		if err != nil {
			t.Errorf("%s: Evaluate: %v", tc.name, err)
			continue
		}
		assertExplains(t, tc.name, out, tc.want)
	}
}

// assertExplains checks that the explanation agrees with the verdict it
// accompanies: exactly one DECIDED line, on the layer the outcome names,
// carrying the verdict the outcome carries.
func assertExplains(t *testing.T, name string, out EvalOutcome, want DecisionLayer) {
	t.Helper()
	if out.Layer != want {
		t.Errorf("%s: decided at %s, want %s", name, out.Layer, want)
	}
	decided := []string{}
	for _, line := range strings.Split(out.Trace.Explanation, "\n") {
		if strings.Contains(line, "DECIDED") {
			decided = append(decided, line)
		}
	}
	if len(decided) != 1 {
		t.Fatalf("%s: %d decided lines in:\n%s", name, len(decided), out.Trace.Explanation)
	}
	if !strings.Contains(decided[0], "("+want.String()+")") {
		t.Errorf("%s: the decided line names another layer: %q", name, decided[0])
	}
	if !strings.Contains(decided[0], out.Verdict.String()) {
		t.Errorf("%s: the decided line says %q but the verdict is %s",
			name, decided[0], out.Verdict)
	}
	if !strings.Contains(decided[0], out.Reason) {
		t.Errorf("%s: the reason %q is not in the explanation %q", name, out.Reason, decided[0])
	}
}

// TestExplainIsDeterministic runs the same request repeatedly and requires
// byte-identical explanations. The request carries attributes, whose map
// would be the one place a rendering could pick up iteration order.
func TestExplainIsDeterministic(t *testing.T) {
	ctx := context.Background()
	f := layerFixture(t)
	req := readRequest(L2)
	req.Attributes = map[string]string{
		"repo": "cascade", "lane": "a", "branch": "main", "host": "local",
	}
	first, err := f.engine.Evaluate(ctx, req)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	for i := 0; i < 20; i++ {
		out, err := f.engine.Evaluate(ctx, req)
		if err != nil {
			t.Fatalf("Evaluate on run %d: %v", i, err)
		}
		if out.Trace.Explanation != first.Trace.Explanation {
			t.Fatalf("run %d explained differently:\n%s\n---\n%s",
				i, out.Trace.Explanation, first.Trace.Explanation)
		}
		if out.Trace.MatchedRule != first.Trace.MatchedRule {
			t.Fatalf("run %d matched %q, first matched %q",
				i, out.Trace.MatchedRule, first.Trace.MatchedRule)
		}
	}
}

// TestExplainTraceEdges covers the two shapes the renderer must handle
// without inventing a decision: no layers at all, and a layer that
// recorded no rule.
func TestExplainTraceEdges(t *testing.T) {
	if got := ExplainTrace(Trace{}); !strings.Contains(got, "nothing was decided") {
		t.Errorf("an empty trace rendered %q", got)
	}
	tr := traceOf([]LayerResult{{Index: 3, Layer: LayerStandingGrant}})
	if tr.MatchedRule != "" {
		t.Errorf("a trace with no deciding layer matched %q", tr.MatchedRule)
	}
	if !strings.Contains(tr.Explanation, "recorded no rule") {
		t.Errorf("a ruleless layer rendered %q", tr.Explanation)
	}
	if !strings.Contains(tr.Explanation, "layer 3 (standing-grant)") {
		t.Errorf("the layer line is missing from %q", tr.Explanation)
	}
}

// TestTraceIsACopy pins that a trace handed to a caller does not change
// when the run that produced it records another layer.
func TestTraceIsACopy(t *testing.T) {
	results := []LayerResult{{Index: 0, Layer: LayerDataClass, Rule: "first"}}
	tr := traceOf(results)
	results[0].Rule = "rewritten"
	if tr.Layers[0].Rule != "first" {
		t.Errorf("the trace followed a later edit: %q", tr.Layers[0].Rule)
	}
}
