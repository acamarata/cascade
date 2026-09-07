// Package pbd (author_test.go): tests for the `pbd create|edit|move`
// command wiring — RunCreate/RunEdit/RunMove and the RunCommand dispatch
// this ticket adds alongside T2's validate and T4's lint. Engine-level
// proofs (atomicity, round-trip fidelity, Lint pre-flighting) live in
// internal/pews/author_test.go; this file only proves the command surface
// reaches the real engine and parses its own args correctly.
package pbd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// writeTicketFile writes a standalone ticket YAML file (not inside a
// tree) that RunCreate/RunEdit read as their <ticket-file> argument.
func writeTicketFile(t *testing.T, dir, name, id string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(cleanLintTicketYAML(id)), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAuthorCommands(t *testing.T) {
	t.Run("RunCreate authors a new ticket via the real engine", testAuthorRunCreate)
	t.Run("RunCommand dispatches create to the real engine", testAuthorCommandCreate)
	t.Run("create refuses a missing ticket file", testAuthorCreateMissingFile)
	t.Run("RunEdit overwrites via the real engine", testAuthorRunEdit)
	t.Run("RunEdit refuses when the file's id does not match the target", testAuthorEditIDMismatch)
	t.Run("RunCommand dispatches edit to the real engine", testAuthorCommandEdit)
	t.Run("RunMove relocates via the real engine", testAuthorRunMove)
	t.Run("RunCommand dispatches move to the real engine", testAuthorCommandMove)
	t.Run("RunCommand rejects each authoring verb's malformed arg count", testAuthorCommandBadArgs)
}

func testAuthorRunCreate(t *testing.T) {
	root := t.TempDir()
	file := writeTicketFile(t, t.TempDir(), "new.yaml", "P1-E14-W3-S28-T1")
	if err := RunCreate(root, file, "P1"); err != nil {
		t.Fatalf("RunCreate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "epics", "E-N", "waves", "W-3", "sprints", "S-28", "tickets", "T-1.yaml")); err != nil {
		t.Errorf("ticket file not written: %v", err)
	}
}

func testAuthorCommandCreate(t *testing.T) {
	root := t.TempDir()
	file := writeTicketFile(t, t.TempDir(), "new.yaml", "P1-E14-W3-S28-T1")
	h := handlers{}
	if err := h.RunCommand(context.Background(), createCommandName, []string{root, file}); err != nil {
		t.Fatalf("RunCommand create: %v", err)
	}
}

func testAuthorCreateMissingFile(t *testing.T) {
	root := t.TempDir()
	if err := RunCreate(root, filepath.Join(root, "missing.yaml"), "P1"); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Errorf("err = %v, want KindNotFound", err)
	}
}

func testAuthorRunEdit(t *testing.T) {
	root := t.TempDir()
	file := writeTicketFile(t, t.TempDir(), "t.yaml", "P1-E14-W3-S28-T1")
	if err := RunCreate(root, file, "P1"); err != nil {
		t.Fatalf("RunCreate: %v", err)
	}
	if err := RunEdit(root, "P1-E14-W3-S28-T1", file, "P1"); err != nil {
		t.Fatalf("RunEdit: %v", err)
	}
}

func testAuthorEditIDMismatch(t *testing.T) {
	root := t.TempDir()
	file := writeTicketFile(t, t.TempDir(), "t.yaml", "P1-E14-W3-S28-T1")
	if err := RunCreate(root, file, "P1"); err != nil {
		t.Fatalf("RunCreate: %v", err)
	}
	if err := RunEdit(root, "P1-E14-W3-S28-T2", file, "P1"); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("err = %v, want KindInvalidInput", err)
	}
}

func testAuthorCommandEdit(t *testing.T) {
	root := t.TempDir()
	file := writeTicketFile(t, t.TempDir(), "t.yaml", "P1-E14-W3-S28-T1")
	if err := RunCreate(root, file, "P1"); err != nil {
		t.Fatalf("RunCreate: %v", err)
	}
	h := handlers{}
	if err := h.RunCommand(context.Background(), editCommandName, []string{root, "P1-E14-W3-S28-T1", file}); err != nil {
		t.Fatalf("RunCommand edit: %v", err)
	}
}

func testAuthorRunMove(t *testing.T) {
	root := t.TempDir()
	file := writeTicketFile(t, t.TempDir(), "t.yaml", "P1-E14-W3-S28-T1")
	if err := RunCreate(root, file, "P1"); err != nil {
		t.Fatalf("RunCreate: %v", err)
	}
	if err := RunMove(root, "P1-E14-W3-S28-T1", "P1-E14-W3-S29-T1", "P1"); err != nil {
		t.Fatalf("RunMove: %v", err)
	}
}

func testAuthorCommandMove(t *testing.T) {
	root := t.TempDir()
	file := writeTicketFile(t, t.TempDir(), "t.yaml", "P1-E14-W3-S28-T1")
	if err := RunCreate(root, file, "P1"); err != nil {
		t.Fatalf("RunCreate: %v", err)
	}
	h := handlers{}
	if err := h.RunCommand(context.Background(), moveCommandName, []string{root, "P1-E14-W3-S28-T1", "P1-E14-W3-S29-T1"}); err != nil {
		t.Fatalf("RunCommand move: %v", err)
	}
}

func testAuthorCommandBadArgs(t *testing.T) {
	h := handlers{}
	root := t.TempDir()
	cases := []struct {
		name string
		args []string
	}{
		{createCommandName, nil},
		{createCommandName, []string{root}},
		{editCommandName, []string{root, "P1-E14-W3-S28-T1"}},
		{moveCommandName, []string{root, "P1-E14-W3-S28-T1"}},
	}
	for _, c := range cases {
		if err := h.RunCommand(context.Background(), c.name, c.args); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("RunCommand(%q, %v) err = %v, want KindInvalidInput", c.name, c.args, err)
		}
	}
}
