// Package coverage (this file): unit tests over synthetic fixtures
// (always run, including in public CI) plus the guarded live-tree
// assertion, which only runs when .claude/planning/p1/phase exists
// locally (R-14.85 — the planning tree is gitignored and CI never sees
// it).
package coverage

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// --- LoadInventory -----------------------------------------------------

func TestLoadInventory_ParsesRowsAcrossSections(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "inventory.md")
	writeFile(t, path, "# Title\n\n"+
		"## Section One\n\n"+
		"| Feature | Disposition |\n|---|---|\n"+
		"| widget | CORE (K/S-23.T6) |\n"+
		"| gadget | PRODUCT-DEFER |\n\n"+
		"## Section Two\n\n"+
		"| Feature | Disposition |\n|---|---|\n"+
		"| gizmo | T1 |\n")
	rows, err := LoadInventory(path)
	if err != nil {
		t.Fatalf("LoadInventory: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3: %+v", len(rows), rows)
	}
	if rows[0].Section != "Section One" || rows[0].Index != 1 {
		t.Fatalf("rows[0] = %+v", rows[0])
	}
	if rows[2].Section != "Section Two" || rows[2].Index != 1 {
		t.Fatalf("rows[2] = %+v", rows[2])
	}
	if got := rows[0].RowID(); got != "Section One #1" {
		t.Fatalf("RowID() = %q", got)
	}
}

func TestLoadInventory_MissingFile(t *testing.T) {
	if _, err := LoadInventory(filepath.Join(t.TempDir(), "nope.md")); err == nil {
		t.Fatal("expected an error for a missing inventory file")
	}
}

func TestLoadInventory_EmptyInventoryFailsClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.md")
	writeFile(t, path, "# Title\n\nno tables here, just prose.\n")
	if _, err := LoadInventory(path); err == nil {
		t.Fatal("expected an error for zero parsed rows; a gate over an empty inventory always passes")
	}
}

// --- LoadTicketTree ------------------------------------------------------

func TestLoadTicketTree_WalksTicketsDirs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "epics", "E-K", "waves", "W-1", "sprints", "S-23", "tickets", "T-6.yaml"),
		"id: P1-E11-W1-S23-T6\ntitle: widget\n")
	writeFile(t, filepath.Join(root, "epics", "E-K", "waves", "W-1", "sprints", "S-23", "tickets", "T-7.yaml"),
		"id: P1-E11-W1-S23-T7\ntitle: gadget\n")
	recs, err := LoadTicketTree(root)
	if err != nil {
		t.Fatalf("LoadTicketTree: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d tickets, want 2: %+v", len(recs), recs)
	}
	if recs[0].ID != "P1-E11-W1-S23-T6" {
		t.Fatalf("recs[0].ID = %q", recs[0].ID)
	}
}

func TestLoadTicketTree_MissingIDFailsClosed(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "epics", "E-K", "waves", "W-1", "sprints", "S-23", "tickets", "T-6.yaml"),
		"title: widget, no id line\n")
	if _, err := LoadTicketTree(root); err == nil {
		t.Fatal("expected an error for a ticket YAML with no id: line")
	}
}

func TestLoadTicketTree_ZeroTicketsFailsClosed(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "epics", "E-K", "readme.md"), "not a ticket\n")
	if _, err := LoadTicketTree(root); err == nil {
		t.Fatal("expected an error for zero tickets found")
	}
}

// --- LoadDeferrals -------------------------------------------------------

func TestLoadDeferrals_ParsesList(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deferrals.yaml")
	writeFile(t, path, "deferrals:\n"+
		"- id: DEF-P2-example\n  source: X/S-1.T1\n  ruling: R-1.1\n  what: an example\n  reenter: P2\n")
	entries, err := LoadDeferrals(path)
	if err != nil {
		t.Fatalf("LoadDeferrals: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != "DEF-P2-example" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestLoadDeferrals_MissingFile(t *testing.T) {
	if _, err := LoadDeferrals(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Fatal("expected an error for a missing deferrals file")
	}
}

func TestLoadDeferrals_MalformedYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	writeFile(t, path, "deferrals: [this is not valid: yaml: at all\n")
	if _, err := LoadDeferrals(path); err == nil {
		t.Fatal("expected an error for malformed YAML")
	}
}

func TestLoadDeferrals_MissingIDFailsClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "noid.yaml")
	writeFile(t, path, "deferrals:\n- source: X/S-1.T1\n  what: no id field\n")
	if _, err := LoadDeferrals(path); err == nil {
		t.Fatal("expected an error for a deferral entry with no id")
	}
}

// --- ExtractCitations / ResolveTicketCitation -----------------------------

func TestExtractCitations(t *testing.T) {
	got := ExtractCitations("shipped as CORE (K/S-23.T6) and superseded by (AK/S-73.T2); deferred DEF-P2-graphrag")
	want := []string{"K/S-23.T6", "AK/S-73.T2", "DEF-P2-graphrag"}
	if len(got) != len(want) {
		t.Fatalf("ExtractCitations = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ExtractCitations[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestResolveTicketCitation(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".claude", "planning", "p1", "phase", "epics",
		"E-K", "waves", "W-3", "sprints", "S-23", "tickets", "T-6.yaml"), "id: P1-E11-W3-S23-T6\n")

	resolved, err := ResolveTicketCitation(root, "K/S-23.T6")
	if err != nil {
		t.Fatalf("ResolveTicketCitation: %v", err)
	}
	if !resolved {
		t.Fatal("expected K/S-23.T6 to resolve against the real fixture tree")
	}

	resolved, err = ResolveTicketCitation(root, "K/S-99.T1")
	if err != nil {
		t.Fatalf("ResolveTicketCitation: %v", err)
	}
	if resolved {
		t.Fatal("expected a nonexistent sprint/ticket to NOT resolve")
	}

	resolved, err = ResolveTicketCitation(root, "not a citation")
	if err != nil || resolved {
		t.Fatalf("ResolveTicketCitation on non-shaped input = (%v, %v), want (false, nil)", resolved, err)
	}
}
