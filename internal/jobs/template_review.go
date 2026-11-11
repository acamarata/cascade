package jobs

import (
	"encoding/json"
	"strconv"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ReviewSubject carries the inputs to BuildCleanReviewContext.
type ReviewSubject struct {
	JobID, NodeID, BaseTree, Diff string
	Files                         []ReviewFile
}

// ReviewDispatch supplies the policy, resolved families and subject for review.
type ReviewDispatch struct {
	Policy   ReviewPolicySource
	Author   FamilyRef
	Eligible []FamilyRef
	Subject  ReviewSubject
}

type reviewAssignment struct {
	Author         FamilyRef   `json:"author"`
	Eligible       []FamilyRef `json:"eligible"`
	FamilyDistinct bool        `json:"family_distinct"`
	Degraded       bool        `json:"degraded"`
}

// enforceReview validates a read-only node and attaches an isolated context
// and assignment. Only the injected policy decides whether dispatch may proceed.
func enforceReview(tc TemplateContext, kind string, node DagNode) (DagNode, error) {
	if tc.Review == nil {
		return DagNode{}, cascade.New(cascade.KindInvalidInput, "jobs: "+kind+" template needs a review dispatch")
	}
	if err := EnforceReadOnlyLease(kind, node); err != nil {
		return DagNode{}, err
	}
	dispatch := tc.Review
	subject := dispatch.Subject
	clean, err := BuildCleanReviewContext(subject.JobID, subject.NodeID, subject.BaseTree, subject.Diff, subject.Files)
	if err != nil {
		return DagNode{}, err
	}
	degraded, err := RequireDistinctFamily(dispatch.Policy, dispatch.Author, dispatch.Eligible)
	if err != nil {
		return DagNode{}, err
	}
	distinct := false
	for _, eligible := range dispatch.Eligible {
		if familiesDistinct(dispatch.Author, eligible) {
			distinct = true
			break
		}
	}
	assignment := reviewAssignment{dispatch.Author, dispatch.Eligible, distinct, degraded}
	node.NodeRequirements, err = reviewRequirements(node.NodeRequirements, clean, assignment)
	if err != nil {
		return DagNode{}, err
	}
	return node, nil
}

// reviewRequirements copies caller requirements before adding dispatch evidence.
func reviewRequirements(existing map[string]string, clean ReviewContext, assignment reviewAssignment) (map[string]string, error) {
	requirements := make(map[string]string, len(existing)+3)
	for key, value := range existing {
		requirements[key] = value
	}
	for key, value := range map[string]any{"review_context": clean, "reviewer_assignment": assignment} {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		requirements[key] = string(encoded)
	}
	requirements["reviewer_family_distinct"] = strconv.FormatBool(assignment.FamilyDistinct)
	return requirements, nil
}
