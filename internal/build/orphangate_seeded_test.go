package build

// Purpose: mutation proofs for the predicates in orphangate.go, against
// synthetic fixtures rather than the real testonly-allow.json (that file
// is edited concurrently by every lane on this phase; a test that
// requires a specific real-tree shape to fail is a test that breaks the
// moment someone else's ticket lands). Each RED case here has a matching
// GREEN case one field away, so a rejection is attributable to the
// mutated field alone, the same shape testonlygate_validation_test.go
// already uses for the sibling gate.

import (
	"os"
	"path/filepath"
	"testing"
)

// orphanFixtureEntry builds a well-formed entry with only the fields a
// test below cares about varied.
func orphanFixtureEntry(symbol, retireTicket, callerSite, addedBy string) TestOnlyAllowEntry {
	return TestOnlyAllowEntry{
		Symbol: symbol, Reason: "fixture", Caller: "fixture",
		RetireTicket: retireTicket, CallerSite: callerSite, AddedByTicket: addedBy,
	}
}

// TestFindOrphanedAllowEntries_Table covers every shape the predicate
// must distinguish: UNOWNED is never orphaned regardless of closed;
// closed + missing file is orphaned; closed + existing file is not;
// still-open is not, no matter how plausible the caller_site looks.
func TestFindOrphanedAllowEntries_Table(t *testing.T) {
	root := t.TempDir()
	existingCaller := filepath.Join(root, "internal", "widget", "wired.go")
	if err := os.MkdirAll(filepath.Dir(existingCaller), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(existingCaller, []byte("package widget\n"), 0o644); err != nil {
		t.Fatalf("writing fixture caller: %v", err)
	}

	allow := map[string]TestOnlyAllowEntry{
		"internal/a.Unowned":  orphanFixtureEntry("internal/a.Unowned", UnownedTicket, "internal/a/never.go", ""),
		"internal/b.Orphaned": orphanFixtureEntry("internal/b.Orphaned", "P1-E01-W1-S01-T1", "internal/b/never.go", ""),
		"internal/widget.Wired": orphanFixtureEntry(
			"internal/widget.Wired", "P1-E01-W1-S01-T1", "internal/widget/wired.go", ""),
		"internal/c.StillOpen": orphanFixtureEntry("internal/c.StillOpen", "P1-E99-W9-S99-T9", "internal/c/never.go", ""),
	}
	closed := map[string]bool{"P1-E01-W1-S01-T1": true}

	got := FindOrphanedAllowEntries(root, allow, closed)
	if len(got) != 1 || got[0].Symbol != "internal/b.Orphaned" {
		t.Fatalf("FindOrphanedAllowEntries = %+v, want exactly [internal/b.Orphaned]", got)
	}
}

// TestFindSelfReferentialAllowEntries_RedThenGreen: an entry whose
// AddedByTicket equals its own RetireTicket is a closed loop (RED); the
// identical entry with a DIFFERENT AddedByTicket, or none recorded, is
// not (GREEN), with no other field changed.
func TestFindSelfReferentialAllowEntries_RedThenGreen(t *testing.T) {
	t.Run("red: self-referential", func(t *testing.T) {
		allow := map[string]TestOnlyAllowEntry{
			"internal/a.Foo": orphanFixtureEntry("internal/a.Foo", "P1-E01-W1-S01-T1", "internal/a/x.go",
				"P1-E01-W1-S01-T1"),
		}
		got := FindSelfReferentialAllowEntries(allow)
		if len(got) != 1 || got[0] != "internal/a.Foo" {
			t.Fatalf("FindSelfReferentialAllowEntries = %v, want exactly [internal/a.Foo]", got)
		}
	})

	t.Run("green: different authoring ticket", func(t *testing.T) {
		allow := map[string]TestOnlyAllowEntry{
			"internal/a.Foo": orphanFixtureEntry("internal/a.Foo", "P1-E01-W1-S01-T1", "internal/a/x.go",
				"P1-E02-W1-S02-T2"),
		}
		if got := FindSelfReferentialAllowEntries(allow); len(got) != 0 {
			t.Fatalf("FindSelfReferentialAllowEntries = %v, want none", got)
		}
	})

	t.Run("green: provenance not recorded", func(t *testing.T) {
		allow := map[string]TestOnlyAllowEntry{
			"internal/a.Foo": orphanFixtureEntry("internal/a.Foo", "P1-E01-W1-S01-T1", "internal/a/x.go", ""),
		}
		if got := FindSelfReferentialAllowEntries(allow); len(got) != 0 {
			t.Fatalf("FindSelfReferentialAllowEntries = %v, want none (legacy entries record no provenance)", got)
		}
	})
}

// TestLoadClosedTicketManifest_FailsLoud proves the CI-visibility
// contract: a missing, empty-tickets, or malformed manifest is always an
// error, never an empty set standing in for "nothing to check".
func TestLoadClosedTicketManifest_FailsLoud(t *testing.T) {
	dir := t.TempDir()

	t.Run("missing file", func(t *testing.T) {
		if _, err := LoadClosedTicketManifest(filepath.Join(dir, "missing.json")); err == nil {
			t.Fatal("expected an error for a missing manifest")
		}
	})

	t.Run("malformed json", func(t *testing.T) {
		p := filepath.Join(dir, "malformed.json")
		if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
			t.Fatalf("writing fixture: %v", err)
		}
		if _, err := LoadClosedTicketManifest(p); err == nil {
			t.Fatal("expected an error for malformed json")
		}
	})

	t.Run("zero tickets", func(t *testing.T) {
		p := filepath.Join(dir, "empty.json")
		if err := os.WriteFile(p, []byte(`{"generated_at":"2026-09-13T00:00:00Z","tickets":[]}`), 0o644); err != nil {
			t.Fatalf("writing fixture: %v", err)
		}
		if _, err := LoadClosedTicketManifest(p); err == nil {
			t.Fatal("expected an error for a zero-ticket manifest")
		}
	})
}

// TestLoadClosedTicketManifest_ValidFile is the positive control for the
// three refusals above: the same shape, populated, loads cleanly.
func TestLoadClosedTicketManifest_ValidFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "closed.json")
	body := `{"generated_at":"2026-09-13T00:00:00Z","tickets":["P1-E01-W1-S01-T1","P1-E01-W1-S01-T2"]}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	got, err := LoadClosedTicketManifest(p)
	if err != nil {
		t.Fatalf("LoadClosedTicketManifest: %v", err)
	}
	if len(got) != 2 || !got["P1-E01-W1-S01-T1"] || !got["P1-E01-W1-S01-T2"] {
		t.Fatalf("LoadClosedTicketManifest = %v, want both fixture tickets", got)
	}
}

// TestTrackedDeadSurfaceCount_Arithmetic pins gate 4's whole point: the
// two categories are added, not reported separately.
func TestTrackedDeadSurfaceCount_Arithmetic(t *testing.T) {
	allow := map[string]TestOnlyAllowEntry{
		"internal/a.One": orphanFixtureEntry("internal/a.One", UnownedTicket, "internal/a/x.go", ""),
		"internal/a.Two": orphanFixtureEntry("internal/a.Two", UnownedTicket, "internal/a/y.go", ""),
	}
	orphaned := []OrphanedAllowEntry{{Symbol: "internal/b.Three", RetireTicket: "P1-E01-W1-S01-T1", CallerSite: "z.go"}}
	if got := TrackedDeadSurfaceCount(allow, orphaned); got != 3 {
		t.Fatalf("TrackedDeadSurfaceCount = %d, want 3 (2 UNOWNED + 1 orphaned)", got)
	}
}
