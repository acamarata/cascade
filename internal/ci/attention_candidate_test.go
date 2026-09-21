package ci

// Purpose: the BuildCandidate/matchesWatch/attentionSourceRef unit tests
// split out of attention_test.go purely to keep that file under
// Art.10.3's 300-line cap. Same coverage target (P1-E25-W5-S51-T4), same
// package, same helpers (matchedWatch/failingChecks defined in
// attention_test.go).
//
// SPORT: internal.ci (TEST) -- P1-E25-W5-S51-T4.

import (
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
)

// TestMatchesWatch_GlobPatterns exercises path.Match-based matching
// directly: "*" wildcards, the absence of "**" recursive semantics, and
// case sensitivity. The `workflow` column is the ACTIONS WORKFLOW name
// (WaitResult.WorkflowName), never a job name.
func TestMatchesWatch_GlobPatterns(t *testing.T) {
	cases := []struct {
		name                   string
		watch                  runtime.CIWatchEntry
		repo, branch, workflow string
		want                   bool
	}{
		{"exact repo match, no filters", runtime.CIWatchEntry{Repo: "acamarata/cascade"}, "acamarata/cascade", "main", "build", true},
		{"repo owner wildcard", runtime.CIWatchEntry{Repo: "acamarata/*"}, "acamarata/cascade", "main", "build", true},
		{"repo wildcard does not cross the slash boundary", runtime.CIWatchEntry{Repo: "acamarata/*"}, "other/cascade", "main", "build", false},
		{"branch glob matches", runtime.CIWatchEntry{Repo: "acamarata/cascade", Branch: "release-*"}, "acamarata/cascade", "release-1.0", "build", true},
		{"branch glob does not match", runtime.CIWatchEntry{Repo: "acamarata/cascade", Branch: "release-*"}, "acamarata/cascade", "main", "build", false},
		{"no double-star recursive semantics: single * stops at literal text", runtime.CIWatchEntry{Repo: "acamarata/cascade", Workflow: "build-*"}, "acamarata/cascade", "main", "release-build-amd64", false},
		{"workflow glob matches a prefix", runtime.CIWatchEntry{Repo: "acamarata/cascade", Workflow: "build-*"}, "acamarata/cascade", "main", "build-amd64", true},
		{"case sensitive repo mismatch", runtime.CIWatchEntry{Repo: "Acamarata/Cascade"}, "acamarata/cascade", "main", "build", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := matchesWatch([]runtime.CIWatchEntry{tc.watch}, tc.repo, tc.branch, tc.workflow)
			if got != tc.want {
				t.Errorf("matchesWatch(%+v, %q, %q, %q) = %v, want %v", tc.watch, tc.repo, tc.branch, tc.workflow, got, tc.want)
			}
		})
	}
}

// TestWorkflowGlobMatchesTheWorkflowNotAJob is CR finding 4's failing
// input, pinned: `--workflow release` must match the RELEASE workflow's
// failure and must NOT match a job called "release" inside an unrelated
// workflow.
func TestWorkflowGlobMatchesTheWorkflowNotAJob(t *testing.T) {
	watches := []runtime.CIWatchEntry{{Repo: "acamarata/cascade", Workflow: "release"}}
	result := failingResult()
	result.WorkflowName = "release"
	result.Checks = []CheckStatus{{Name: "build-amd64", Status: RunStatusCompleted, Conclusion: ConclusionFailure}}
	c, ok := BuildCandidate(result)
	if !ok {
		t.Fatalf("BuildCandidate: ok=false, want true")
	}
	if !matchesWatch(watches, c.Repo, c.Ref, c.Workflow) {
		t.Errorf("the release workflow's failure did not match --workflow release")
	}

	nightly := failingResult()
	nightly.WorkflowName = "nightly"
	nightly.Checks = []CheckStatus{{Name: "release", Status: RunStatusCompleted, Conclusion: ConclusionFailure}}
	c2, ok := BuildCandidate(nightly)
	if !ok {
		t.Fatalf("BuildCandidate (nightly): ok=false, want true")
	}
	if matchesWatch(watches, c2.Repo, c2.Ref, c2.Workflow) {
		t.Errorf("a JOB named release inside the nightly workflow matched --workflow release; it must not")
	}
}

