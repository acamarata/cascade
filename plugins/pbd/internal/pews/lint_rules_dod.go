// Package pews (lint_rules_dod.go): Purpose: the Article-3 Ticket DoD
// contract-lint rule — checks that acceptance_criteria carries every
// 06-FORGE-SPEC.md §5 rule 25 DoD concept, case-insensitively and with
// the ruling-justified alternate wordings actually present in the real
// P1 tree (never invented for this fix). Split out of lint_rules.go to
// keep that file under the 300-line cap.
// Inputs: TicketRecord values from an already-Validate-clean Tree.
// Outputs: []LintIssue; lint.go concatenates and sorts them with every
// other rule's.
// Constraints: pure functions, no filesystem/clock/network access.
// SPORT: plugins/pbd/internal/pews lint_rules (ADD) — P1-E14-W3-S28-T4.
package pews

import (
	"fmt"
	"regexp"
	"strings"
)

// dodClauses are the Article-3 Ticket DoD concepts 06-FORGE-SPEC.md §5
// rule 25 requires acceptance_criteria to carry, one regexp per concept,
// matched case-insensitively against the joined acceptance_criteria
// text. Case-insensitivity matches ruleArt11's own established
// convention (lint_rules_sets.go): a DoD phrase leading a bullet is
// grammatically capitalized ("Checks green in CI ...") by ~40 tickets,
// meaning the same content a literal-case match would wrongly flag as
// missing. Four concepts carry a second, ruling-justified alternative
// actually present in the tree, never invented for this fix:
//   - checks-green: R-14.85 (AB/S-57-58's coverage-gate lane) strikes
//     the "GitHub-CI-job framing" for those tickets, which instead say
//     "checks green locally (in CI where runnable ...)".
//   - error-path / race-clean: a docs-only ticket (Z/S-54.T2) has no Go
//     code to error-path test and says so ("error-path tests and -race
//     are N/A (docs-only ticket, no Go files in scope)"); a ticket whose
//     coverage is provably already exercised by a named sibling ticket
//     (AH/S-70.T3) says "error-path coverage inherited from <ticket>"
//     rather than duplicate it; the two wave-hardening gate tickets
//     (AM/S-76.T4, AR/S-85.T5) substitute "the full repository -race
//     suite green from the tagged snapshot" for both concepts at once,
//     since running the whole existing suite from a tag already re-runs
//     every error-path test in it.
//   - no-stubs: 12-QUALITY-CONSTITUTION.md Art.3 itself quotes "no
//     Article-1 violations", not 06-FORGE-SPEC's "no Article-1 stubs";
//     since 06 §5 rule 25 defers to 12 as binding, both wordings count.
//   - docs-updates: the same two gate tickets say "gate report landed"
//     — the name of their actual docs_updates entry (a gate-report doc).
var dodClauses = []*regexp.Regexp{
	regexp.MustCompile(`(?i)checks green (in ci|locally)`),
	regexp.MustCompile(`(?i)(error-path tests present|error-path tests and -race are n/a|error-path coverage inherited from|-race suite green from the tagged snapshot)`),
	regexp.MustCompile(`(?i)(-race clean|error-path tests and -race are n/a|-race suite green from the tagged snapshot)`),
	regexp.MustCompile(`(?i)no article-1 (stubs|violations)`),
	regexp.MustCompile(`(?i)(docs_updates landed|gate report landed)`),
	regexp.MustCompile(`(?i)cr by a different agent`),
	regexp.MustCompile(`(?i)journal written`),
}

// dodClauseNames labels dodClauses' concepts, in the same order, for
// issue messages.
var dodClauseNames = []string{
	"checks green in CI (or, for R-14.85 local-lane tickets, locally)",
	"error-path tests present (or a recorded N/A / inherited-coverage / gate-form equivalent)",
	"-race clean (or a recorded N/A / gate-form equivalent)",
	"no Article-1 stubs (06) / violations (12-QUALITY-CONSTITUTION, binding)",
	"docs_updates landed (or a gate ticket's gate report landed)",
	"CR by a different agent",
	"journal written",
}

// ruleAcceptanceDoD checks that acceptance_criteria's combined text
// carries every 06 §5 rule 25 Article-3 Ticket DoD concept (dodClauses),
// reporting every concept missing rather than stopping at the first —
// matching lint.go's own "every issue found, never just the first"
// contract, which this function's single-phrase early return violated.
func ruleAcceptanceDoD(t TicketRecord) []LintIssue {
	joined := strings.Join(t.Ticket.AcceptanceCriteria, "\n")
	var v []LintIssue
	for i, re := range dodClauses {
		if !re.MatchString(joined) {
			v = append(v, LintIssue{LintKindMissingDoD, t.ID, t.RelPath,
				fmt.Sprintf("acceptance_criteria is missing the Article-3 Ticket DoD clause %q", dodClauseNames[i])})
		}
	}
	return v
}
