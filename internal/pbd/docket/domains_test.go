package docket

import (
	"testing"
)

func TestFeedReadsOnlyListedSections(t *testing.T) {
	s := Store{
		"a": {{ID: "a1"}, {ID: "a2"}},
		"b": {{ID: "b1"}},
		"c": {{ID: "c1"}},
	}
	railfeed := []byte(`{"sections":["a","b"]}`)
	got := Feed(s, railfeed)

	gotIDs := make(map[string]bool)
	for _, e := range got {
		gotIDs[e.ID] = true
	}

	expected := []string{"a1", "a2", "b1"}
	for _, id := range expected {
		if !gotIDs[id] {
			t.Errorf("expected entry %s in feed", id)
		}
	}
	if len(got) != len(expected) {
		t.Errorf("expected %d entries, got %d", len(expected), len(got))
	}
}

func TestUnlistedSectionExcluded(t *testing.T) {
	s := Store{
		"a": {{ID: "a1"}},
		"b": {{ID: "b1"}},
		"c": {{ID: "c1"}},
	}
	railfeed := []byte(`{"sections":["a","b"]}`)
	got := Feed(s, railfeed)

	for _, e := range got {
		if e.ID == "c1" {
			t.Errorf("entry from unlisted section 'c' should be excluded")
		}
	}
}

func TestParentIsNearestAncestor(t *testing.T) {
	nodes := []Node{
		{ID: "root"},
		{ID: "mid", Parent: "root"},
		{ID: "leaf", Parent: "mid"},
	}
	got := Parent(nodes, "leaf")
	if got != "mid" {
		t.Errorf("expected parent 'mid', got %q", got)
	}
}

func TestParentSkipsShells(t *testing.T) {
	nodes := []Node{
		{ID: "root"},
		{ID: "shell", Parent: "root", Shell: true},
		{ID: "leaf", Parent: "shell"},
	}
	got := Parent(nodes, "leaf")
	if got != "root" {
		t.Errorf("expected parent 'root' (skipping shell), got %q", got)
	}
}

func TestMissingRailfeedFailsClosed(t *testing.T) {
	s := Store{
		"a": {{ID: "a1"}},
	}
	got := Feed(s, nil)
	if len(got) != 0 {
		t.Errorf("expected empty feed when railfeed is absent, got %d entries", len(got))
	}

	got = Feed(s, []byte{})
	if len(got) != 0 {
		t.Errorf("expected empty feed when railfeed is empty, got %d entries", len(got))
	}
}