// TestAttentionSourceRef_IsTheRunIdentity proves the dedup key is the
// run's identity and nothing else: the same run observed with a different
// failed-job set, or in a different job order, yields the identical
// SourceRef (one item per failed run), while a different run id does not.
func TestAttentionSourceRef_IsTheRunIdentity(t *testing.T) {
	first := failingResult()
	second := failingResult()
	second.Checks = []CheckStatus{
		{Name: "test", Status: RunStatusCompleted, Conclusion: ConclusionFailure},
		{Name: "build-amd64", Status: RunStatusCompleted, Conclusion: ConclusionFailure},
	}
	c1, ok1 := BuildCandidate(first)
	c2, ok2 := BuildCandidate(second)
	if !ok1 || !ok2 {
		t.Fatalf("BuildCandidate: ok1=%v ok2=%v, want true/true", ok1, ok2)
	}
	if attentionSourceRef(c1) != attentionSourceRef(c2) {
		t.Errorf("SourceRef changed when the failed-job set changed:\n  %s\n  %s",
			attentionSourceRef(c1), attentionSourceRef(c2))
	}
	if want := "ci:acamarata/cascade:42"; attentionSourceRef(c1) != want {
		t.Errorf("SourceRef = %q, want the exact identity %q", attentionSourceRef(c1), want)
	}

	other := failingResult()
	other.RunID = 43
	c3, ok3 := BuildCandidate(other)
	if !ok3 {
		t.Fatalf("BuildCandidate: ok3=false")
	}
	if attentionSourceRef(c1) == attentionSourceRef(c3) {
		t.Errorf("a different run_id produced an identical SourceRef; distinct failures must not dedup")
	}
}

// TestFailedJobs_SortedForDeterminism pins CR finding 5's duplicate input
// B: a paginated PollJobs returning the same two failures in the opposite
// order must produce the same failed-job list.
func TestFailedJobs_SortedForDeterminism(t *testing.T) {
	forward := failedJobs([]CheckStatus{
		{Name: "build", Conclusion: ConclusionFailure},
		{Name: "test", Conclusion: ConclusionTimedOut},
	})
	reverse := failedJobs([]CheckStatus{
		{Name: "test", Conclusion: ConclusionTimedOut},
		{Name: "build", Conclusion: ConclusionFailure},
	})
	if len(forward) != 2 || len(reverse) != 2 {
		t.Fatalf("failedJobs lengths = %d/%d, want 2/2", len(forward), len(reverse))
	}
	for i := range forward {
		if forward[i] != reverse[i] {
			t.Fatalf("failedJobs is order-dependent: %+v vs %+v", forward, reverse)
		}
	}
}

// TestBuildCandidate_RunConclusionIsTheRoutingDecision replaces the
// assertion that used to RATIFY dropping a genuine failure whose
// individual checks carried no routable conclusion (CR finding 2). The
// run's own conclusion decides.
func TestBuildCandidate_RunConclusionIsTheRoutingDecision(t *testing.T) {
	cases := []struct {
		name       string
		conclusion RunConclusion
		checks     []CheckStatus
		wantOK     bool
	}{
		{"startup_failure normalizes to unknown and routes with zero jobs", ConclusionUnknown, nil, true},
		{"failure routes even when every job is success or skipped", ConclusionFailure, []CheckStatus{
			{Name: "lint", Status: RunStatusCompleted, Conclusion: ConclusionSuccess},
			{Name: "test", Status: RunStatusCompleted, Conclusion: ConclusionSkipped},
		}, true},
		{"success never routes", ConclusionSuccess, failingChecks(), false},
		{"an unconcluded run never routes", ConclusionNone, failingChecks(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := failingResult()
			result.Conclusion = tc.conclusion
			result.Checks = tc.checks
			c, ok := BuildCandidate(result)
			if ok != tc.wantOK {
				t.Fatalf("BuildCandidate ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && c.Conclusion != tc.conclusion {
				t.Errorf("Conclusion = %q, want the run's own %q carried verbatim", c.Conclusion, tc.conclusion)
			}
		})
	}
}

// TestBuildCandidate_WorstConclusionWins now proves the RUN conclusion is
// what the candidate carries, and that every routable check is still
// listed in FailedJobs alongside it.
func TestBuildCandidate_WorstConclusionWins(t *testing.T) {
	result := failingResult()
	result.Conclusion = ConclusionFailure
	result.Checks = []CheckStatus{
		{Name: "a", Status: RunStatusCompleted, Conclusion: ConclusionTimedOut},
		{Name: "b", Status: RunStatusCompleted, Conclusion: ConclusionFailure},
	}
	c, ok := BuildCandidate(result)
	if !ok {
		t.Fatalf("BuildCandidate: ok=false, want true")
	}
	if c.Conclusion != ConclusionFailure {
		t.Errorf("Conclusion = %q, want the run's own failure", c.Conclusion)
	}
	if len(c.FailedJobs) != 2 {
		t.Errorf("FailedJobs = %+v, want both routable checks listed", c.FailedJobs)
	}
}
