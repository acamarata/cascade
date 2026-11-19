package consult

import (
	"errors"
	"strings"
	"testing"
)

// TestPlanningBriefIncludesHint verifies that a non-empty dependency file list
// causes the no-redeclare hint to be included in the planning brief.
func TestPlanningBriefIncludesHint(t *testing.T) {
	got, err := BuildPlanningBrief("Plan X", []string{"a.go"})
	if err != nil {
		t.Fatalf("BuildPlanningBrief returned error: %v", err)
	}
	if !strings.Contains(got, "Do not redeclare types already defined") {
		t.Fatalf("BuildPlanningBrief output missing dependency hint, got %q", got)
	}
}

// TestPlanningBriefListsDepFiles verifies that dependency file names are listed
// as separate lines in the supplied order.
func TestPlanningBriefListsDepFiles(t *testing.T) {
	got, err := BuildPlanningBrief("Plan X", []string{"a.go", "b.go"})
	if err != nil {
		t.Fatalf("BuildPlanningBrief returned error: %v", err)
	}

	want := []string{"a.go", "b.go"}
	next := 0
	for _, line := range strings.Split(got, "\n") {
		if next < len(want) && line == want[next] {
			next++
		}
	}
	if next != len(want) {
		t.Fatalf("BuildPlanningBrief did not list dependency files in order; want %v, got %q", want, got)
	}
}

// TestPlanningBriefNoDepsUnchanged verifies that a brief with no dependency
// files is returned unchanged.
func TestPlanningBriefNoDepsUnchanged(t *testing.T) {
	got, err := BuildPlanningBrief("Plan X", nil)
	if err != nil {
		t.Fatalf("BuildPlanningBrief returned error: %v", err)
	}
	if got != "Plan X" {
		t.Fatalf("BuildPlanningBrief changed base without dependency files: got %q, want %q", got, "Plan X")
	}
}

// TestPlanningBriefEmptyBaseFailsClosed verifies that an empty base brief is
// rejected with ErrEmptyBrief instead of producing hint-only output.
func TestPlanningBriefEmptyBaseFailsClosed(t *testing.T) {
	_, err := BuildPlanningBrief("", []string{"a.go"})
	if !errors.Is(err, ErrEmptyBrief) {
		t.Fatalf("BuildPlanningBrief returned %v, want %v", err, ErrEmptyBrief)
	}
}
