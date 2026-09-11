// Purpose: the sole canonical owner of both §5 normative tables that live
//   outside the model.execute envelope (R-16.59): the §5.16 nine-row
//   task-class taxonomy (ownership moved here from S-22.T2 per R-14.38)
//   and the §5.18 model_class -> task_class mapping, exported as
//   ModelClassToTaskClass for callers that resolve model_class before
//   task_class ever reaches a ModelRequest (N/S-30.T2's ticket dispatch,
//   AH/S-69.T3's DAG jobs). K/S-22.T1's Execute and ExecuteStream never
//   call ModelClassToTaskClass and never touch model_class in any form -
//   the envelope carries task_class only.
// Inputs: none at this layer - taskClassTable and the model_class mapping
//   are static, verbatim transcriptions of 06-FORGE-SPEC.md §5.16/§5.18.
// Outputs: TaskClasses() (a defensive copy of the nine-row table, for
//   the daemon composition root to pass to NewRouter) and
//   ModelClassToTaskClass (a pure resolution function).
// Constraints: this file owns both tables' CONTENTS (R-14.38); it does not
//   alter router.go's filter pipeline or its TaskClassRow type, and it does
//   not touch execute.go at all (R-21.217, R-16.59). Unknown or zero
//   ModelClass values FAIL CLOSED: ModelClassToTaskClass never returns a
//   permissive default.
// SPORT: conductor.task-classes/ADD (P1-E11-W3-S22-T4).

package conductor

