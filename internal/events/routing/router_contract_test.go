package routing

import (
	"context"
	"reflect"
	"testing"

	"github.com/acamarata/cascade/internal/policy"
)

// Purpose: the R-21.236 structural assertions — the router classifies
//   nothing and calls the one evaluation signature.
// Constraints: these assert the ROUTER's shape against the ruling's text,
//   not against a second copy of the router's own behaviour.

// recordingEngine captures the request it was handed.
type recordingEngine struct {
	got policy.EvalRequest
}

func (e *recordingEngine) Evaluate(_ context.Context, req policy.EvalRequest) (policy.EvalOutcome, error) {
	e.got = req
	return policy.EvalOutcome{Verdict: policy.VerdictAllow, Level: policy.L0, Layer: policy.LayerAutonomyProfile}, nil
}

// TestRouteActionPerformsNoClassification asserts the router holds a
// policy engine and nothing else, and that the request it builds carries
// the command text verbatim with no resolved level of any kind: a
// destructive command and a read command reach the evaluator identically
// apart from that text, because the router does not look at it.
func TestRouteActionPerformsNoClassification(t *testing.T) {
	rt := reflect.TypeOf(ActionRouter{})
	for i := 0; i < rt.NumField(); i++ {
		name := rt.Field(i).Name
		if name != "engine" && name != "audit" {
			t.Fatalf("ActionRouter carries field %q; it holds a policy engine and an audit writer only", name)
		}
	}
	if _, found := rt.FieldByName("classifier"); found {
		t.Fatal("ActionRouter carries a classifier field")
	}
	reqType := reflect.TypeOf(policy.EvalRequest{})
	for _, banned := range []string{"RiskLevel", "Level"} {
		if _, found := reqType.FieldByName(banned); found {
			t.Fatalf("EvalRequest carries %q; no caller of Evaluate supplies a level", banned)
		}
	}

	eng := &recordingEngine{}
	r, err := NewActionRouter(eng, newAuditLog())
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	for _, command := range []string{"cat notes.txt", "rm -rf / --no-preserve-root"} {
		if _, _, err := r.RouteAction(context.Background(), routedAction(command, "hooks.read")); err != nil {
			t.Fatalf("route %q: %v", command, err)
		}
		if eng.got.Action != command {
			t.Fatalf("engine saw action %q, want the command verbatim %q", eng.got.Action, command)
		}
	}
}

// TestRouteActionUsesCanonicalEvaluateSignature asserts the seam the
// router calls is the evaluator's own signature, transcribed from
// evaluator.go: Evaluate(ctx, EvalRequest) (EvalOutcome, error), and that
// the production engine satisfies it.
func TestRouteActionUsesCanonicalEvaluateSignature(t *testing.T) {
	seam := reflect.TypeOf((*PolicyEngine)(nil)).Elem()
	if seam.NumMethod() != 1 {
		t.Fatalf("PolicyEngine has %d methods, want exactly Evaluate", seam.NumMethod())
	}
	method, ok := seam.MethodByName("Evaluate")
	if !ok {
		t.Fatal("PolicyEngine has no Evaluate method")
	}
	fn := method.Type
	if fn.NumIn() != 2 || fn.NumOut() != 2 {
		t.Fatalf("Evaluate has %d in / %d out, want 2 and 2", fn.NumIn(), fn.NumOut())
	}
	if fn.In(0) != reflect.TypeOf((*context.Context)(nil)).Elem() {
		t.Fatalf("Evaluate's first argument is %v, want context.Context", fn.In(0))
	}
	if fn.In(1) != reflect.TypeOf(policy.EvalRequest{}) {
		t.Fatalf("Evaluate's second argument is %v, want policy.EvalRequest", fn.In(1))
	}
	if fn.Out(0) != reflect.TypeOf(policy.EvalOutcome{}) {
		t.Fatalf("Evaluate's first result is %v, want policy.EvalOutcome", fn.Out(0))
	}
	if !reflect.TypeOf((*policy.Engine)(nil)).Implements(seam) {
		t.Fatal("*policy.Engine does not satisfy the seam the router calls")
	}
}
