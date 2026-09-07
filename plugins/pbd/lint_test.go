// Package pbd (lint_test.go): tests for the `pbd lint` command wiring —
// RunLint/runLintAndSummarize and the RunCommand dispatch this ticket
// adds alongside T2's already-landed `validate`. Rule-level proofs live
// in internal/pews/lint_rules_test.go; this file only proves the command
// surface reaches the real engine.
package pbd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// cleanLintTicketYAML mirrors internal/pews's cleanTicketYAML: a document
// that passes every schema and contract-lint rule, so command-level tests
// can prove a clean tree lints with no issues without depending on
// internal/pews's unexported test helpers.
func cleanLintTicketYAML(id string) string {
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

func TestLintCommand(t *testing.T) {
	t.Run("RunCommand dispatches lint to the real engine on a clean tree", func(t *testing.T) {
		root := t.TempDir()
		writeCleanTicket(t, root, "P1-E14-W3-S28-T1")

		h := handlers{}
		if err := h.RunCommand(context.Background(), lintCommandName, []string{root}); err != nil {
			t.Fatalf("RunCommand: %v", err)
		}
	})

	t.Run("RunCommand surfaces contract-lint issues, folded into one error", func(t *testing.T) {
		root := t.TempDir()
		mkTicket(t, root, "N", 3, 28, 1, "P1-E14-W3-S28-T1", nil)
		// mkTicket's own minimalTicketYAML omits journals and uses
		// empty tasks/checks/AC/spec_refs: several rules must fire.

		h := handlers{}
		err := h.RunCommand(context.Background(), lintCommandName, []string{root})
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("err = %v, want KindInvalidInput", err)
		}
		if !strings.Contains(err.Error(), "pbd lint:") {
			t.Errorf("err = %v, want a folded pbd-lint summary", err)
		}
	})

	t.Run("RunCommand requires a tree root argument", func(t *testing.T) {
		h := handlers{}
		if err := h.RunCommand(context.Background(), lintCommandName, nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("err = %v, want KindInvalidInput", err)
		}
	})

	t.Run("RunLint propagates a hard load failure without a false-clean report", func(t *testing.T) {
		report, err := RunLint(t.TempDir()+"/does-not-exist", "P1")
		if !cascade.HasKind(err, cascade.KindNotFound) {
			t.Fatalf("err = %v, want KindNotFound", err)
		}
		if report.TicketCount != 0 || len(report.Issues) != 0 {
			t.Errorf("report = %+v, want the zero value on a load failure", report)
		}
	})
}

// writeCleanTicket overwrites the ticket mkTicket already placed with a
// fully contract-lint-clean document.
func writeCleanTicket(t *testing.T, root, id string) {
	t.Helper()
	mkTicket(t, root, "N", 3, 28, 1, id, nil)
	path := filepath.Join(root, "epics", "E-N", "waves", "W-3", "sprints", "S-28", "tickets", "T-1.yaml")
	if err := os.WriteFile(path, []byte(cleanLintTicketYAML(id)), 0o644); err != nil {
		t.Fatal(err)
	}
}