import (
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// TaskClass is the closed §5.16 task-class name ModelClassToTaskClass
// resolves to. It is a defined string type (not a bare string) so a typo
// in a mapping literal is caught by the consistency test rather than
// silently threaded through as an arbitrary string; every consumer that
// needs a provider.ModelRequest.TaskClass value converts explicitly with
// string(tc), since that field predates this type (J/S-19.T1) and stays a
// plain string.
type TaskClass string

// The nine §5.16 task-class names, verbatim from the NORMATIVE TABLE.
const (
	TaskClassClassify  TaskClass = "classify"
	TaskClassSegment   TaskClass = "segment"
	TaskClassSummarize TaskClass = "summarize"
	TaskClassExtract   TaskClass = "extract"
	TaskClassChat      TaskClass = "chat"
	TaskClassCode      TaskClass = "code"
	TaskClassReason    TaskClass = "reason"
	TaskClassReview    TaskClass = "review"
	TaskClassArbitrate TaskClass = "arbitrate"
)

// taskClassTable is the §5.16 NORMATIVE TABLE, verbatim, all six columns.
// It is named distinctly from resolve.go's package-level taskClasses
// (a map[string]bool of the same nine names, owned by S-22.T1 for
// ResolveTaskClass's pure validation) to avoid a duplicate package-level
// declaration - see this file's journal entry for both sides quoted.
// CtxK is the minimum context window in thousands of tokens.
//
// SensitivityDefault encoding note (documented deviation, not silent
// papering-over): the spec's table carries two distinct semantic values
// this single provider.SensitivityTier field cannot both represent -
// "inherit" (six rows) and "restricted-capable" (code/review/arbitrate).
// Both encode here as provider.SensitivityRestricted: "inherit" fails
// closed to restricted per §5.16's own rule when the calling context
// cannot be observed from a static table row, and "restricted-capable"
// names the tightest bound a restricted-capable class may default to
// without ever silently widening. The class-specific "MAY carry
// restricted, must resolve to a permitted lane" behavior these three rows
// require is enforced by the router's sensitivity filter and K/S-22.T3's
// egress enforcement, not by this field alone.
var taskClassTable = []TaskClassRow{
	{Class: string(TaskClassClassify), Reasoning: "low", CtxK: 8, Structured: true, SensitivityDefault: provider.SensitivityRestricted, LaneAffinity: "cheapest/free"},
	{Class: string(TaskClassSegment), Reasoning: "low", CtxK: 16, Structured: true, SensitivityDefault: provider.SensitivityRestricted, LaneAffinity: "cheapest/free"},
	{Class: string(TaskClassSummarize), Reasoning: "low", CtxK: 32, Structured: false, SensitivityDefault: provider.SensitivityRestricted, LaneAffinity: "cheap"},
	{Class: string(TaskClassExtract), Reasoning: "low", CtxK: 32, Structured: true, SensitivityDefault: provider.SensitivityRestricted, LaneAffinity: "cheap"},
	{Class: string(TaskClassChat), Reasoning: "medium", CtxK: 128, Structured: false, SensitivityDefault: provider.SensitivityRestricted, LaneAffinity: "mid"},
	{Class: string(TaskClassCode), Reasoning: "high", CtxK: 200, Structured: false, SensitivityDefault: provider.SensitivityRestricted, LaneAffinity: "strong"},
	{Class: string(TaskClassReason), Reasoning: "high", CtxK: 200, Structured: false, SensitivityDefault: provider.SensitivityRestricted, LaneAffinity: "strong"},
	{Class: string(TaskClassReview), Reasoning: "high", CtxK: 200, Structured: true, SensitivityDefault: provider.SensitivityRestricted, LaneAffinity: "strong"},
	{Class: string(TaskClassArbitrate), Reasoning: "max", CtxK: 200, Structured: true, SensitivityDefault: provider.SensitivityRestricted, LaneAffinity: "strongest"},
}

// init asserts the taxonomy's own invariant against itself: exactly nine
// rows, always. This is a fail-fast check over this file's own constant
// data (never over caller input), the same class of invariant Go's
// standard library asserts in package init for fixed tables.
func init() {
	if len(taskClassTable) != 9 {
		panic("conductor: taskClassTable must have exactly 9 rows per 06-FORGE-SPEC.md §5.16")
	}
}

// TaskClasses returns a defensive copy of the §5.16 nine-row taxonomy,
// for the daemon composition root to pass to NewRouter (R-14.38,
// R-21.217). Callers that mutate the returned slice never affect this
// package's own table.
func TaskClasses() []TaskClassRow {
	out := make([]TaskClassRow, len(taskClassTable))
	copy(out, taskClassTable)
	return out
}

// ModelClass is the §5.18 model_class enum: the class a caller (ticket
// dispatch, a DAG job) assigns BEFORE it ever resolves a task_class or
// constructs a provider.ModelRequest. Conductor never resolves model_class
// itself (R-16.59); this type and ModelClassToTaskClass exist solely for
// callers outside conductor's own call path.
type ModelClass string

// The five §5.18 model_class values. The zero value ("") is intentionally
// not a member - a caller that forgets to set ModelClass fails closed via
// ModelClassToTaskClass rather than silently resolving to any class.
const (
	ModelClassMech    ModelClass = "mech"
	ModelClassBuild   ModelClass = "build"
	ModelClassHeavy   ModelClass = "heavy"
	ModelClassReview  ModelClass = "review"
	ModelClassArbiter ModelClass = "arbiter"
)

// ErrInvalidModelClass is returned by ModelClassToTaskClass for the zero
// value or any string outside the five declared ModelClass members. It
// wraps the frozen KindInvalidInput kind (A-T7 sentinel rule); there is no
// second, conductor-local invalid-input kind.
var ErrInvalidModelClass = cascade.New(cascade.KindInvalidInput, "conductor: invalid or unknown model class")

// ModelClassToTaskClass implements the §5.18 NORMATIVE mapping verbatim:
// mech/build/heavy all resolve to "code" (they differ by cr_level and
// reviewer depth, never by task class - see §5.18's note), review resolves
// to "review", and arbiter resolves to "arbitrate". Any other value,
// including the zero value, FAILS CLOSED to ErrInvalidModelClass: the
// class chooses the routing lane and therefore the cost and sensitivity
// treatment, so an unmappable model_class is never defaulted to a
// general-purpose task class.
func ModelClassToTaskClass(mc ModelClass) (TaskClass, error) {
	switch mc {
	case ModelClassMech, ModelClassBuild, ModelClassHeavy:
		return TaskClassCode, nil
	case ModelClassReview:
		return TaskClassReview, nil
	case ModelClassArbiter:
		return TaskClassArbitrate, nil
	default:
		return "", ErrInvalidModelClass
	}
}
