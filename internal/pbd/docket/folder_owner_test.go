package docket

import (
	"testing"
)

// TestPlanOwnsWholeFolder verifies that a plan (HasTickets true) owns its
// entire repository folder, so any card path under that folder maps to it.
func TestPlanOwnsWholeFolder(t *testing.T) {
	cards := []Card{{Folder: "repo/a/b/c"}}
	projects := []Project{{Name: "a", Folder: "repo/a", HasTickets: true}}

	got, err := ResolveOwner(cards, projects)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if owner, ok := got["repo/a/b/c"]; !ok {
		t.Errorf("expected folder repo/a/b/c to have an owner, but it was absent")
	} else if owner != "a" {
		t.Errorf("expected owner %q, got %q", "a", owner)
	}
}

// TestLegacyPointerOwnsExactFolder verifies that a legacy pointer
// (HasTickets false) owns only its exact folder.
func TestLegacyPointerOwnsExactFolder(t *testing.T) {
	cards := []Card{{Folder: "repo/x"}}
	projects := []Project{{Name: "x", Folder: "repo/x", HasTickets: false}}

	got, err := ResolveOwner(cards, projects)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if owner, ok := got["repo/x"]; !ok {
		t.Errorf("expected folder repo/x to have an owner, but it was absent")
	} else if owner != "x" {
		t.Errorf("expected owner %q, got %q", "x", owner)
	}
}

// TestLegacyPointerRejectsSubfolder verifies that a legacy pointer does
// not match cards in subfolders.
func TestLegacyPointerRejectsSubfolder(t *testing.T) {
	cards := []Card{{Folder: "repo/x/sub"}}
	projects := []Project{{Name: "x", Folder: "repo/x", HasTickets: false}}

	got, err := ResolveOwner(cards, projects)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := got["repo/x/sub"]; ok {
		t.Errorf("expected folder repo/x/sub to have no owner, but it was present")
	}
}

// TestUnownedFolderReturnsNone verifies that a card whose folder is not
// owned by any project returns no owner and no error.
func TestUnownedFolderReturnsNone(t *testing.T) {
	cards := []Card{{Folder: "repo/zzz"}}
	projects := []Project{{Name: "x", Folder: "repo/x", HasTickets: false}}

	got, err := ResolveOwner(cards, projects)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := got["repo/zzz"]; ok {
		t.Errorf("expected folder repo/zzz to have no owner, but it was present")
	}
}
