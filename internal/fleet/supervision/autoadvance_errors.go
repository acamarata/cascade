// Purpose: the one error the auto-advance evaluator can produce, kept
//
//	beside it so autoadvance.go stays within Art.10.3's line budget.
//
// SPORT: internal/fleet/supervision:autoadvance-errors (ADD) — P1-E18-W4-S39-T2.

package supervision

import "github.com/acamarata/cascade/pkg/cascade"

// errEvaluatorPanicked reports a recovered panic inside the evaluator.
//
// The panic VALUE is deliberately not interpolated: it can carry arbitrary
// content from whatever failed, and this message reaches the audit log.
// The recovery itself is the fact worth recording; the value belongs in a
// stack trace, not in a record an operator reads.
func errEvaluatorPanicked(any) error {
	return cascade.New(cascade.KindInternal,
		"supervision: the auto-advance evaluator panicked; the action was denied")
}
