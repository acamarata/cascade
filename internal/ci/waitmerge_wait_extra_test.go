// Purpose: white-box unit coverage for wait-on-green's unexported helpers
// (findRunForRef, requiredCheckNames, pollOnce's NotModified branches),
// split out of waitmerge_wait_test.go purely to keep both files under
// Art.10.3's 300-line cap. evaluateChecks has its own file
// (waitmerge_checks_test.go), matching the implementation split.
//
// SPORT: internal.ci.WaitOnGreen/TESTED (P1-E25-W5-S51-T3).
package ci

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestFindRunForRef_PicksHighestRunIDAndRefusesNoMatch(t *testing.T) {
	runs := []Run{{RunID: 1, HeadSHA: "x"}, {RunID: 5, HeadSHA: "x"}, {RunID: 3, HeadBranch: "main"}}
	r, ok := findRunForRef(runs, "x")
	if !ok || r.RunID != 5 {
		t.Fatalf("got %+v ok=%v, want the highest RunID (5) matching HeadSHA", r, ok)
	}
	if _, ok := findRunForRef(runs, "nope"); ok {
		t.Fatal("want no match for an unrelated ref")
	}
}

func TestRequiredCheckNames_ExplicitListOverridesAutoDiscovery(t *testing.T) {
	opts := WaitOptions{RequiredChecks: []string{"lint"}}
	got := requiredCheckNames(opts, []Job{{Name: "build", Conclusion: ConclusionSuccess}})
	if len(got) != 1 || got[0] != "lint" {
		t.Fatalf("got %v, want the explicit list verbatim, ignoring the jobs actually reported", got)
	}
}

func TestPollOnce_RunsNotModifiedReusesLastJobs(t *testing.T) {
	doer := &fakeDoer{responses: map[string]HTTPResponse{waitRunsURL: {Status: 304}}}
	clock := runtime.NewFixedClock(time.Unix(0, 0))
	deps := waitTestDeps(t, doer, clock, (&fakeSleepLog{clock: clock}).sleep)
	lastJobs := []Job{{Name: "build"}}

	run, jobs, err := deps.pollOnce(context.Background(), WaitOptions{Owner: "acamarata", Repo: "cascade", Ref: "main"}, lastJobs)
	if err != nil {
		t.Fatalf("pollOnce: %v", err)
	}
	if run.RunID != 0 {
		t.Fatalf("run = %+v, want the zero Run on a 304", run)
	}
	if len(jobs) != 1 || jobs[0].Name != "build" {
		t.Fatalf("jobs = %+v, want lastJobs reused verbatim", jobs)
	}
}

func TestPollOnce_JobsNotModifiedReusesLastJobs(t *testing.T) {
	doer := &fakeDoer{responses: map[string]HTTPResponse{
		waitRunsURL:   {Status: 200, Body: waitRunsBody("in_progress", "")},
		waitJobsURL(): {Status: 304},
	}}
	clock := runtime.NewFixedClock(time.Unix(0, 0))
	deps := waitTestDeps(t, doer, clock, (&fakeSleepLog{clock: clock}).sleep)
	lastJobs := []Job{{Name: "build"}}

	run, jobs, err := deps.pollOnce(context.Background(), WaitOptions{Owner: "acamarata", Repo: "cascade", Ref: "main"}, lastJobs)
	if err != nil {
		t.Fatalf("pollOnce: %v", err)
	}
	if run.RunID != 501 {
		t.Fatalf("run.RunID = %d, want 501 (the run itself is fresh)", run.RunID)
	}
	if len(jobs) != 1 || jobs[0].Name != "build" {
		t.Fatalf("jobs = %+v, want lastJobs reused on a jobs-only 304", jobs)
	}
}

func TestWaitOptions_ValidateEachRequiredField(t *testing.T) {
	cases := []WaitOptions{
		{Repo: "cascade", Ref: "main"},        // no owner
		{Owner: "acamarata", Ref: "main"},     // no repo
		{Owner: "acamarata", Repo: "cascade"}, // no ref
	}
	for _, o := range cases {
		if err := o.validate(); err == nil {
			t.Fatalf("%+v: want a validation error", o)
		}
	}
}

