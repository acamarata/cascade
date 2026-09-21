// Purpose: the R-21.156/R-21.191 BLIND request internal/review builds for
//   its OWN conductor dispatch (P1-E25-W5-S52-T4): BlindRequest, whose only
//   fields are the checkpoint id, the level rubric, the artifact under
//   review and the consequence class -- author identity, authoring lane,
//   prior verdicts and expected outcome are not fields of it and are
//   structurally unreachable from it, per R-21.156(a). Also Plan, which
//   pairs that blind request with the two derived facts a dispatch needs
//   and the blind set may not carry: the AMD-20260916/6 checklist parsed out
//   of the caller's Context, and the R-21.191 exclusion notes.
// Inputs: the caller-facing provider.ReviewRequest (Level/Diff/Context).
// Outputs: a Plan, or a typed error for an invalid level or an
//   unrecognised artifact format.
// Constraints: NewPlan is the ONLY constructor -- filtering happens at
//   construction, never by trimming a fuller context later (R-21.191). The
//   Context is filtered on BOTH channels: its diff sections (artifact.go) and
//   its PROSE lines referencing `.claude/**` or AGENTS.md (prose.go, the
//   confirming-review fix); every other line of it reaches the Rubric
//   verbatim. No
//   field on BlindRequest can express author identity, lane, prior verdict
//   or expected outcome -- TestReviewRequestBlind asserts this by reflection
//   over the live field set, not by convention.
//
//   SENSITIVITY IS NOT DECIDED HERE (CR fix D1). The earlier draft scanned
//   the free-text Context for a `sensitivity: local-only` tag and refused
//   on a regex match; three ordinary spellings slipped past it and the
//   dispatch proceeded. That check is DELETED. The reviewer instead sets
//   ModelRequest.Sensitivity to the review/arbitrate task class's own
//   SensitivityDefault from the real §5.16 table (templates.go) and never
//   below it, and passes the CALLER's ctx through to Execute unchanged, so
//   internal/conductor's FILTER 0 (privacy.go) and FILTER 2's filterLocalOnly
//   (filters_capability.go) apply the thread's real privacy mode. The refusal
//   is the router's, proven against the real router in router_test.go.
// SPORT: internal/review.request/ADD (P1-E25-W5-S52-T4).

package review

import (
	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/pkg/provider"
)

// ConsequenceClass is the R-21.156 assignment input this ticket publishes:
// how hard cross-family reviewer assignment is required for one dispatch.
// The actual ASSIGNMENT POLICY and ledger row belong to AH/S-69.T2 (not yet
// built); this type, ConsequenceClassForLevel and the HOLD/fallback rules
// below are what this ticket owns per its own §WHAT text.
type ConsequenceClass string

// The closed ConsequenceClass vocabulary, low to critical.
const (
	ConsequenceLow      ConsequenceClass = "low"
	ConsequenceNormal   ConsequenceClass = "normal"
	ConsequenceHigh     ConsequenceClass = "high"
	ConsequenceCritical ConsequenceClass = "critical"
)

// Valid reports whether c is one of the four declared members.
func (c ConsequenceClass) Valid() bool {
	switch c {
	case ConsequenceLow, ConsequenceNormal, ConsequenceHigh, ConsequenceCritical:
		return true
	default:
		return false
	}
}

// RequiresDistinctFamily reports whether c is at or above "normal", where
// R-21.156 makes cross-family reviewer assignment MANDATORY. An unknown
// value fails closed to true -- a corrupt ConsequenceClass never silently
// relaxes the requirement.
func (c ConsequenceClass) RequiresDistinctFamily() bool {
	switch c {
	case ConsequenceNormal, ConsequenceHigh, ConsequenceCritical:
		return true
	case ConsequenceLow:
		return false
	default:
		return true
	}
}

// HoldsWithoutDistinctFamily reports whether the absence of an eligible
// distinct family HOLDS the review (R-21.191: High/Critical raise
// ErrNoEligibleReviewerFamily) rather than logging a same-family fallback
// (Low/Normal). An unknown value fails closed to true.
func (c ConsequenceClass) HoldsWithoutDistinctFamily() bool {
	switch c {
	case ConsequenceHigh, ConsequenceCritical:
		return true
	case ConsequenceLow, ConsequenceNormal:
		return false
	default:
		return true
	}
}

