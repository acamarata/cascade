package ingest

import (
	"os"
	"path/filepath"
	"testing"
)

// TestOutlinePhaseHasARowWithoutTickets verifies that OutlinePhase returns
// a PhaseRow with an empty Tickets slice when the directory contains only PHASE.md.
func TestOutlinePhaseHasARowWithoutTickets(t *testing.T) {
	dir := t.TempDir()
	phaseDir := filepath.Join(dir, "alpha")
	if err := os.Mkdir(phaseDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(phaseDir, "PHASE.md"), []byte("# Alpha\n"), 0o644); err != nil {
		t.Fatalf("write PHASE.md: %v", err)
	}

	row, err := OutlinePhase(phaseDir)
	if err != nil {
		t.Fatalf("OutlinePhase error: %v", err)
	}
	if row == nil {
		t.Fatal("expected non-nil PhaseRow")
	}
	if len(row.Tickets) != 0 {
		t.Fatalf("expected empty Tickets, got %d", len(row.Tickets))
	}
	if row.Name != "alpha" {
		t.Fatalf("expected Name=alpha, got %s", row.Name)
	}
	if row.IsLegacy {
		t.Fatal("expected IsLegacy=false for outline phase")
	}
}

// TestLegacyPhaseIsARow verifies that ParseCurrentYAML returns exactly one
// PhaseRow with IsLegacy set to true.
func TestLegacyPhaseIsARow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "current.yaml")
	if err := os.WriteFile(path, []byte("legacy: true\n"), 0o644); err != nil {
		t.Fatalf("write current.yaml: %v", err)
	}

	rows, err := ParseCurrentYAML(path)
	if err != nil {
		t.Fatalf("ParseCurrentYAML error: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if !rows[0].IsLegacy {
		t.Fatal("expected IsLegacy=true")
	}
}
