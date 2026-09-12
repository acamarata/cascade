package jobs

// Purpose: the six typed job-template kinds (19 §Epic AC S-60.T2) --
//
//	implement, review, adversarial, qa, ci, integrate -- each a
//	JobTemplate.Resolve real implementation, registered into
//	TemplateRegistry by this file's own init().
//
// Inputs: the TemplateContext carried on each Resolve call's ctx
//
//	(template.go); ImplementTemplate/IntegrateTemplate additionally
//	close over a FootprintClassifier at construction.
//
// Outputs: a DagNode per kind, carrying ONLY the DECIDED S-59.T4 field
//
//	set -- this file adds no field to dag.go.
//
// Constraints: review/adversarial/qa/ci set MutableScope to the EMPTY
//
//	glob set (read-only kinds; R-16.37 AC constants) regardless of what
//	TemplateContext.Footprint carries -- a caller-supplied footprint for
//	a read-only kind is dropped, never silently applied as mutable
//	scope. capabilities=["review", ...] hints are additive over the
//	caller's own Capabilities (mergeCapabilities), never a replacement.
//
// SPORT: jobs/job-templates/ADD (P1-E29-W6-S60-T2).

import (
	"context"

	"github.com/acamarata/cascade/internal/conductor"
)

// The six DECIDED template kind names (19 §Epic AC S-60.T2), plus the
// seventh AH/S-70.T2 adds below. TemplateRegistry.By resolves exactly
// these seven and ErrUnknownTemplateKind for anything else.
const (
	TemplateKindImplement   = "implement"
	TemplateKindReview      = "review"
	TemplateKindAdversarial = "adversarial"
	TemplateKindQA          = "qa"
	TemplateKindCI          = "ci"
	TemplateKindIntegrate   = "integrate"
	// TemplateKindRelease is the seventh kind (19 §Epic AH S-70.T2): a
	// release/CD gate, Critical class by declaration (R-16.13), never
	// classifier-derived. No alias ("release-gate" included) is
	// registered -- 06 §5.1 never-invent-scope names exactly this one.
	TemplateKindRelease = "release"
)

func init() {
	TemplateRegistry.Register(TemplateKindImplement, NewImplementTemplate(nil))
	TemplateRegistry.Register(TemplateKindReview, ReviewTemplate{})
	TemplateRegistry.Register(TemplateKindAdversarial, AdversarialTemplate{})
	TemplateRegistry.Register(TemplateKindQA, QaTemplate{})
	TemplateRegistry.Register(TemplateKindCI, CiTemplate{})
	TemplateRegistry.Register(TemplateKindIntegrate, NewIntegrateTemplate(nil))
	TemplateRegistry.Register(TemplateKindRelease, ReleaseTemplate{})
}

// ReleaseTemplate resolves the "release" kind: a Critical-class release/
// CD gate node. It is stateless -- see release_gate.go's header CONTRACT
// DEVIATION note for why it holds no ApprovalQueue/RollbackEvidence
// fields despite this ticket's contract text naming them on the type:
// Resolve's output never varies by either, and the separate
// RequireHumanApproval function (release_gate.go) takes both as direct
// parameters instead.
type ReleaseTemplate struct{}

// Resolve implements JobTemplate. Unlike readOnlyNode's four kinds,
// MutableScope is nil for a DIFFERENT reason than "read-only": a release
// gate approves or blocks an artifact an earlier ci/integrate node
// already built, so it has no footprint of its own to declare, mutable or
// not. RiskClass is FIXED at RiskClassCritical -- never classifier-
// derived, matching Epic AH's DECIDED "Release/CD gate = Critical class"
// rule -- and NodeRequirements is always empty: R-16.37 fixes the
// executor-capability name set (clean-room, docker, xcode, gpu plus
// toolchain fingerprint), none of which a release gate needs, and the
// human-approval requirement is expressed by RiskClassCritical's own gate
// set (riskgates.go's GateHumanApproval/GateRollbackEvidence/
// GateReleaseGate), not by an invented node requirement.
func (ReleaseTemplate) Resolve(ctx context.Context) (DagNode, error) {
	tc, err := requireTemplateContext(ctx)
	if err != nil {
		return DagNode{}, err
	}
	return DagNode{
		ID:               tc.ID,
		Capabilities:     tc.Capabilities,
		Deps:             tc.DependsOn,
		MutableScope:     nil,
		RiskClass:        RiskClassCritical,
		MinTaskClass:     conductor.TaskClassArbitrate,
		NodeRequirements: nil,
		Timeout:          tc.Timeout,
		CostCeiling:      tc.CostCeiling,
		Priority:         tc.Priority,
	}, nil
}

// ImplementTemplate resolves the "implement" kind: mutable_scope is the
// ticket footprint globs, min_task_class=code, risk_class comes from
// the injected classifier, and no DAG deps are pre-wired beyond the
// caller's own DependsOn.
type ImplementTemplate struct {
	classify FootprintClassifier
}

// NewImplementTemplate constructs an ImplementTemplate. A nil classify
// installs the production defaultClassifier (S-59.T4's classifyFootprint).
func NewImplementTemplate(classify FootprintClassifier) *ImplementTemplate {
	if classify == nil {
		classify = defaultClassifier
	}
	return &ImplementTemplate{classify: classify}
}

