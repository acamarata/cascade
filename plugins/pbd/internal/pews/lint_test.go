// Package pews (lint_test.go): tests for Lint's orchestration — that it
// composes Validate as a precondition (fail-closed on a structurally
// unsound tree) rather than re-deriving structural facts, and that a
// structurally sound but contract-incomplete tree still refuses.
// Individual rule-firing proofs live in lint_rules_test.go.
package pews

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// cleanTicketYAML returns a ticket document that satisfies every schema
// (T1) AND every contract-lint (T4) rule this file's package implements,
// so tests can mutate exactly one field to prove exactly one rule fires.
func cleanTicketYAML(id string) string {
	return `id: ` + id + `
title: Do the thing
short_desc: does the thing
full_desc: does the thing in implementation-level detail
branch: ` + id + `-Do-The-Thing
weight: S
model_class: build
depends_on: []
tasks:
  - step one
  - step two
  - step three
checks:
  - go test ./plugins/pbd/...
acceptance_criteria:
  - "checks green in CI; error-path tests present; -race clean; no Article-1 stubs; docs_updates landed; CR by a different agent; journal written"
files_scope:
  add:
    - plugins/pbd/x.go
  change: []
  delete: []
spec_refs:
  - 06-FORGE-SPEC.md section 1
cr_level: CR-A+CR-B
qa_level: QA-A
sport_updates: []
docs_updates: []
journals: true
`
}

// ticketPath returns the canonical file path for one ticket under root.
func ticketPath(root, epic string, wave, sprint, ticket int) string {
	return filepath.Join(root, "epics", "E-"+epic, "waves",
		fmt.Sprintf("W-%d", wave), "sprints", fmt.Sprintf("S-%02d", sprint), "tickets", fmt.Sprintf("T-%d.yaml", ticket))
}

func TestContractLint(t *testing.T) {
	t.Run("nil tree refuses", func(t *testing.T) {
		if _, err := Lint(nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("err = %v, want KindInvalidInput", err)
		}
	})

	t.Run("a structurally unsound tree refuses lint without re-deriving the fault", func(t *testing.T) {
		root := t.TempDir()
		// Declared id ("...T9") does not match the tree position
		// ("...T1"): a T2 identity-mismatch violation, not anything
		// this ticket's rules touch.
		mustWriteFile(t, ticketPath(root, "N", 3, 28, 1), cleanTicketYAML("P1-E14-W3-S28-T9"))
		report, err := Lint(mustLoad(t, root))
		if err == nil {
			t.Fatal("Lint of a structurally invalid tree must refuse")
		}
		if len(report.Issues) != 0 || report.TicketCount != 0 {
			t.Errorf("report = %+v, want the zero value: Lint must not evaluate contract rules over an invalid tree", report)
		}
	})

	t.Run("a structurally sound, fully compliant tree lints clean", func(t *testing.T) {
		root := t.TempDir()
		mustWriteFile(t, ticketPath(root, "N", 3, 28, 1), cleanTicketYAML("P1-E14-W3-S28-T1"))
		report, err := Lint(mustLoad(t, root))
		if err != nil {
			t.Fatalf("Lint: %v (issues: %+v)", err, report.Issues)
		}
		if !report.OK() || report.TicketCount != 1 {
			t.Errorf("report = %+v", report)
		}
	})

	t.Run("a structurally sound but contract-incomplete tree refuses with issues populated", func(t *testing.T) {
		root := t.TempDir()
		yaml := strings.Replace(cleanTicketYAML("P1-E14-W3-S28-T1"), "title: Do the thing\n", "title: \"\"\n", 1)
		mustWriteFile(t, ticketPath(root, "N", 3, 28, 1), yaml)
		report, err := Lint(mustLoad(t, root))
		if err == nil {
			t.Fatal("want a fail-closed error for a contract-incomplete tree")
		}
		if report.TicketCount != 1 || !hasLintIssue(report, LintKindFieldRequired) {
			t.Errorf("report = %+v", report)
		}
	})
}

func hasLintIssue(report LintReport, kind LintIssueKind) bool {
	for _, i := range report.Issues {
		if i.Kind == kind {
			return true
		}
	}
	return false
}
