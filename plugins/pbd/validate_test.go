package pbd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// minimalTicketYAML builds a fully valid (all 17 fields present) ticket
// document for id, with the given depends_on list. Shared by every test
// file in this package.
func minimalTicketYAML(id string, depends []string) string {
	deps := "[]"
	if len(depends) > 0 {
		var b strings.Builder
		b.WriteString("\n")
		for _, d := range depends {
			b.WriteString("  - " + d + "\n")
		}
		deps = strings.TrimRight(b.String(), "\n")
	}
	return fmt.Sprintf(`id: %s
title: t
short_desc: d
full_desc: d
branch: b
weight: S
model_class: build
depends_on: %s
tasks: []
checks: []
acceptance_criteria: []
files_scope:
  add: []
  change: []
  delete: []
spec_refs: []
cr_level: CR-B
qa_level: QA-A
sport_updates: []
docs_updates: []
`, id, deps)
}

// mkTicket writes a minimal ticket at the canonical tree position for
// (epic letters, wave, sprint, ticket).
func mkTicket(t *testing.T, root, epic string, wave, sprint, ticket int, id string, depends []string) {
	t.Helper()
	dir := filepath.Join(root, "epics", "E-"+epic, "waves",
		fmt.Sprintf("W-%d", wave), "sprints", fmt.Sprintf("S-%02d", sprint), "tickets")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("T-%d.yaml", ticket))
	if err := os.WriteFile(path, []byte(minimalTicketYAML(id, depends)), 0o644); err != nil {
		t.Fatalf("write ticket: %v", err)
	}
}

func TestRunValidate(t *testing.T) {
	t.Run("clean tree, default phase", func(t *testing.T) {
		root := t.TempDir()
		mkTicket(t, root, "N", 3, 28, 1, "P1-E14-W3-S28-T1", nil)
		report, err := RunValidate(root, "")
		if err != nil {
			t.Fatalf("RunValidate: %v", err)
		}
		if !report.OK() {
			t.Errorf("violations = %+v", report.Violations)
		}
	})

	t.Run("load failure propagates", func(t *testing.T) {
		_, err := RunValidate(filepath.Join(t.TempDir(), "missing"), "P1")
		if !cascade.HasKind(err, cascade.KindNotFound) {
			t.Errorf("err = %v, want KindNotFound", err)
		}
	})

	t.Run("violations propagate as a fail-closed error", func(t *testing.T) {
		root := t.TempDir()
		mkTicket(t, root, "N", 3, 28, 1, "P1-E14-W3-S28-T9", nil)
		_, err := RunValidate(root, "P1")
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("err = %v, want KindInvalidInput", err)
		}
	})
}

func TestValidateCommand(t *testing.T) {
	t.Run("clean tree returns nil", func(t *testing.T) {
		root := t.TempDir()
		mkTicket(t, root, "N", 3, 28, 1, "P1-E14-W3-S28-T1", nil)
		h := handlers{}
		if err := h.RunCommand(context.Background(), validateCommandName, []string{root}); err != nil {
			t.Fatalf("RunCommand: %v", err)
		}
	})

	t.Run("no args refuses", func(t *testing.T) {
		h := handlers{}
		err := h.RunCommand(context.Background(), validateCommandName, nil)
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("err = %v, want KindInvalidInput", err)
		}
	})

	t.Run("violations surface in the returned error text", func(t *testing.T) {
		root := t.TempDir()
		mkTicket(t, root, "N", 3, 28, 1, "P1-E14-W3-S28-T9", nil)
		h := handlers{}
		err := h.RunCommand(context.Background(), validateCommandName, []string{root})
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("err = %v, want KindInvalidInput", err)
		}
		if !strings.Contains(err.Error(), string(identityMismatchKindForTest)) {
			t.Errorf("error text = %q, want it to name the violation kind", err.Error())
		}
	})

	t.Run("explicit phase override is honored", func(t *testing.T) {
		root := t.TempDir()
		mkTicket(t, root, "N", 3, 28, 1, "X1-E14-W3-S28-T1", nil)
		h := handlers{}
		if err := h.RunCommand(context.Background(), validateCommandName, []string{root, "X1"}); err != nil {
			t.Fatalf("RunCommand with phase override: %v", err)
		}
	})
}

