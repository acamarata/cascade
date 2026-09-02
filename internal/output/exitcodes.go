package output

// This file (exitcodes.go) holds the CLI process exit-code seam for
// internal/output's callers.
//
// Constraints: this file defines NO exit-code constants or Kind->code
//   lookup table of its own. pkg/cascade/codes.go already owns the FROZEN
//   R-14.3 taxonomy's exhaustive, non-overlapping exit-code table
//   (ExitOK..ExitCanceled, exitCodeByKind) and pkg/cascade/wire.go already
//   exposes the single entry point, cascade.ExitCode(err) — a second table
//   here would be exactly the two-sources-of-truth drift R-14.2 exists to
//   prevent (see this file's ExitCode doc for the delegation this ticket
//   settled on instead of a duplicate table).
// SPORT: internal/output [ADD] (D/S-06.T5 sport_updates).

import "github.com/acamarata/cascade/pkg/cascade"

// ExitCode returns the CLI process exit status for err, delegating
// entirely to pkg/cascade.ExitCode (pkg/cascade/wire.go): ExitOK for a nil
// err, the taxonomy Kind's exit status when err's chain carries one
// (pkg/cascade/codes.go's exitCodeByKind, including ExitCanceled's 130 =
// 128+SIGINT convention), and ExitInternal as the fallback for any other
// non-nil error.
//
// This wrapper exists so cmd/cascade/main.go — the single caller — needs
// only internal/output for both constructing its Writer (output.go) and
// computing the process exit status, keeping the composition root's import
// list to one internal package. It adds no behavior of its own: an early
// draft of this ticket considered re-deriving the exit-code table locally
// so internal/output would not need to import pkg/cascade for this one
// call, but that would have created a second, driftable copy of a table
// pkg/cascade already guarantees is exhaustive and collision-free
// (TestExitCodeTable_MatchesR143 and TestTaxonomyTablesTotalAndNonOverlapping
// in pkg/cascade/wire_test.go) — the wrapper is the smaller cost.
func ExitCode(err error) int {
	return cascade.ExitCode(err)
}
