// Purpose: the stack driver's own properties — one Evaluate shape, one
// classification per evaluation, fail-closed on unclassifiable input, and
// the R-21.231 rule that a same-turn hit still faces the elevation check.
//
// SPORT: internal/policy policy-engine/ADDED (P1-E09-W2-S17-T2).
package policy

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// countingClassifier wraps the real classifier and counts calls, so a
// second classification anywhere in the stack is visible.
type countingClassifier struct {
	inner CommandClassifier
	calls int
}

func (c *countingClassifier) Classify(ctx context.Context, cmd string) (RiskLevel, error) {
	c.calls++
	return c.inner.Classify(ctx, cmd)
}

// TestEvaluateClassifiesExactlyOnce pins R-14.26's "resolved once before
// evaluation": the layers consume the rung and never re-derive it.
func TestEvaluateClassifiesExactlyOnce(t *testing.T) {
	f := layerFixture(t)
	spy := &countingClassifier{inner: NewCommandClassifier()}
	f.engine.classifier = spy
	if _, err := f.engine.Evaluate(context.Background(), readRequest(L2)); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if spy.calls != 1 {
		t.Errorf("the stack classified %d times, want exactly 1", spy.calls)
	}
}

// TestEvaluateIsTheOnlyShape is the compile-time-equivalent assertion the
// contract asks for: internal/policy declares exactly one Evaluate, with
// the one signature, and its request type carries no pre-resolved rung.
func TestEvaluateIsTheOnlyShape(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading internal/policy: %v", err)
	}
	fset := token.NewFileSet()
	found := 0
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		found += countEvaluate(t, file)
	}
	if found != 1 {
		t.Errorf("internal/policy declares %d Evaluate functions, want exactly 1", found)
	}
	assertNoLevelField(t)
}

// countEvaluate checks every Evaluate declaration in one file against the
// single permitted shape and returns how many it saw.
func countEvaluate(t *testing.T, file *ast.File) int {
	t.Helper()
	seen := 0
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Evaluate" {
			continue
		}
		seen++
		if got := fn.Type.Params.NumFields(); got != 2 {
			t.Errorf("Evaluate takes %d parameter groups, want (ctx, EvalRequest)", got)
		}
		if got := fn.Type.Results.NumFields(); got != 2 {
			t.Errorf("Evaluate returns %d values, want (EvalOutcome, error)", got)
		}
	}
	return seen
}

// assertNoLevelField proves no caller can supply a rung: EvalRequest
// declares no RiskLevel field, so a pre-resolved level has nowhere to
// travel and the evaluator is the only place a rung is decided.
func assertNoLevelField(t *testing.T) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "engine.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing engine.go: %v", err)
	}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.TypeSpec)
		if !ok || spec.Name.Name != "EvalRequest" {
			return true
		}
		st, ok := spec.Type.(*ast.StructType)
		if !ok {
			return false
		}
		for _, field := range st.Fields.List {
			ident, ok := field.Type.(*ast.Ident)
			if ok && ident.Name == "RiskLevel" {
				t.Errorf("EvalRequest carries a caller-supplied %s field", ident.Name)
			}
		}
		return false
	})
}

// TestSameTurnHitStillFacesElevation is R-21.231: a same-turn
// authorization overrides the deny-list ENTRY and nothing else. The
// evaluation continues to layer 2, where an elevated verb with no fresh
// attestation is still denied.
func TestSameTurnHitStillFacesElevation(t *testing.T) {
	ctx := context.Background()
	f := layerFixture(t)
	f.engine.WithSameTurnAuthorizer(alwaysSameTurn{})
	req := readRequest(L4)
	req.Verb = "vault.get"

	out, err := f.engine.Evaluate(ctx, req)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if out.Verdict != VerdictDeny || out.Layer != LayerElevation {
		t.Fatalf("outcome = %+v, want a layer 2 deny", out)
	}
	if len(out.Trace.Layers) < 2 || out.Trace.Layers[1].Layer != LayerDenyList {
		t.Fatalf("the trace does not record layer 1: %s", out.Trace.Explanation)
	}
	entry := out.Trace.Layers[1]
	if entry.Decided || !strings.Contains(entry.Rule, "same-turn") {
		t.Errorf("layer 1 = %+v, want an undecided same-turn override", entry)
	}
	// And the override is not a licence: with a verifier attached but no
	// matching nonce, the answer is still a refusal.
	f.engine.WithElevationVerifier(refusingElevation{})
	req.ElevationNonce = "not-a-live-nonce"
	if out, err = f.engine.Evaluate(ctx, req); err != nil || out.Verdict != VerdictDeny {
		t.Errorf("a refused attestation yielded %+v (%v), want deny", out, err)
	}
}

// TestEvaluateFailsClosedOnUnclassifiableInput covers the classifier's
// refusal path and the missing-classifier path: both are the top rung, and
// neither is reported to the caller as an error, because an unclassifiable
// action IS a classification.
func TestEvaluateFailsClosedOnUnclassifiableInput(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		build func(f *engineFixture) EvalRequest
	}{
		{"input the shell grammar rejects", func(*engineFixture) EvalRequest {
			return readRequest(0)
		}},
		{"an empty action", func(*engineFixture) EvalRequest {
			req := readRequest(L0)
			req.Action = ""
			return req
		}},
		{"no classifier attached", func(f *engineFixture) EvalRequest {
			f.engine.classifier = nil
			return readRequest(L0)
		}},
	}
	for _, tc := range cases {
		f := layerFixture(t)
		out, err := f.engine.Evaluate(ctx, tc.build(f))
		if err != nil {
			t.Errorf("%s: a verdict was reported as an error: %v", tc.name, err)
		}
		if out.Verdict != VerdictDeny || out.Level != L4 {
			t.Errorf("%s: outcome = %+v, want a deny at L4", tc.name, out)
		}
	}
}