// identityMismatchKindForTest avoids validate_test.go importing
// internal/pews just to spell one constant string in an assertion.
const identityMismatchKindForTest = "identity-mismatch"

// archivedValidatorCase mirrors one entry of
// internal/pews/testdata/v1-goldens/validator-cases.json.
type archivedValidatorCase struct {
	Name                 string   `json:"name"`
	Description          string   `json:"description"`
	Epic                 string   `json:"epic"`
	Wave                 int      `json:"wave"`
	Sprint               int      `json:"sprint"`
	TicketNumbers        []int    `json:"ticket_numbers"`
	Tombstones           []string `json:"tombstones"`
	DuplicateIDs         []string `json:"duplicate_ids"`
	ExpectViolationKinds []string `json:"expect_violation_kinds"`
}

type archivedValidatorFixture struct {
	Cases []archivedValidatorCase `json:"cases"`
}

// TestArchivedValidatorFixtureParity replays every case harvested from the
// archived structural checker (.claude/planning/p1/.plan-audit.py — see
// internal/pews/testdata/v1-goldens/README.md for provenance) against the
// native validator and asserts the same behavior.
func TestArchivedValidatorFixtureParity(t *testing.T) {
	fixture := loadArchivedValidatorFixture(t)
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) { runArchivedValidatorCase(t, c) })
	}
}

// loadArchivedValidatorFixture reads and parses validator-cases.json.
func loadArchivedValidatorFixture(t *testing.T) archivedValidatorFixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("internal", "pews", "testdata", "v1-goldens", "validator-cases.json"))
	if err != nil {
		t.Fatalf("reading goldens: %v", err)
	}
	var fixture archivedValidatorFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("parsing goldens: %v", err)
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("no archived-validator cases loaded")
	}
	return fixture
}

// buildArchivedValidatorTree materializes one case's ticket_numbers,
// duplicate_ids, and tombstones as a real PEWS tree under t.TempDir().
func buildArchivedValidatorTree(t *testing.T, c archivedValidatorCase) string {
	t.Helper()
	root := t.TempDir()
	for _, n := range c.TicketNumbers {
		id := fmt.Sprintf("P1-E14-W%d-S%02d-T%d", c.Wave, c.Sprint, n)
		mkTicket(t, root, c.Epic, c.Wave, c.Sprint, n, id, nil)
	}
	for _, dupID := range c.DuplicateIDs {
		// Re-declare the id under the next free ticket slot in a different
		// sprint, reproducing a genuine cross-sprint duplicate the way the
		// tree, not the case, would.
		mkTicket(t, root, c.Epic, c.Wave, c.Sprint+1, 1, dupID, nil)
	}
	if len(c.Tombstones) > 0 {
		var b strings.Builder
		b.WriteString("tombstones:\n")
		for _, id := range c.Tombstones {
			fmt.Fprintf(&b, "  - id: %s\n    reason: harvested-fixture\n", id)
		}
		if err := os.WriteFile(filepath.Join(root, "tombstones.yaml"), []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// runArchivedValidatorCase builds c's tree, validates it, and asserts the
// native validator's violations match c.ExpectViolationKinds.
func runArchivedValidatorCase(t *testing.T, c archivedValidatorCase) {
	root := buildArchivedValidatorTree(t, c)
	report, err := RunValidate(root, "P1")
	gotKinds := map[string]bool{}
	for _, v := range report.Violations {
		gotKinds[string(v.Kind)] = true
	}

	if len(c.ExpectViolationKinds) == 0 {
		if err != nil {
			t.Fatalf("case %s: want clean, got %v (violations: %+v)", c.Name, err, report.Violations)
		}
		return
	}
	if err == nil {
		t.Fatalf("case %s: want violations %v, got none", c.Name, c.ExpectViolationKinds)
	}
	for _, want := range c.ExpectViolationKinds {
		if !gotKinds[want] {
			t.Errorf("case %s: missing expected violation kind %q (got %+v)", c.Name, want, report.Violations)
		}
	}
}
