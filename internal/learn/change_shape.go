package learn

// Purpose: restrict proposal edits to add, reorder and parameterise shapes.
// Inputs: a closed proposal kind, target id or alias, and a Change.
// Outputs: the authority's bound verdict or a typed refusal.
// Constraints: ClassifyChange is the sole tier and bound authority.
// SPORT: internal.learn.ValidateChangeShape/ADDED (P1-LRN-02a).

import "github.com/acamarata/cascade/pkg/cascade"

// ShapeVerdict reports the authority's tighten-only bound decision.
type ShapeVerdict struct {
	LoosensBound bool
}

// ValidateChangeShape accepts only add_step, reorder and bounded set edits.
// Removal and unknown operations are policy denied. Security classifications,
// including out-of-bound values, refuse with the authority's bound flag intact.
func ValidateChangeShape(kind ProposedChangeKind, target string, ch Change) (ShapeVerdict, error) {
	switch kind {
	case ProposedRule, ProposedPolicy, ProposedWorkflow, ProposedCI:
	default:
		return ShapeVerdict{}, cascade.New(cascade.KindInvalidInput, "learn: unknown proposal kind")
	}
	switch ch.Op {
	case OpAddStep, OpReorder, OpSet:
	default:
		return ShapeVerdict{}, cascade.New(cascade.KindPolicyDenied, "learn: proposal operation is not add, reorder or parameterise")
	}
	cls, err := ClassifyChange(target, ch)
	if err != nil {
		return ShapeVerdict{}, err
	}
	verdict := ShapeVerdict{LoosensBound: cls.LoosensBound}
	if cls.Tier == TierSecurity {
		return verdict, cascade.New(cascade.KindPolicyDenied, "learn: security-classified proposal change refused")
	}
	return verdict, nil
}
