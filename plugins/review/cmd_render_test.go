// Purpose (this file): cmd_render.go's own tests -- specifically the
//
//	deterministic-sort assertion the adversarial review's MINOR 1 found
//	missing (a mutation deleting toReviewFindingViews' sort.Slice call
//	left `go test ./plugins/review/...` green, because no existing test
//	built a ReviewResponse with 2+ findings). Split from cmd_test.go
//	purely because that file is at Art.10.3's 300-line cap, matching
//	cmd_edgecases_test.go's identical reason for existing.
//
// SPORT: plugins/review:cmd_render_test (ADD) -- FIX P1-E25-W5-S52-T5 (D4).
package review

import (
	"strconv"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// TestToReviewFindingViewsIsSortedDeterministically proves the sort this
// ticket's REWORK fix (D4) requires: three findings supplied out of order
// (by file, then line, then message -- toReviewFindingViews' own tie-break
// chain) come back sorted. Deleting the sort.Slice call in cmd_render.go
// (the CR's own recorded mutation) makes this test fail: the unsorted
// input order ("z.go", "a.go", "a.go" with lines 5 then 1) is not already
// sorted, so a no-op sort would leave it visibly wrong.
func TestToReviewFindingViewsIsSortedDeterministically(t *testing.T) {
	in := []provider.ReviewFinding{
		{Severity: provider.ReviewSeverityMinor, File: "z.go", Line: 1, Message: "last file"},
		{Severity: provider.ReviewSeverityMajor, File: "a.go", Line: 5, Message: "second line"},
		{Severity: provider.ReviewSeverityBlocker, File: "a.go", Line: 1, Message: "b message"},
		{Severity: provider.ReviewSeverityNit, File: "a.go", Line: 1, Message: "a message"},
	}
	got := toReviewFindingViews(in)
	if len(got) != len(in) {
		t.Fatalf("len(got) = %d, want %d (no finding dropped or duplicated)", len(got), len(in))
	}
	want := []string{"a.go:1:a message", "a.go:1:b message", "a.go:5:second line", "z.go:1:last file"}
	for i, w := range want {
		row := got[i].File + ":" + strconv.Itoa(got[i].Line) + ":" + got[i].Message
		if row != w {
			t.Fatalf("position %d = %q, want %q (full order: %+v)", i, row, w, got)
		}
	}
}

// TestToReviewFindingViewsHandlesEmptyAndNil proves the "never nil"
// contract cmd_render.go's own header names: both nil and empty inputs
// come back as a non-nil, zero-length slice.
func TestToReviewFindingViewsHandlesEmptyAndNil(t *testing.T) {
	for _, in := range [][]provider.ReviewFinding{nil, {}} {
		got := toReviewFindingViews(in)
		if got == nil {
			t.Fatal("toReviewFindingViews returned nil, want a non-nil empty slice")
		}
		if len(got) != 0 {
			t.Fatalf("len(got) = %d, want 0", len(got))
		}
	}
}