// ConsequenceClassForLevel is this ticket's own default consequence-class
// policy, keyed off the requested review level: pkg/provider.ReviewRequest
// carries no consequence_class field of its own (O/S-33.T1's frozen ABI),
// so a dispatch's consequence class is derived from Level until a future
// contract amendment threads a caller-supplied one through. CR-C ("gate
// tickets and security-class changes", the ticket's own §WHAT text) maps to
// high; CR-B (ordinary peer review) to normal; CR-A (lightweight, per-hunk)
// to low. Every provider.ReviewCRLevel member is listed explicitly
// (exhaustive lint).
func ConsequenceClassForLevel(level provider.ReviewCRLevel) ConsequenceClass {
	switch level {
	case provider.ReviewCRLevelA:
		return ConsequenceLow
	case provider.ReviewCRLevelB:
		return ConsequenceNormal
	case provider.ReviewCRLevelC:
		return ConsequenceHigh
	default:
		return ConsequenceCritical
	}
}

// Lease is the R-16.32 read-only lease every reviewer dispatch carries. It
// deliberately has NO field: there is no way to construct a Lease value
// that grants write access, because the type cannot represent one -- the
// structural proof TestLeaseStructurallyReadOnly checks, rather than a boolean
// a caller could flip.
type Lease struct{}

// ReadOnly always reports true; see Lease's own doc comment for why no
// other value is representable.
func (Lease) ReadOnly() bool { return true }

// BlindRequest is the R-21.156(a) blind struct: the ONLY fields a
// conductor dispatch this package builds may carry. CheckpointID, Rubric,
// Artifact and ConsequenceClass are exactly the four fields R-21.156(a)
// names; Lease is R-16.32's read-only marker. Nothing here can express
// author identity, authoring lane, a prior verdict or an expected outcome.
type BlindRequest struct {
	// CheckpointID identifies this review dispatch. AP/S-81.T1 (not yet
	// built) will own a real checkpoint-id source; until then this is a
	// deterministic digest of the filtered artifact (audit.HashParams),
	// real and reproducible, never a random or fabricated value -- see
	// NewPlan.
	CheckpointID string
	// Rubric is the level's fixed review-instruction text (templates.go).
	Rubric string
	// Artifact is the diff under review, AFTER the R-21.191 exclusion
	// filter has run (FilterArtifact, artifact.go) -- never the raw,
	// unfiltered diff.
	Artifact string
	// ConsequenceClass is the R-21.156 assignment input.
	ConsequenceClass ConsequenceClass
	// Lease is the R-16.32 read-only marker.
	Lease Lease
}

// Plan is one dispatch's full input: the blind request itself plus the two
// derived facts a dispatch needs that R-21.156(a)'s blind field set may not
// carry -- the AMD-20260916/6 checklist the response must answer, and the
// paths R-21.191 removed (surfaced to the caller as a note, so an exclusion
// is never silent).
type Plan struct {
	// Blind is the R-21.156(a) request the dispatch is built from.
	Blind BlindRequest
	// Checks is the checklist parsed out of the caller's Context.
	Checks Checklist
	// Excluded lists the paths the R-21.191 filter dropped, in sorted
	// order. Empty when nothing was excluded.
	Excluded []string
}

// renderRubric assembles the Rubric the dispatch carries: the level's fixed
// instruction text, the caller's own Context VERBATIM (byte-identical -- a
// Context carrying files_scope/tasks/acceptance_criteria blocks reaches the
// model unrewritten, which is what "the reviewer never mutates the author's
// scope" means on the prompt side), and the checklist instructions.
func renderRubric(levelRubric, reqContext string, checks Checklist) string {
	out := levelRubric
	if reqContext != "" {
		out += "\n\nREVIEW CONTEXT (verbatim, as the caller supplied it):\n" + reqContext
	}
	return out + checks.Instructions()
}

// NewPlan builds the Plan a conductor dispatch carries, from the
// caller-facing req and the consequence class ConsequenceClassForLevel
// derived. Filtering runs HERE, at construction (R-21.191) -- never applied
// later to an already-built request -- and an artifact whose format cannot
// be attributed to paths is REFUSED here rather than dispatched unfiltered
// (FilterArtifact, D7).
func NewPlan(req provider.ReviewRequest, consequence ConsequenceClass) (Plan, error) {
	tmpl, err := TemplateFor(req.Level)
	if err != nil {
		return Plan{}, err
	}
	artifact, excluded, err := FilterArtifact(req.Diff)
	if err != nil {
		return Plan{}, err
	}
	reqContext, ctxExcluded := filterDiffSections(req.Context)
	reqContext, proseExcluded := filterContextProse(reqContext)
	checks := ParseChecklist(reqContext)
	return Plan{
		Blind: BlindRequest{
			CheckpointID:     audit.HashParams([]byte(artifact)),
			Rubric:           renderRubric(tmpl.Rubric(), reqContext, checks),
			Artifact:         artifact,
			ConsequenceClass: consequence,
			Lease:            Lease{},
		},
		Checks:   checks,
		Excluded: append(append(excluded, ctxExcluded...), proseExcluded...),
	}, nil
}