// countingTransientDoer fails EVERY call with the one kind
// isTransientPollError retries, and counts how many times it was asked.
type countingTransientDoer struct{ calls int }

func (d *countingTransientDoer) Do(context.Context, HTTPRequest) (HTTPResponse, error) {
	d.calls++
	return HTTPResponse{}, cascade.New(cascade.KindUnavailable, "ci: api.github.com answered 502")
}

// TestWaitOnGreen_RetriesNeverOutlastTheDeadline pins D7's
// timeout-ACROSS-retries half, which the confirming review's note 9 found
// held only by construction (the pre-poll deadline check) with no test
// driving a permanently flaky transport.
//
// A transport that never recovers must still end at the wait's own
// deadline: the backoff doubles 10s -> 20s -> 40s under the injected
// clock, and the fourth iteration's PRE-POLL deadline check fires instead
// of a fourth retry. MUTATION TARGET: removing the deadline check at the
// top of waitLoop makes this run forever (the backoff caps at 2m and never
// stops on its own).
func TestWaitOnGreen_RetriesNeverOutlastTheDeadline(t *testing.T) {
	skipWhenWaitIsRefusedHere(t)
	doer := &countingTransientDoer{}
	clock := runtime.NewFixedClock(time.Unix(0, 0))
	sleeper := &recordingSleeper{clock: clock}
	deps := WaitDeps{
		Client: NewClient(doer, testEngine(t), "", 1, allowActionsRoutes),
		Clock:  clock, Sleep: sleeper.sleep,
	}

	_, err := WaitOnGreen(context.Background(), deps, WaitOptions{
		Owner: "acamarata", Repo: "cascade", Ref: "main",
		Timeout: time.Minute, PollInterval: 10 * time.Second,
	})
	if !cascade.HasKind(err, cascade.KindTimeout) {
		t.Fatalf("err = %v, want KindTimeout: a permanently flaky transport must end at the deadline", err)
	}
	want := []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second}
	if !reflect.DeepEqual(sleeper.delays, want) {
		t.Fatalf("delays = %v, want %v (doubling, then stopped by the deadline)", sleeper.delays, want)
	}
	if doer.calls != len(want) {
		t.Fatalf("doer.calls = %d, want %d: no poll may be issued once the deadline has passed", doer.calls, len(want))
	}
	if got := clock.Now().Sub(time.Unix(0, 0)); got != 70*time.Second {
		t.Fatalf("elapsed = %s, want 70s -- the retries overran the 1m deadline by one backoff, never more", got)
	}
}

// TestWaitOnGreen_FailingResultCarriesWorkflowNameAndConclusion proves
// decide() stamps the RUN's own Conclusion and WorkflowName onto the
// WaitResult it returns even on the failure path -- P1-E25-W5-S51-T4's
// routing decision (attention.go) reads both fields off exactly this
// result. MUTATION TARGET: blanking waitmerge_wait.go's decide() assignment
// `Conclusion: run.Conclusion, WorkflowName: run.Name` (replacing it with
// ConclusionNone/"") leaves the returned error kind unchanged -- only these
// two fields go silently empty.
func TestWaitOnGreen_FailingResultCarriesWorkflowNameAndConclusion(t *testing.T) {
	skipWhenWaitIsRefusedHere(t)
	doer := &fakeDoer{responses: map[string]HTTPResponse{
		waitRunsURL:   {Status: 200, Body: waitRunsBody("completed", "cancelled")},
		waitJobsURL(): {Status: 200, Body: waitJobsBody("build", "completed", "cancelled")},
	}}
	clock := runtime.NewFixedClock(time.Unix(0, 0))
	sleeper := &fakeSleepLog{clock: clock}
	deps := waitTestDeps(t, doer, clock, sleeper.sleep)

	result, err := WaitOnGreen(context.Background(), deps, WaitOptions{Owner: "acamarata", Repo: "cascade", Ref: "main"})
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("err = %v, want KindConflict", err)
	}
	if result.WorkflowName != "ci" {
		t.Errorf("result.WorkflowName = %q, want %q (run.Name)", result.WorkflowName, "ci")
	}
	if result.Conclusion != ConclusionCancelled {
		t.Errorf("result.Conclusion = %q, want %q (run.Conclusion)", result.Conclusion, ConclusionCancelled)
	}
}
