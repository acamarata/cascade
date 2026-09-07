// Package pbd (validate.go): Purpose: the orchestration layer between the
//
//	mounted `pbd validate` command (pbd.go) and the native engine in
//	internal/pews — loads the tree, runs the validator, and folds the
//	resulting Report into one fail-closed error message.
//
// Inputs: a tree root and phase; delegates entirely to internal/pews.
// Outputs: RunValidate's (pews.Report, error); summarizeReport's single
//
//	*cascade.Error, or nil for a clean report.
//
// Constraints: imports pkg/** and internal/pews ONLY (Art.10.2) — no
//
//	internal/output, so violation text is folded into the returned error
//	rather than printed directly (see pbd.go's doc comment).
//
// SPORT: plugins/pbd validate (ADD) — P1-E14-W3-S28-T2.
package pbd

import (
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/pbd/internal/pews"
)

// DefaultPhase is the phase prefix used when a caller supplies none.
const DefaultPhase = "P1"

// RunValidate loads the PEWS tree rooted at root (ticket ids named under
// phase, or DefaultPhase when phase is empty) and runs the native
// structural validator over it. It is exported so a caller with its own
// tree root — a test harness, or a later command sharing this engine —
// can drive it directly without going through RunCommand's args-decoding.
func RunValidate(root, phase string) (pews.Report, error) {
	if phase == "" {
		phase = DefaultPhase
	}
	tree, err := pews.NewStore(root, phase).Load()
	if err != nil {
		return pews.Report{}, err
	}
	return pews.Validate(tree)
}

// runValidateAndSummarize is RunCommand's production entry point: it
// drives RunValidate and folds the result into one fail-closed error via
// summarizeReport, distinguishing a hard load failure (report never
// populated: pews.Report{} means Load itself failed, since a report with
// at least a zero ActiveCount only comes from a load that actually
// succeeded) from the tree's own structural violations.
func runValidateAndSummarize(root, phase string) error {
	report, err := RunValidate(root, phase)
	if err != nil && report.ActiveCount == 0 && report.TombstoneCount == 0 && len(report.Violations) == 0 {
		return err
	}
	return summarizeReport(report)
}

// summarizeReport folds report's violations into one fail-closed
// *cascade.Error, or returns nil for a clean report. Each violation is
// formatted by formatViolation (validate_ids.go/validate_deps.go split by
// concern) on its own line.
func summarizeReport(report pews.Report) error {
	if report.OK() {
		return nil
	}
	lines := make([]string, 0, len(report.Violations))
	for _, v := range report.Violations {
		lines = append(lines, formatViolation(v))
	}
	return cascade.Newf(cascade.KindInvalidInput,
		"pbd validate: %d violation(s) (%d active, %d tombstoned, %d gate-only):\n%s",
		len(report.Violations), report.ActiveCount, report.TombstoneCount, report.GateOnlyCount,
		strings.Join(lines, "\n"))
}

// formatViolation dispatches v to the id-class or dependency-class
// formatter, whichever owns its Kind.
func formatViolation(v pews.Violation) string {
	if depViolationKinds[v.Kind] {
		return formatDepViolation(v)
	}
	return formatIDViolation(v)
}
