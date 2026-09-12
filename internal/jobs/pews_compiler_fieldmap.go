package jobs

// Purpose: the R-21.192 NORMATIVE 17-field -> job/DAG attribute map,
//
//	one named function per contract-field row, plus ValidateContractFields,
//	the single fail-closed presence/shape check every one of the
//	seventeen fields passes through before CompileTicket touches them.
//	No mapping target is inferred at build time: every row this
//	ticket's contract table names has its own function here.
//
// Inputs: a PEWSContract.
// Outputs: each row's mapped value in its documented target shape, or
//
//	(from ValidateContractFields) a typed refusal.
//
// Constraints: pure functions only; no field is silently defaulted.
//
//	Several rows (short_desc, full_desc, tasks, spec_refs, sport_updates,
//	docs_updates) map to "job metadata" or "an integrate job" -- kinds
//	that have no concrete Go type in this tree yet (no job/DAG-assembly
//	binary exists this side of AC/S-60.T1); those rows are represented
//	as pass-through accessors returning the value in the shape a future
//	job-metadata consumer would read it in, per this ticket's own scope
//	line 4 ("not in scope: ... wiring a live plugins/pbd caller").
//
// SPORT: jobs/pews-compiler/ADD (P1-E34-W7-S69-T3).

import "github.com/acamarata/cascade/internal/conductor"

// Field-name constants, used both by ValidateContractFields' errors and
// by ErrUnknownReviewLevel.Error's field switch.
const (
	crLevelField = "cr_level"
	qaLevelField = "qa_level"
)

// validWeights is the closed 06 §1 weight enum, verbatim from the
// N/S-28.T1 schema (Weight{XS,S,M,L,XL}).
var validWeights = map[string]bool{"XS": true, "S": true, "M": true, "L": true, "XL": true}

// validCRLevels is the complete R-16.42 canonical cr_level form set.
var validCRLevels = map[string]bool{
	"CR-B": true, "CR-A+CR-B": true, "CR-B+CR-C": true, "CR-A+CR-B+CR-C": true,
}

// validQALevels is the closed 06 §1 qa_level enum.
var validQALevels = map[string]bool{"QA-A": true, "QA-B": true, "QA-C": true}

// requiredStringFields are the row-2/3/4/5 scalar fields required
// non-empty (id, model_class, cr_level and qa_level have their own more
// specific sentinel and are checked separately).
func requiredStringFields(c PEWSContract) []struct{ name, value string } {
	return []struct{ name, value string }{
		{"title", c.Title}, {"short_desc", c.ShortDesc},
		{"full_desc", c.FullDesc}, {"branch", c.Branch},
	}
}

// requiredListFields are the row-8/9/10/11/13/16/17 list fields
// required present (non-nil; see PEWSContract's nil-vs-empty doc).
func requiredListFields(c PEWSContract) []struct {
	name string
	list []string
} {
	return []struct {
		name string
		list []string
	}{
		{"depends_on", c.DependsOn}, {"tasks", c.Tasks}, {"checks", c.Checks},
		{"acceptance_criteria", c.AcceptanceCriteria}, {"spec_refs", c.SpecRefs},
		{"sport_updates", c.SportUpdates}, {"docs_updates", c.DocsUpdates},
	}
}

// ValidateContractFields fail-closes every one of the seventeen 06 §1
// fields: id, model_class, cr_level and qa_level use their own named
// sentinel (ErrEmptyTicketID / ErrUnknownModelClass / ErrUnknownReviewLevel);
// every other field returns ErrMissingContractField (absent) or
// ErrUnparseableContractField (present but malformed).
func ValidateContractFields(c PEWSContract) error {
	if c.ID == "" {
		return ErrEmptyTicketID
	}
	if _, err := conductor.ModelClassToTaskClass(c.ModelClass); err != nil {
		return &ErrUnknownModelClass{ModelClass: string(c.ModelClass)}
	}
	if !validCRLevels[c.CRLevel] {
		return &ErrUnknownReviewLevel{Field: crLevelField, Value: c.CRLevel}
	}
	if !validQALevels[c.QALevel] {
		return &ErrUnknownReviewLevel{Field: qaLevelField, Value: c.QALevel}
	}
	for _, f := range requiredStringFields(c) {
		if f.value == "" {
			return &ErrMissingContractField{Field: f.name}
		}
	}
	if c.Weight == "" {
		return &ErrMissingContractField{Field: "weight"}
	}
	if !validWeights[c.Weight] {
		return &ErrUnparseableContractField{Field: "weight", Value: c.Weight, Reason: "one of XS, S, M, L, XL"}
	}
	for _, f := range requiredListFields(c) {
		if f.list == nil {
			return &ErrMissingContractField{Field: f.name}
		}
	}
	if c.FilesScopeAdd == nil && c.FilesScopeChange == nil && c.FilesScopeDelete == nil {
		return &ErrMissingContractField{Field: "files_scope"}
	}
	return nil
}

