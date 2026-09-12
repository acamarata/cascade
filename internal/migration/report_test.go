// Purpose: Report.String()'s human-mode rendering tests.
// SPORT: internal/migration [ADD] (P1-E26-W10-S53-T3 sport_updates).
package migration

import (
	"strings"
	"testing"
)

// TestReportStringEmpty covers the "nothing considered" branch.
func TestReportStringEmpty(t *testing.T) {
	if got := (Report{}).String(); got != "0 projects considered" {
		t.Fatalf("empty report String() = %q", got)
	}
}

// TestReportStringCoversEveryRowKind exercises the fresh, error, drifted,
// and applied rendering branches all at once, asserting each contributes
// its own recognizable substring rather than checking one giant literal
// (which would break on every unrelated tabwriter spacing change).
func TestReportStringCoversEveryRowKind(t *testing.T) {
	r := Report{
		Projects: []ProjectReport{
			{ProjectPath: "/fresh"},
			{ProjectPath: "/broken", Error: "boom"},
			{ProjectPath: "/stale", Drift: []DriftEntry{
				{HarnessFile: "/stale/.claude/CLAUDE.md", Added: 3, Removed: 1, DiffBody: "+new\n-old\n"},
			}},
		},
		Applied: []AppliedFile{{ProjectPath: "/stale", Path: "/stale/.claude/CLAUDE.md", Action: "updated"}},
		Partial: true,
	}
	got := r.String()
	for _, want := range []string{
		"/fresh", "(fresh)",
		"/broken", "error: boom",
		"/stale", "/stale/.claude/CLAUDE.md", "3", "1",
		"+new", "-old",
		"applied:", "updated",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("String() missing %q in:\n%s", want, got)
		}
	}
}

// TestReportStringNoAppliedOmitsSection asserts appliedLines contributes
// nothing (no stray "applied:" heading) when nothing was applied — the
// --check / decline path.
func TestReportStringNoAppliedOmitsSection(t *testing.T) {
	r := Report{Projects: []ProjectReport{{ProjectPath: "/fresh"}}}
	got := r.String()
	if strings.Contains(got, "applied:") {
		t.Fatalf("String() with no Applied entries must not mention 'applied:', got:\n%s", got)
	}
}
