package jobs

// Purpose: PEWSContract, the portable value type AH/S-69.T3 compiles
//
//	into a PlanInput. It mirrors the N/S-28.T1 17-field schema's
//	semantics field-for-field (R-21.192's normative table) without
//	importing plugins/pbd/internal/pews: 02-TARGET-STRUCTURE's v1.1
//	import-boundary rule runs plugins/providers -> pkg only, and Go's
//	own internal/ visibility rule additionally makes
//	plugins/pbd/internal/pews unreachable from this package regardless.
//	The party that already holds a decoded pews.Ticket (a downstream
//	integration point outside this ticket, per AC/S-59.T4's own wording)
//	builds a PEWSContract from it field-for-field.
//
// Inputs: none at this layer -- this file declares the type only.
// Outputs: PEWSContract.
//
// Constraints: every field name and shape mirrors the schema's own
//
//	(same string enums, same slice-of-string shapes); no field is
//	dropped and no field this compiler does not need is added (06 §5.1
//	never-invent-scope). A nil slice field is this contract's own
//	"field omitted" signal, distinct from a present-but-empty slice --
//	ValidateContractFields (pews_compiler_fieldmap.go) relies on this
//	distinction to fail closed on a genuinely missing field while still
//	accepting a ticket that legitimately declares an empty list.
//
// SPORT: jobs/pews-compiler/ADD (P1-E34-W7-S69-T3).

import "github.com/acamarata/cascade/internal/conductor"

// PEWSContract carries all seventeen 06 §1 schema fields, decoded and
// validated by the schema's own owner (N/S-28.T1) before this compiler
// ever sees them. files_scope's three sub-lists are carried as three
// separate slices rather than a nested struct, matching the shape
// AC/S-59.T4's TicketInput.Footprint already expects (their union is
// this compiler's Footprint value -- see mapFilesScopeToFootprint).
type PEWSContract struct {
	// ID is 06 §1 field 1 (the ticket id) -> TicketInput.ID.
	ID string
	// Title is field 2 -> job.name.
	Title string
	// ShortDesc is field 3 -> job metadata.
	ShortDesc string
	// FullDesc is field 4 -> job metadata.
	FullDesc string
	// Branch is field 5 -> the job's worktree branch base.
	Branch string
	// Weight is field 6, one of {XS,S,M,L,XL} -> cost_ceiling.
	Weight string
	// ModelClass is field 7, one of the closed conductor.ModelClass set
	// -> task_class via conductor.ModelClassToTaskClass (06 §5.18).
	ModelClass conductor.ModelClass
	// DependsOn is field 8, ticket ids -> DAG edges.
	DependsOn []string
	// Tasks is field 9 -> job metadata (ordered waypoints).
	Tasks []string
	// Checks is field 10, check commands -> one verification job per
	// command.
	Checks []string
	// AcceptanceCriteria is field 11 -> completion-gate evidence
	// requirements.
	AcceptanceCriteria []string
	// FilesScopeAdd/FilesScopeChange/FilesScopeDelete together are field
	// 12 (files_scope) -> lease scope + the mutation-set footprint.
	FilesScopeAdd    []string
	FilesScopeChange []string
	FilesScopeDelete []string
	// SpecRefs is field 13 -> job metadata.
	SpecRefs []string
	// CRLevel is field 14, an R-16.42 canonical form ("CR-B",
	// "CR-A+CR-B", "CR-B+CR-C" or "CR-A+CR-B+CR-C") -> the declared gate
	// set (jointly with QALevel).
	CRLevel string
	// QALevel is field 15, one of {QA-A,QA-B,QA-C} -> the declared gate
	// set (jointly with CRLevel).
	QALevel string
	// SportUpdates is field 16 -> an integrate job.
	SportUpdates []string
	// DocsUpdates is field 17 -> an integrate job.
	DocsUpdates []string
}
