// Purpose: normalize.go tests. Exercises the real, provenance-stamped
//
//	GitHub Actions API fixtures (Art.2) plus constructed cases for the
//	status/conclusion values this repo's own history never produced
//	(testdata/README.md's "Enum values NOT covered by a real fixture"
//	section) -- the fail-closed "unknown" mapping (06 §5.20) is what
//	makes covering those safely possible without a fabricated fixture.
//
// SPORT: internal.ci.normalizeActionsRuns/TESTED,
//
//	internal.ci.normalizeActionsJobs/TESTED (P1-E25-W5-S51-T2).
package ci

import (
	"os"
	"path/filepath"
	"testing"
)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "fixtures", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return b
}

// TestNormalizeActionsRuns_RealFixtures drives the real success/failure/
// cancelled captures (Art.2's real-counterpart requirement) and asserts
// each fixture's run-level conclusion normalizes to the matching member.
func TestNormalizeActionsRuns_RealFixtures(t *testing.T) {
	cases := []struct {
		fixture string
		want    RunConclusion
	}{
		{"runs_success.json", ConclusionSuccess},
		{"runs_failure.json", ConclusionFailure},
		{"runs_cancelled.json", ConclusionCancelled},
	}
	for _, tc := range cases {
		body := loadFixture(t, tc.fixture)
		runs, err := normalizeActionsRuns(body, 42)
		if err != nil {
			t.Fatalf("%s: normalizeActionsRuns: %v", tc.fixture, err)
		}
		if len(runs) != 1 {
			t.Fatalf("%s: got %d runs, want 1 (fixture is per_page=1)", tc.fixture, len(runs))
		}
		if runs[0].Status != RunStatusCompleted {
			t.Errorf("%s: Status = %q, want completed", tc.fixture, runs[0].Status)
		}
		if runs[0].Conclusion != tc.want {
			t.Errorf("%s: Conclusion = %q, want %q", tc.fixture, runs[0].Conclusion, tc.want)
		}
		if runs[0].RepoID != 42 {
			t.Errorf("%s: RepoID = %d, want 42 (caller-supplied, not on the wire)", tc.fixture, runs[0].RepoID)
		}
		if runs[0].RunID == 0 {
			t.Errorf("%s: RunID was not populated", tc.fixture)
		}
	}
}

// TestNormalizeActionsJobs_RealFixture drives the real 18-job capture and
// asserts the mix of job/step conclusions it actually carries
// (success/failure/skipped) normalizes correctly, with no unknowns from a
// real, well-formed payload.
func TestNormalizeActionsJobs_RealFixture(t *testing.T) {
	body := loadFixture(t, "run_jobs.json")
	jobs, steps, err := normalizeActionsJobs(body)
	if err != nil {
		t.Fatalf("normalizeActionsJobs: %v", err)
	}
	if len(jobs) != 18 {
		t.Fatalf("got %d jobs, want 18 (the real capture)", len(jobs))
	}
	if len(steps) == 0 {
		t.Fatal("got zero steps from a fixture with real per-job steps arrays")
	}
	seen := map[RunConclusion]bool{}
	for _, j := range jobs {
		seen[j.Conclusion] = true
		if j.Conclusion == ConclusionUnknown {
			t.Errorf("job %d: a real, well-formed conclusion normalized to unknown", j.JobID)
		}
	}
	for _, s := range steps {
		if s.Conclusion == ConclusionUnknown {
			t.Errorf("job %d step %d: a real, well-formed conclusion normalized to unknown", s.JobID, s.Number)
		}
	}
	if !seen[ConclusionSuccess] || !seen[ConclusionFailure] {
		t.Errorf("expected both success and failure job conclusions in the real capture, saw %v", seen)
	}
}

// TestNormalizeConclusion_FailClosed covers every enum value the capture
// swept and found NO real fixture for (testdata/README.md), constructed
// explicitly per this ticket's own instruction, plus a value GitHub has
// never documented at all -- every one of them must map to "unknown",
// never be refused.
func TestNormalizeConclusion_FailClosed(t *testing.T) {
	outOfEnum := []string{"action_required", "neutral", "stale", "a-future-github-value"}
	for _, raw := range outOfEnum {
		if got := normalizeConclusion(raw); got != ConclusionUnknown {
			t.Errorf("normalizeConclusion(%q) = %q, want unknown", raw, got)
		}
	}
	for _, raw := range []string{"queued", "in_progress", "something-new"} {
		want := RunStatusUnknown
		if raw == "queued" {
			want = RunStatusQueued
		}
		if raw == "in_progress" {
			want = RunStatusInProgress
		}
		if got := normalizeRunStatus(raw); got != want {
			t.Errorf("normalizeRunStatus(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestNormalizeActionsRuns_MalformedJSON asserts a decode failure returns
// a taxonomy error and a nil slice, never a partially populated one.
func TestNormalizeActionsRuns_MalformedJSON(t *testing.T) {
	runs, err := normalizeActionsRuns([]byte(`{not valid json`), 1)
	if err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
	if runs != nil {
		t.Fatalf("expected a nil slice alongside the error, got %v", runs)
	}
}

// TestParseWireTime covers the empty/unparseable/valid three-way branch.
func TestParseWireTime(t *testing.T) {
	if got := parseWireTime(""); !got.IsZero() {
		t.Errorf("empty input: got %v, want zero time", got)
	}
	if got := parseWireTime("not-a-timestamp"); !got.IsZero() {
		t.Errorf("unparseable input: got %v, want zero time", got)
	}
	got := parseWireTime("2026-09-11T15:33:31Z")
	if got.IsZero() {
		t.Error("valid RFC3339 input parsed to zero time")
	}
}