// Resolve implements JobTemplate.
func (t *ImplementTemplate) Resolve(ctx context.Context) (DagNode, error) {
	tc, err := requireTemplateContext(ctx)
	if err != nil {
		return DagNode{}, err
	}
	return DagNode{
		ID:               tc.ID,
		Capabilities:     tc.Capabilities,
		Deps:             tc.DependsOn,
		MutableScope:     tc.Footprint,
		RiskClass:        t.classify(tc.Footprint),
		MinTaskClass:     conductor.TaskClassCode,
		NodeRequirements: tc.NodeRequirements,
		Timeout:          tc.Timeout,
		CostCeiling:      tc.CostCeiling,
		Priority:         tc.Priority,
	}, nil
}

// readOnlyNode builds the shared shape every read-only kind (review,
// adversarial, qa, ci) produces: MutableScope is always the empty glob
// set, regardless of what the caller's TemplateContext.Footprint
// carried -- these kinds never touch files, so a declared footprint
// would be a lie about what the node does.
func readOnlyNode(tc TemplateContext, minTaskClass conductor.TaskClass, extraCaps []string, extraNodeReqs map[string]string) DagNode {
	nodeReqs := tc.NodeRequirements
	if len(extraNodeReqs) > 0 {
		merged := make(map[string]string, len(nodeReqs)+len(extraNodeReqs))
		for k, v := range nodeReqs {
			merged[k] = v
		}
		for k, v := range extraNodeReqs {
			merged[k] = v
		}
		nodeReqs = merged
	}
	return DagNode{
		ID:               tc.ID,
		Capabilities:     mergeCapabilities(tc.Capabilities, extraCaps...),
		Deps:             tc.DependsOn,
		MutableScope:     nil,
		RiskClass:        RiskClassNormal,
		MinTaskClass:     minTaskClass,
		NodeRequirements: nodeReqs,
		Timeout:          tc.Timeout,
		CostCeiling:      tc.CostCeiling,
		Priority:         tc.Priority,
	}
}

// ReviewTemplate resolves the "review" kind: read-only, min_task_class
// =review, capabilities carry the preferred-family hint
// ("family:distinct-from-author"); AH/S-69.T2 owns ENFORCEMENT of that
// hint, not this ticket.
type ReviewTemplate struct{}

// Resolve implements JobTemplate.
func (ReviewTemplate) Resolve(ctx context.Context) (DagNode, error) {
	tc, err := requireTemplateContext(ctx)
	if err != nil {
		return DagNode{}, err
	}
	return readOnlyNode(tc, conductor.TaskClassReview, []string{"review", "family:distinct-from-author"}, nil), nil
}

// AdversarialTemplate resolves the "adversarial" kind: read-only,
// min_task_class=review, capabilities carry the adversarial hint.
type AdversarialTemplate struct{}

// Resolve implements JobTemplate.
func (AdversarialTemplate) Resolve(ctx context.Context) (DagNode, error) {
	tc, err := requireTemplateContext(ctx)
	if err != nil {
		return DagNode{}, err
	}
	return readOnlyNode(tc, conductor.TaskClassReview, []string{"review", "adversarial"}, nil), nil
}

// QaTemplate resolves the "qa" kind: read-only, min_task_class=review,
// no capability hint beyond the caller's own.
type QaTemplate struct{}

// Resolve implements JobTemplate.
func (QaTemplate) Resolve(ctx context.Context) (DagNode, error) {
	tc, err := requireTemplateContext(ctx)
	if err != nil {
		return DagNode{}, err
	}
	return readOnlyNode(tc, conductor.TaskClassReview, nil, nil), nil
}

// ciCleanRoomKey is the NodeRequirements key CiTemplate sets. The value
// is deliberately "true", matching this map[string]string field's
// string-valued shape (dag.go); there is no boolean NodeRequirements
// variant to conform to instead.
const ciCleanRoomKey = "clean-room"

// CiTemplate resolves the "ci" kind: read-only, min_task_class=code
// (CI still runs build/lint/test, code-class work, even though it
// touches no files), node_requirements names the clean-room
// requirement.
type CiTemplate struct{}

// Resolve implements JobTemplate.
func (CiTemplate) Resolve(ctx context.Context) (DagNode, error) {
	tc, err := requireTemplateContext(ctx)
	if err != nil {
		return DagNode{}, err
	}
	return readOnlyNode(tc, conductor.TaskClassCode, nil, map[string]string{ciCleanRoomKey: "true"}), nil
}

// IntegrateTemplate resolves the "integrate" kind: mutable_scope is the
// target subtree glob, min_task_class=code, risk_class defaults to
// Normal but the injected classifier may elevate it at plan time --
// never below Normal, since integrate always touches the target
// subtree.
type IntegrateTemplate struct {
	classify FootprintClassifier
}

// NewIntegrateTemplate constructs an IntegrateTemplate. A nil classify
// installs the production defaultClassifier.
func NewIntegrateTemplate(classify FootprintClassifier) *IntegrateTemplate {
	if classify == nil {
		classify = defaultClassifier
	}
	return &IntegrateTemplate{classify: classify}
}

// Resolve implements JobTemplate.
func (t *IntegrateTemplate) Resolve(ctx context.Context) (DagNode, error) {
	tc, err := requireTemplateContext(ctx)
	if err != nil {
		return DagNode{}, err
	}
	class := RiskClassNormal
	if classified := t.classify(tc.Footprint); riskRank[classified] > riskRank[RiskClassNormal] {
		class = classified
	}
	return DagNode{
		ID:               tc.ID,
		Capabilities:     tc.Capabilities,
		Deps:             tc.DependsOn,
		MutableScope:     tc.Footprint,
		RiskClass:        class,
		MinTaskClass:     conductor.TaskClassCode,
		NodeRequirements: tc.NodeRequirements,
		Timeout:          tc.Timeout,
		CostCeiling:      tc.CostCeiling,
		Priority:         tc.Priority,
	}, nil
}
