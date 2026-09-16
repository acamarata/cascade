// Package dispatch holds cascade-pbd's model_class → task_class
// resolution, the load-bearing step between a PEWS ticket and the
// Conductor's model.execute envelope.
//
// Purpose: turn a ticket's model_class into the task_class the envelope
//
//	actually carries. The K/S-22.T1 envelope has NO model_class field
//	(R-16.59), so this is what sets task_class — not a redundant parity
//	check alongside some other resolution.
//
// Inputs: one pews.ModelClass.
// Outputs: the 06-FORGE-SPEC §5.18 task class, or false for a value this
//
//	build does not define.
//
// Constraints: the canonical owner of the §5.18 table is K/S-22.T4's
//
//	internal/conductor/task_classes.go, which this package MAY NOT import
//	(02-TARGET-STRUCTURE's plugins/→pkg-only boundary, enforced by
//	depguard). The two are kept in step by the table-literal parity test
//	beside this file, which iterates all five model_class values against
//	the §5.18 rows verbatim — not by an import that the boundary forbids.
//
//	There is deliberately NO default clause: the `exhaustive` analyzer on
//	the lint wall (R-14.101) fails the build when a model_class is added
//	without a row here, and the parity test fails alongside it. A runtime
//	fallback would turn that compile-time failure into a silently
//	mis-classified dispatch.
//
// SPORT: plugins/pbd/internal/dispatch:mapping (ADD) — P1-E14-W3-S30-T2.
package dispatch

import "github.com/acamarata/cascade/plugins/pbd/internal/pews"

// The §5.18 task classes this mapping can produce. They are plain strings
// because pkg/provider.ModelRequest.TaskClass is a plain string: K/S-22.T4
// owns the closed set, and redeclaring it as an enum here would be a second
// copy to drift.
const (
	// TaskClassCode is implementation work.
	TaskClassCode = "code"
	// TaskClassReview is CR-B review work.
	TaskClassReview = "review"
	// TaskClassArbitrate is CR-C and gate adjudication.
	TaskClassArbitrate = "arbitrate"
)

// TaskClassFor resolves m to its §5.18 task class.
//
// ok is false for a model_class this build does not define, which the
// caller refuses rather than dispatching under a guessed class. A wrong
// task class routes the work to the wrong lane, so guessing here would be
// worse than refusing.
func TaskClassFor(m pews.ModelClass) (taskClass string, ok bool) {
	switch m {
	case pews.ModelClassMech:
		return TaskClassCode, true
	case pews.ModelClassBuild:
		return TaskClassCode, true
	case pews.ModelClassHeavy:
		return TaskClassCode, true
	case pews.ModelClassReview:
		return TaskClassReview, true
	case pews.ModelClassArbiter:
		return TaskClassArbitrate, true
	}
	return "", false
}