// VerificationJob is one row-10 (checks) target: one verification job
// per check command, in declared order.
type VerificationJob struct {
	Command string
}

// The seventeen §1a NORMATIVE row mapping functions, in 06 §1 field
// order. Each returns its row's value in the documented job/DAG target
// shape; none infers a target other than the one the contract's own
// table names.

func mapIDToTicketID(c PEWSContract) string { return c.ID }

func mapTitleToJobName(c PEWSContract) string { return c.Title }

func mapShortDescToMetadata(c PEWSContract) string { return c.ShortDesc }

func mapFullDescToMetadata(c PEWSContract) string { return c.FullDesc }

func mapBranchToWorktreeBase(c PEWSContract) string { return c.Branch }

// mapWeightToCostCeiling resolves row 6 (weight -> cost_ceiling) to the
// compiler's own monotonic weight-to-ceiling table. No normative
// numeric mapping exists elsewhere in the tree (PassThroughFields.
// CostCeiling is caller-supplied pass-through per planinput.go, never
// planner-synthesized), so this scale is this compiler's own documented
// choice, exposed for a future job-metadata consumer; CompileTicket's
// own TicketInput never sets CostCeiling from it (that would violate
// planinput.go's own pass-through-only invariant for that field).
func mapWeightToCostCeiling(weight string) (float64, error) {
	switch weight {
	case "XS":
		return 1, nil
	case "S":
		return 2, nil
	case "M":
		return 3, nil
	case "L":
		return 5, nil
	case "XL":
		return 8, nil
	default:
		return 0, &ErrUnparseableContractField{Field: "weight", Value: weight, Reason: "one of XS, S, M, L, XL"}
	}
}

func mapModelClassToTaskClass(c PEWSContract) (conductor.TaskClass, error) {
	return conductor.ModelClassToTaskClass(c.ModelClass)
}

func mapDependsOnToDAGEdges(c PEWSContract) []string { return c.DependsOn }

func mapTasksToMetadata(c PEWSContract) []string { return c.Tasks }

func mapChecksToVerificationJobs(c PEWSContract) []VerificationJob {
	out := make([]VerificationJob, 0, len(c.Checks))
	for _, cmd := range c.Checks {
		out = append(out, VerificationJob{Command: cmd})
	}
	return out
}

func mapAcceptanceCriteriaToEvidenceRequirements(c PEWSContract) []string {
	return c.AcceptanceCriteria
}

// mapFilesScopeToFootprint is row 12 (files_scope -> lease scope + the
// mutation-set footprint): the ADD+CHANGE+DELETE union, in that order,
// deduplicated -- AC/S-59.T4's own TicketInput.Footprint shape.
func mapFilesScopeToFootprint(c PEWSContract) []string {
	seen := make(map[string]bool, len(c.FilesScopeAdd)+len(c.FilesScopeChange)+len(c.FilesScopeDelete))
	out := make([]string, 0, len(seen))
	for _, group := range [][]string{c.FilesScopeAdd, c.FilesScopeChange, c.FilesScopeDelete} {
		for _, p := range group {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

func mapSpecRefsToMetadata(c PEWSContract) []string { return c.SpecRefs }

func mapSportUpdatesToIntegrateJob(c PEWSContract) []string { return c.SportUpdates }

func mapDocsUpdatesToIntegrateJob(c PEWSContract) []string { return c.DocsUpdates }
