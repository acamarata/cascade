// Package pbd (lint.go): Purpose: the orchestration layer between the
// mounted `pbd lint` command (pbd.go) and the native contract-lint engine
// in internal/pews — loads the tree, runs the lint, and folds the
// resulting LintReport into one fail-closed error message. Mirrors
// validate.go's split between command wiring and engine result
// formatting, kept in its own file because N/S-28.T4's files_scope adds
// this file rather than growing validate.go past its own line budget.
// Inputs: a tree root and phase; delegates entirely to internal/pews.
// Outputs: RunLint's (pews.LintReport, error); summarizeLintReport's
// single *cascade.Error, or nil for a clean report.
// Constraints: imports pkg/** and internal/pews ONLY (Art.10.2) — no
// internal/output, so issue text is folded into the returned error rather
// than printed directly (see pbd.go's doc comment).
// SPORT: plugins/pbd lint (ADD) — P1-E14-W3-S28-T4.
package pbd

import (
	"fmt"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/pbd/internal/pews"
)

// RunLint loads the PEWS tree rooted at root (ticket ids named under
// phase, or DefaultPhase when phase is empty) and runs the native
// contract-lint engine over it. It is exported so a caller with its own
// tree root — a test harness, or a later command sharing this engine —
// can drive it directly without going through RunCommand's args-decoding.
func RunLint(root, phase string) (pews.LintReport, error) {
	if phase == "" {
		phase = DefaultPhase
	}
	tree, err := pews.NewStore(root, phase).Load()
	if err != nil {
		return pews.LintReport{}, err
	}
	return pews.Lint(tree)
}

// runLintAndSummarize is RunCommand's production entry point for `pbd
// lint`: it drives RunLint and folds the result into one fail-closed
// error via summarizeLintReport, distinguishing a hard load/structural
// failure (LintReport never populated: pews.LintReport{} with
// TicketCount 0 and no issues means Lint itself refused, since a report
// with a populated TicketCount only comes from a lint that actually ran)
// from the tree's own contract-completeness issues.
func runLintAndSummarize(root, phase string) error {
	report, err := RunLint(root, phase)
	if err != nil && report.TicketCount == 0 && len(report.Issues) == 0 {
		return err
	}
	return summarizeLintReport(report)
}

// summarizeLintReport folds report's issues into one fail-closed
// *cascade.Error, or returns nil for a clean report.
func summarizeLintReport(report pews.LintReport) error {
	if report.OK() {
		return nil
	}
	lines := make([]string, 0, len(report.Issues))
	for _, issue := range report.Issues {
		lines = append(lines, formatLintIssue(issue))
	}
	return cascade.Newf(cascade.KindInvalidInput,
		"pbd lint: %d issue(s) across %d ticket(s):\n%s",
		len(report.Issues), report.TicketCount, strings.Join(lines, "\n"))
}

// formatLintIssue renders one contract-lint issue as a single report
// line, prefixed with its kind so issues of the same kind group naturally
// when several appear in one report.
func formatLintIssue(i pews.LintIssue) string {
	if i.Path != "" {
		return fmt.Sprintf("[%s] %s (%s)", i.Kind, i.Message, i.Path)
	}
	return fmt.Sprintf("[%s] %s", i.Kind, i.Message)
}
