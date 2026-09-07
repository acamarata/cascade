package pews

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// minimalTicketYAML builds a fully valid (all 17 fields present) ticket
// document for id, with the given depends_on list.
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

// mkTicketFile writes a minimal ticket at the canonical tree position for
// (epic letters, wave, sprint, ticket).
func mkTicketFile(t *testing.T, root, epic string, wave, sprint, ticket int, id string, depends []string) {
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

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEpicNumberFromLetters(t *testing.T) {
	cases := map[string]int{"A": 1, "N": 14, "Z": 26, "AA": 27, "AH": 34, "AR": 44}
	for letters, want := range cases {
		if got := epicNumberFromLetters(letters); got != want {
			t.Errorf("epicNumberFromLetters(%q) = %d, want %d", letters, got, want)
		}
	}
}

func TestTreeStore(t *testing.T) {
	t.Run("positive", testTreeStorePositive)
	t.Run("refusals", testTreeStoreRefusals)
}

// testTreeStorePositive covers the shapes that must Load successfully.
func testTreeStorePositive(t *testing.T) {
	t.Run("clean tree loads every ticket", func(t *testing.T) {
		root := t.TempDir()
		mkTicketFile(t, root, "N", 3, 28, 1, "P1-E14-W3-S28-T1", nil)
		mkTicketFile(t, root, "N", 3, 28, 2, "P1-E14-W3-S28-T2", []string{"P1-E14-W3-S28-T1"})
		tree, err := NewStore(root, "P1").Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(tree.Tickets) != 2 || tree.Tickets[0].CanonicalID != "P1-E14-W3-S28-T1" {
			t.Fatalf("tree = %+v", tree.Tickets)
		}
	})

	t.Run("empty root is a valid empty tree", func(t *testing.T) {
		tree, err := NewStore(t.TempDir(), "P1").Load()
		if err != nil || len(tree.Tickets) != 0 {
			t.Fatalf("tree=%+v err=%v", tree, err)
		}
	})

	t.Run("tombstones.yaml is loaded", func(t *testing.T) {
		root := t.TempDir()
		mustWriteFile(t, filepath.Join(root, "tombstones.yaml"),
			"tombstones:\n  - id: P1-E14-W3-S28-T3\n    reason: superseded\n")
		tree, err := NewStore(root, "P1").Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(tree.Tombstones) != 1 || tree.Tombstones[0].ID != "P1-E14-W3-S28-T3" {
			t.Errorf("tombstones = %+v", tree.Tombstones)
		}
	})
}

// treeStoreRefusalCase is one input Load must reject, and the Kind the
// refusal must carry.
type treeStoreRefusalCase struct {
	name  string
	setup func(t *testing.T) (root, phase string)
	kind  cascade.Kind
}

// treeStoreRefusalCases enumerates every case testTreeStoreRefusals drives.
func treeStoreRefusalCases() []treeStoreRefusalCase {
	return []treeStoreRefusalCase{
		{"empty root string", func(_ *testing.T) (string, string) { return "", "P1" }, cascade.KindInvalidInput},
		{"empty phase", func(t *testing.T) (string, string) { return t.TempDir(), "" }, cascade.KindInvalidInput},
		{"missing root", func(t *testing.T) (string, string) {
			return filepath.Join(t.TempDir(), "does-not-exist"), "P1"
		}, cascade.KindNotFound},
		{"root is a file", func(t *testing.T) (string, string) {
			root := t.TempDir()
			mustWriteFile(t, filepath.Join(root, "notadir"), "x")
			return filepath.Join(root, "notadir"), "P1"
		}, cascade.KindInvalidInput},
		{"malformed epic dir", func(t *testing.T) (string, string) {
			root := t.TempDir()
			mustWriteFile(t, filepath.Join(root, "epics", "not-an-epic", ".keep"), "")
			return root, "P1"
		}, cascade.KindInvalidInput},
		{"malformed wave dir", func(t *testing.T) (string, string) {
			root := t.TempDir()
			mustWriteFile(t, filepath.Join(root, "epics", "E-A", "waves", "wave-1", ".keep"), "")
			return root, "P1"
		}, cascade.KindInvalidInput},
		{"malformed sprint dir", func(t *testing.T) (string, string) {
			root := t.TempDir()
			mustWriteFile(t, filepath.Join(root, "epics", "E-A", "waves", "W-1", "sprints", "sprint-1", ".keep"), "")
			return root, "P1"
		}, cascade.KindInvalidInput},
		{"malformed ticket file name", func(t *testing.T) (string, string) {
			root := t.TempDir()
			mustWriteFile(t, filepath.Join(root, "epics", "E-A", "waves", "W-1", "sprints", "S-01", "tickets", "ticket-one.yaml"), "x")
			return root, "P1"
		}, cascade.KindInvalidInput},
		{"malformed ticket YAML", func(t *testing.T) (string, string) {
			root := t.TempDir()
			mustWriteFile(t, filepath.Join(root, "epics", "E-A", "waves", "W-1", "sprints", "S-01", "tickets", "T-1.yaml"), "not: valid: yaml: [")
			return root, "P1"
		}, cascade.KindInvalidInput},
		{"malformed tombstones YAML", func(t *testing.T) (string, string) {
			root := t.TempDir()
			mustWriteFile(t, filepath.Join(root, "tombstones.yaml"), "[")
			return root, "P1"
		}, cascade.KindInvalidInput},
	}
}

// testTreeStoreRefusals runs every treeStoreRefusalCases entry.
func testTreeStoreRefusals(t *testing.T) {
	for _, c := range treeStoreRefusalCases() {
		t.Run(c.name, func(t *testing.T) {
			root, phase := c.setup(t)
			if _, err := NewStore(root, phase).Load(); !cascade.HasKind(err, c.kind) {
				t.Errorf("err = %v, want Kind %v", err, c.kind)
			}
		})
	}
}
