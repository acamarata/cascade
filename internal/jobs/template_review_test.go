package jobs

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/pkg/cascade"
)

type templateReviewPolicy struct{ require, hold bool }

func (p templateReviewPolicy) RequiresDistinctFamily() bool     { return p.require }
func (p templateReviewPolicy) HoldsWithoutDistinctFamily() bool { return p.hold }

func validReviewDispatch() *ReviewDispatch {
	return &ReviewDispatch{
		Policy: templateReviewPolicy{require: true},
		Author: FamilyRef{Family: "acme-corp", Resolved: true},
		Eligible: []FamilyRef{
			{Family: "acme-corp", Resolved: true},
			{Family: "partner-labs", Resolved: true},
		},
		Subject: ReviewSubject{JobID: "subject-job", NodeID: "subject-node", BaseTree: "base", Diff: "diff"},
	}
}

func reviewTemplateKinds() []string {
	return []string{TemplateKindReview, TemplateKindAdversarial, TemplateKindQA}
}

type templateAssignmentRecord struct {
	Author         FamilyRef   `json:"author"`
	Eligible       []FamilyRef `json:"eligible"`
	FamilyDistinct bool        `json:"family_distinct"`
	Degraded       bool        `json:"degraded"`
}

func readTemplateAssignment(t *testing.T, node DagNode) templateAssignmentRecord {
	t.Helper()
	var assignment templateAssignmentRecord
	if err := json.Unmarshal([]byte(node.NodeRequirements["reviewer_assignment"]), &assignment); err != nil {
		t.Fatal(err)
	}
	return assignment
}

func TestReviewTemplatesEnforceReviewerPolicy(t *testing.T) {
	for _, kind := range reviewTemplateKinds() {
		t.Run(kind, func(t *testing.T) {
			dispatch := validReviewDispatch()
			dispatch.Subject.Files = []ReviewFile{{Path: "src/main.go", Content: "code"}, {Path: "AGENTS.md", Content: "instructions"}}
			reqs := map[string]string{"gpu": "true", "reviewer_family_distinct": "forged"}
			before := map[string]string{"gpu": "true", "reviewer_family_distinct": "forged"}
			tmpl, err := TemplateRegistry.By(kind)
			if err != nil {
				t.Fatal(err)
			}
			node, err := tmpl.Resolve(withTC(TemplateContext{
				ID: "review-node", Footprint: []string{"src/**"}, Review: dispatch,
				PassThroughFields: PassThroughFields{NodeRequirements: reqs},
			}))
			if err != nil {
				t.Fatal(err)
			}
			if len(node.MutableScope) != 0 || node.NodeRequirements["reviewer_family_distinct"] != "true" {
				t.Fatalf("unexpected review node: %+v", node)
			}
			var clean ReviewContext
			if err := json.Unmarshal([]byte(node.NodeRequirements["review_context"]), &clean); err != nil {
				t.Fatal(err)
			}
			want, err := BuildCleanReviewContext(dispatch.Subject.JobID, dispatch.Subject.NodeID, dispatch.Subject.BaseTree, dispatch.Subject.Diff, dispatch.Subject.Files)
			if err != nil || !reflect.DeepEqual(clean, want) {
				t.Fatalf("review context = %+v, want %+v; err=%v", clean, want, err)
			}
			assignment := readTemplateAssignment(t, node)
			if assignment.Author != dispatch.Author || !reflect.DeepEqual(assignment.Eligible, dispatch.Eligible) || !assignment.FamilyDistinct || assignment.Degraded {
				t.Fatalf("unexpected assignment: %+v", assignment)
			}
			if node.NodeRequirements["gpu"] != "true" || !reflect.DeepEqual(reqs, before) {
				t.Fatalf("caller requirements changed: %v; node=%v", reqs, node.NodeRequirements)
			}
		})
	}
}

func TestReviewTemplatesRefuseWithoutDispatch(t *testing.T) {
	for _, kind := range reviewTemplateKinds() {
		t.Run(kind, func(t *testing.T) {
			tmpl, err := TemplateRegistry.By(kind)
			if err != nil {
				t.Fatal(err)
			}
			node, err := tmpl.Resolve(withTC(TemplateContext{ID: "review-node"}))
			if !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(err.Error(), kind+" template needs a review dispatch") || !reflect.DeepEqual(node, DagNode{}) {
				t.Fatalf("got node=%+v err=%v", node, err)
			}
		})
	}
}

func TestReviewTemplatesHoldUsesInjectedPolicy(t *testing.T) {
	for _, kind := range reviewTemplateKinds() {
		t.Run(kind, func(t *testing.T) {
			dispatch := validReviewDispatch()
			dispatch.Eligible = []FamilyRef{dispatch.Author}
			tc := TemplateContext{ID: "review-node", Review: dispatch}
			tmpl, err := TemplateRegistry.By(kind)
			if err != nil {
				t.Fatal(err)
			}
			original := readOnlyNode(tc, conductor.TaskClassReview, nil, nil)
			for _, policy := range []ReviewPolicySource{templateReviewPolicy{true, true}, templateReviewPolicy{true, false}, nil} {
				dispatch.Policy = policy
				node, err := tmpl.Resolve(withTC(tc))
				if policy == nil || policy.HoldsWithoutDistinctFamily() {
					if err != ErrNoDistinctFamily || !reflect.DeepEqual(node, DagNode{}) {
						t.Fatalf("hold: node=%+v err=%v", node, err)
					}
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				assignment := readTemplateAssignment(t, node)
				if node.RiskClass != original.RiskClass || node.NodeRequirements["reviewer_family_distinct"] != "false" || assignment.FamilyDistinct || !assignment.Degraded {
					t.Fatalf("fallback: node=%+v assignment=%+v", node, assignment)
				}
			}
		})
	}
}

func TestEnforceReviewRefusesMutableNode(t *testing.T) {
	tc := TemplateContext{ID: "review-node", Footprint: []string{"src/**"}, Review: validReviewDispatch()}
	node := readOnlyNode(tc, conductor.TaskClassReview, nil, nil)
	node.MutableScope = tc.Footprint
	got, err := enforceReview(tc, TemplateKindReview, node)
	if err != ErrMutableReview || !reflect.DeepEqual(got, DagNode{}) {
		t.Fatalf("got node=%+v err=%v", got, err)
	}
}

func TestReviewTemplatesRefuseBadSubject(t *testing.T) {
	for _, kind := range reviewTemplateKinds() {
		t.Run(kind, func(t *testing.T) {
			dispatch := validReviewDispatch()
			dispatch.Subject.JobID = ""
			tmpl, err := TemplateRegistry.By(kind)
			if err != nil {
				t.Fatal(err)
			}
			node, err := tmpl.Resolve(withTC(TemplateContext{ID: "review-node", Review: dispatch}))
			if err != ErrReviewContextIDs || !reflect.DeepEqual(node, DagNode{}) {
				t.Fatalf("got node=%+v err=%v", node, err)
			}
		})
	}
}
