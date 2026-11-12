package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestReviewerSentinels checks the canonical policy and assignment refusals.
func TestReviewerSentinels(t *testing.T) {
	for _, tc := range []struct {
		err  error
		kind cascade.Kind
		msg  string
	}{
		{ErrReviewerMutatedWorktree, cascade.KindIntegrity, "integrity: jobs: reviewer mutated the worktree"},
		{ErrMutableReview, cascade.KindPolicyDenied, "policy-denied: jobs: read-only review node holds a mutable lease scope"},
		{ErrNoDistinctFamily, cascade.KindElevationRequired, "elevation-required: jobs: no distinct reviewer family"},
	} {
		if !cascade.HasKind(tc.err, tc.kind) || tc.err.Error() != tc.msg {
			t.Errorf("sentinel = %v, want kind %v and message %q", tc.err, tc.kind, tc.msg)
		}
	}
}

// TestReviewTemplate_RealWiring resolves a registered review template using a
// real fenced worktree snapshot and checks the resulting dispatch evidence.
func TestReviewTemplate_RealWiring(t *testing.T) {
	wm, lease, _ := reviewerTreeFixture(t)
	ctx := context.Background()
	before, err := wm.ReviewerTreeForJob(ctx, lease.Holder)
	if err != nil {
		t.Fatal(err)
	}
	dispatch := &ReviewDispatch{
		Policy:   templateReviewPolicy{require: true, hold: true},
		Author:   FamilyRef{Family: "author-family", Resolved: true},
		Eligible: []FamilyRef{{Family: "reviewer-family", Resolved: true}},
		Subject: ReviewSubject{
			JobID: lease.Holder, NodeID: "implementation", BaseTree: before,
			Diff: "diff --git a/main.go b/main.go",
			Files: []ReviewFile{
				{Path: "main.go", Content: "package main"},
				{Path: "AGENTS.md", Content: "private instructions"},
			},
		},
	}
	tmpl, err := TemplateRegistry.By(TemplateKindReview)
	if err != nil {
		t.Fatal(err)
	}
	node, err := tmpl.Resolve(WithTemplateContext(ctx, TemplateContext{
		ID: "review", DependsOn: []string{"implementation"}, Review: dispatch,
		Footprint: []string{"main.go"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if node.ID != "review" || len(node.Deps) != 1 || node.Deps[0] != "implementation" || node.MinTaskClass != conductor.TaskClassReview {
		t.Fatalf("unexpected review node: %+v", node)
	}
	if err := EnforceReadOnlyLease(TemplateKindReview, node); err != nil {
		t.Fatal(err)
	}
	assertCleanReviewContext(t, node, lease.Holder, before, dispatch.Subject.Diff)
	assignment := readTemplateAssignment(t, node)
	if assignment.Author != dispatch.Author || len(assignment.Eligible) != 1 || assignment.Eligible[0] != dispatch.Eligible[0] || !assignment.FamilyDistinct || assignment.Degraded || node.NodeRequirements["reviewer_family_distinct"] != "true" {
		t.Fatalf("unexpected assignment: %+v", assignment)
	}
	after, err := wm.ReviewerTreeForJob(ctx, lease.Holder)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyReadOnly(before, after); err != nil {
		t.Fatal(err)
	}
}

// assertCleanReviewContext checks the review_context requirement holds the
// subject with the instruction file filtered out.
func assertCleanReviewContext(t *testing.T, node DagNode, jobID, base, diff string) {
	t.Helper()
	var clean ReviewContext
	if err := json.Unmarshal([]byte(node.NodeRequirements["review_context"]), &clean); err != nil {
		t.Fatal(err)
	}
	if clean.JobID != jobID || clean.NodeID != "implementation" || clean.BaseTree != base || clean.Diff != diff {
		t.Fatalf("unexpected review subject: %+v", clean)
	}
	if len(clean.Files) != 1 || clean.Files[0].Path != "main.go" || clean.Files[0].Content != "package main" {
		t.Fatalf("unexpected review files: %+v", clean.Files)
	}
}

// TestTemplateRegistry_Completeness asserts By resolves exactly the
// seven registered kinds (the original six plus AH/S-70.T2's release
// gate) and nothing else -- the registry-completeness acceptance
// criterion.
func TestTemplateRegistry_Completeness(t *testing.T) {
	kinds := []string{
		TemplateKindImplement, TemplateKindReview, TemplateKindAdversarial,
		TemplateKindQA, TemplateKindCI, TemplateKindIntegrate, TemplateKindRelease,
	}
	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			tmpl, err := TemplateRegistry.By(kind)
			if err != nil {
				t.Fatalf("By(%q): unexpected error %v", kind, err)
			}
			if tmpl == nil {
				t.Fatalf("By(%q): nil template, nil error", kind)
			}
		})
	}
}

// TestTemplateRegistry_UnknownKind asserts By returns the typed
// ErrUnknownTemplateKind for any kind outside the six, never a panic
// and never a (nil, nil) return.
func TestTemplateRegistry_UnknownKind(t *testing.T) {
	for _, kind := range []string{"", "bogus", "IMPLEMENT", "review "} {
		tmpl, err := TemplateRegistry.By(kind)
		if !errors.Is(err, ErrUnknownTemplateKind) {
			t.Fatalf("By(%q): got err %v, want ErrUnknownTemplateKind", kind, err)
		}
		if tmpl != nil {
			t.Fatalf("By(%q): got non-nil template alongside an error", kind)
		}
	}
}

// TestTemplateRegistry_RegisterIsAdditive asserts Register can add a
// brand-new kind without disturbing the six already registered --
// AH/S-69.T2's own extension seam.
func TestTemplateRegistry_RegisterIsAdditive(t *testing.T) {
	TemplateRegistry.Register("probe-kind-for-test", ReviewTemplate{})
	defer func() {
		TemplateRegistry.mu.Lock()
		delete(TemplateRegistry.kinds, "probe-kind-for-test")
		TemplateRegistry.mu.Unlock()
	}()

	if _, err := TemplateRegistry.By("probe-kind-for-test"); err != nil {
		t.Fatalf("By(probe-kind-for-test): %v", err)
	}
	if _, err := TemplateRegistry.By(TemplateKindImplement); err != nil {
		t.Fatalf("registering a new kind disturbed an existing one: %v", err)
	}
}

// TestRequireTemplateContext_NilContext asserts the nil-context error
// path: Resolve must refuse rather than panic on ctx.Value.
func TestRequireTemplateContext_NilContext(t *testing.T) {
	tmpl := NewImplementTemplate(nil)
	//nolint:staticcheck // deliberately passing a nil context.Context to exercise the guard.
	_, err := tmpl.Resolve(nil)
	if err == nil {
		t.Fatal("Resolve(nil): expected an error, got nil")
	}
}

// TestRequireTemplateContext_MissingContext asserts a ctx with no
// attached TemplateContext also refuses, rather than resolving a
// zero-value DagNode.
func TestRequireTemplateContext_MissingContext(t *testing.T) {
	tmpl := ReviewTemplate{}
	_, err := tmpl.Resolve(context.Background())
	if err == nil {
		t.Fatal("Resolve with no TemplateContext: expected an error, got nil")
	}
}

// TestRequireTemplateContext_EmptyID asserts a TemplateContext with no
// id also refuses.
func TestRequireTemplateContext_EmptyID(t *testing.T) {
	ctx := WithTemplateContext(context.Background(), TemplateContext{})
	if _, err := (QaTemplate{}).Resolve(ctx); err == nil {
		t.Fatal("Resolve with empty TemplateContext.ID: expected an error, got nil")
	}
}

// TestReleaseTemplate_Resolve asserts ReleaseTemplate.Resolve is fixed at
// RiskClassCritical (never classifier-derived: the same TemplateContext
// footprint that would classify Low elsewhere must not move this), with
// mutable_scope=nil, min_task_class=arbitrate, and node_requirements
// empty even when the caller's own TemplateContext carries values in
// every one of those fields -- proving Resolve drops them rather than
// passing them through.
func TestReleaseTemplate_Resolve(t *testing.T) {
	ctx := WithTemplateContext(context.Background(), TemplateContext{
		ID:        "release-1",
		Footprint: []string{"docs/**"}, // would classify Low if it were consulted
		DependsOn: []string{"ci-1"},
		PassThroughFields: PassThroughFields{
			Capabilities:     []string{"release"},
			NodeRequirements: map[string]string{"clean-room": "true"},
			Timeout:          5,
			CostCeiling:      1.5,
			Priority:         2,
		},
	})
	node, err := (ReleaseTemplate{}).Resolve(ctx)
	if err != nil {
		t.Fatalf("Resolve: unexpected error %v", err)
	}
	if node.RiskClass != RiskClassCritical {
		t.Fatalf("RiskClass = %q, want %q (fixed, never classifier-derived)", node.RiskClass, RiskClassCritical)
	}
	if node.MutableScope != nil {
		t.Fatalf("MutableScope = %v, want nil", node.MutableScope)
	}
	if node.MinTaskClass != conductor.TaskClassArbitrate {
		t.Fatalf("MinTaskClass = %q, want %q", node.MinTaskClass, conductor.TaskClassArbitrate)
	}
	if node.NodeRequirements != nil {
		t.Fatalf("NodeRequirements = %v, want nil (no R-16.37 executor capability applies)", node.NodeRequirements)
	}
	if node.ID != "release-1" || len(node.Deps) != 1 || node.Deps[0] != "ci-1" {
		t.Fatalf("ID/Deps pass-through broken: got ID=%q Deps=%v", node.ID, node.Deps)
	}
	if len(node.Capabilities) != 1 || node.Capabilities[0] != "release" {
		t.Fatalf("Capabilities pass-through broken: got %v", node.Capabilities)
	}
}

// TestReleaseTemplate_MissingContext asserts the shared guard refuses
// rather than resolving a zero-value DagNode.
func TestReleaseTemplate_MissingContext(t *testing.T) {
	if _, err := (ReleaseTemplate{}).Resolve(context.Background()); err == nil {
		t.Fatal("Resolve with no TemplateContext: expected an error, got nil")
	}
}

// TestMergeCapabilities asserts the shared helper de-duplicates and
// preserves base order.
func TestMergeCapabilities(t *testing.T) {
	got := mergeCapabilities([]string{"a", "b"}, "b", "c")
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("mergeCapabilities: got %v, want %v", got, want)
	}
	for i, v := range want {
		if got[i] != v {
			t.Fatalf("mergeCapabilities: got %v, want %v", got, want)
		}
	}
}
