// Purpose: waitmerge_wait.go tests. Every WaitOnGreen case uses a
// FixedClock plus a fake Sleeper that advances it directly -- never a real
// sleep -- so the timeout case is deterministic and instant (Art.11).
//
// SPORT: internal.ci.WaitOnGreen/TESTED (P1-E25-W5-S51-T3).
package ci

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

const waitRunsURL = "https://api.github.com/repos/acamarata/cascade/actions/runs?per_page=100&page=1"

func waitRunsBody(status, conclusion string) []byte {
	return []byte(`{"total_count":1,"workflow_runs":[{"id":501,"name":"ci","head_branch":"main",` +
		`"head_sha":"deadbeef","status":"` + status + `","conclusion":"` + conclusion +
		`","created_at":"2026-09-20T00:00:00Z","updated_at":"2026-09-20T00:01:00Z"}]}`)
}

func waitJobsURL() string {
	return "https://api.github.com/repos/acamarata/cascade/actions/runs/501/jobs?per_page=100"
}

func waitJobsBody(name, status, conclusion string) []byte {
	return []byte(`{"total_count":1,"jobs":[{"id":1,"run_id":501,"name":"` + name + `","status":"` + status +
		`","conclusion":"` + conclusion + `","started_at":"2026-09-20T00:00:00Z","completed_at":"2026-09-20T00:01:00Z"}]}`)
}

// fakeSleepLog is WaitOnGreen's Sleeper double: it advances a FixedClock
// directly rather than sleeping, so a timeout test resolves instantly.
type fakeSleepLog struct {
	clock *runtime.FixedClock
	calls int
	err   error
}

func (f *fakeSleepLog) sleep(ctx context.Context, d time.Duration) error {
	f.calls++
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.err != nil {
		return f.err
	}
	f.clock.Advance(d)
	return nil
}

func waitTestDeps(t *testing.T, doer *fakeDoer, clock *runtime.FixedClock, sleep Sleeper) WaitDeps {
	t.Helper()
	return WaitDeps{Client: NewClient(doer, testEngine(t), "", 1, allowActionsRoutes), Clock: clock, Sleep: sleep}
}

// TestWaitOnGreen_AllGreenSucceeds proves acceptance criterion 1's positive
// path: every required non-skipped check reports success.
func TestWaitOnGreen_AllGreenSucceeds(t *testing.T) {
	doer := &fakeDoer{responses: map[string]HTTPResponse{
		waitRunsURL:   {Status: 200, Body: waitRunsBody("completed", "success")},
		waitJobsURL(): {Status: 200, Body: waitJobsBody("build", "completed", "success")},
	}}
	clock := runtime.NewFixedClock(time.Unix(0, 0))
	sleeper := &fakeSleepLog{clock: clock}
	deps := waitTestDeps(t, doer, clock, sleeper.sleep)

	result, err := WaitOnGreen(context.Background(), deps, WaitOptions{Owner: "acamarata", Repo: "cascade", Ref: "main"})
	if err != nil {
		t.Fatalf("WaitOnGreen: %v", err)
	}
	if !result.Passed {
		t.Fatal("result.Passed = false, want true")
	}
	if len(result.Checks) != 1 || result.Checks[0].Name != "build" {
		t.Fatalf("Checks = %+v, want one check named build", result.Checks)
	}
}

// TestWaitOnGreen_PendingCheckIsNotGreen proves the "empty/incomplete
// required set is never a vacuous pass" guard (evaluateChecks): a job
// still in_progress must not resolve the wait. MUTATION TARGET: deleting
// evaluateChecks's `default: allDone = false` branch (folding ConclusionNone
// into the success case) makes this test hang until the injected timeout,
// then fail on Passed==true never being reached before KindTimeout.
func TestWaitOnGreen_PendingCheckIsNotGreen(t *testing.T) {
	doer := &fakeDoer{responses: map[string]HTTPResponse{
		waitRunsURL:   {Status: 200, Body: waitRunsBody("in_progress", "")},
		waitJobsURL(): {Status: 200, Body: waitJobsBody("build", "in_progress", "")},
	}}
	clock := runtime.NewFixedClock(time.Unix(0, 0))
	sleeper := &fakeSleepLog{clock: clock}
	deps := waitTestDeps(t, doer, clock, sleeper.sleep)

	_, err := WaitOnGreen(context.Background(), deps, WaitOptions{
		Owner: "acamarata", Repo: "cascade", Ref: "main", Timeout: 30 * time.Second, PollInterval: 10 * time.Second,
	})
	if !cascade.HasKind(err, cascade.KindTimeout) {
		t.Fatalf("err = %v, want KindTimeout (the pending check must never resolve green)", err)
	}
	if sleeper.calls < 2 {
		t.Errorf("sleeper.calls = %d, want the loop to have actually polled more than once before timing out", sleeper.calls)
	}
}

// TestWaitOnGreen_FailureShortCircuits proves the failure-short-circuit
// acceptance criterion. MUTATION TARGET: removing the
// ConclusionFailure/Cancelled/TimedOut case from evaluateChecks's switch
// turns this red (WaitOnGreen would instead time out, never returning
// KindConflict), and a second Poll would occur -- doer.calls would exceed 2.
func TestWaitOnGreen_FailureShortCircuits(t *testing.T) {
	doer := &fakeDoer{responses: map[string]HTTPResponse{
		waitRunsURL:   {Status: 200, Body: waitRunsBody("completed", "failure")},
		waitJobsURL(): {Status: 200, Body: waitJobsBody("build", "completed", "failure")},
	}}
	clock := runtime.NewFixedClock(time.Unix(0, 0))
	sleeper := &fakeSleepLog{clock: clock}
	deps := waitTestDeps(t, doer, clock, sleeper.sleep)

	_, err := WaitOnGreen(context.Background(), deps, WaitOptions{Owner: "acamarata", Repo: "cascade", Ref: "main"})
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("err = %v, want KindConflict", err)
	}
	if sleeper.calls != 0 {
		t.Errorf("sleeper.calls = %d, want 0 -- a failed check must short-circuit before ever sleeping again", sleeper.calls)
	}
	if len(doer.calls) != 2 {
		t.Errorf("doer.calls = %d, want exactly 2 (one PollRuns, one PollJobs) -- no second poll pass", len(doer.calls))
	}
}

// TestWaitOnGreen_UnknownConclusionFailsClosed proves acceptance criterion
// 3: an unrecognized conclusion (GitHub's "neutral", not in this ticket's
// canonical enum per normalize.go) fails closed rather than being treated
// as still-pending.
func TestWaitOnGreen_UnknownConclusionFailsClosed(t *testing.T) {
	doer := &fakeDoer{responses: map[string]HTTPResponse{
		waitRunsURL:   {Status: 200, Body: waitRunsBody("completed", "neutral")},
		waitJobsURL(): {Status: 200, Body: waitJobsBody("build", "completed", "neutral")},
	}}
	clock := runtime.NewFixedClock(time.Unix(0, 0))
	sleeper := &fakeSleepLog{clock: clock}
	deps := waitTestDeps(t, doer, clock, sleeper.sleep)

	_, err := WaitOnGreen(context.Background(), deps, WaitOptions{Owner: "acamarata", Repo: "cascade", Ref: "main"})
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("err = %v, want KindConflict for an unrecognized conclusion", err)
	}
}

// TestWaitOnGreen_TimeoutExpires proves acceptance criterion 2: the
// configured timeout returns KindTimeout, with no real sleep involved.
func TestWaitOnGreen_TimeoutExpires(t *testing.T) {
	doer := &fakeDoer{responses: map[string]HTTPResponse{
		waitRunsURL:   {Status: 200, Body: waitRunsBody("in_progress", "")},
		waitJobsURL(): {Status: 200, Body: waitJobsBody("build", "in_progress", "")},
	}}
	clock := runtime.NewFixedClock(time.Unix(0, 0))
	sleeper := &fakeSleepLog{clock: clock}
	deps := waitTestDeps(t, doer, clock, sleeper.sleep)

	start := time.Now()
	_, err := WaitOnGreen(context.Background(), deps, WaitOptions{
		Owner: "acamarata", Repo: "cascade", Ref: "main", Timeout: time.Minute, PollInterval: 10 * time.Second,
	})
	if time.Since(start) > 500*time.Millisecond {
		t.Fatalf("WaitOnGreen took %s wall-clock time -- it slept for real instead of using the injected Sleeper", time.Since(start))
	}
	if !cascade.HasKind(err, cascade.KindTimeout) {
		t.Fatalf("err = %v, want KindTimeout", err)
	}
}

// TestWaitOnGreen_ContextCanceled proves ctx cancellation is observed
// before the deadline.
func TestWaitOnGreen_ContextCanceled(t *testing.T) {
	doer := &fakeDoer{}
	clock := runtime.NewFixedClock(time.Unix(0, 0))
	sleeper := &fakeSleepLog{clock: clock}
	deps := waitTestDeps(t, doer, clock, sleeper.sleep)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := WaitOnGreen(ctx, deps, WaitOptions{Owner: "acamarata", Repo: "cascade", Ref: "main"})
	if !cascade.HasKind(err, cascade.KindCanceled) {
		t.Fatalf("err = %v, want KindCanceled", err)
	}
	if len(doer.calls) != 0 {
		t.Errorf("doer.calls = %d, want 0 -- a canceled context must refuse before any poll", len(doer.calls))
	}
}

// TestWaitOnGreen_SleepCancellationSurfaces proves a Sleeper cancellation
// (context canceled mid-wait) surfaces as KindCanceled, not a silent retry.
func TestWaitOnGreen_SleepCancellationSurfaces(t *testing.T) {
	doer := &fakeDoer{responses: map[string]HTTPResponse{
		waitRunsURL:   {Status: 200, Body: waitRunsBody("in_progress", "")},
		waitJobsURL(): {Status: 200, Body: waitJobsBody("build", "in_progress", "")},
	}}
	clock := runtime.NewFixedClock(time.Unix(0, 0))
	sleeper := &fakeSleepLog{clock: clock, err: context.Canceled}
	deps := waitTestDeps(t, doer, clock, sleeper.sleep)

	_, err := WaitOnGreen(context.Background(), deps, WaitOptions{Owner: "acamarata", Repo: "cascade", Ref: "main"})
	if !cascade.HasKind(err, cascade.KindCanceled) {
		t.Fatalf("err = %v, want KindCanceled", err)
	}
}

func TestWaitOptions_Validate(t *testing.T) {
	if err := (WaitOptions{}).validate(); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("empty WaitOptions: err = %v, want KindInvalidInput", err)
	}
	if err := (WaitOptions{Owner: "a", Repo: "b"}).validate(); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("no ref: err = %v, want KindInvalidInput", err)
	}
}

func TestWaitDeps_Validate(t *testing.T) {
	if err := (WaitDeps{}).validate(); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("empty WaitDeps: err = %v, want KindInvalidInput", err)
	}
}

func TestContextSleep_HonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ContextSleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("ContextSleep = %v, want context.Canceled", err)
	}
}

func TestContextSleep_ReturnsAfterDuration(t *testing.T) {
	if err := ContextSleep(context.Background(), 5*time.Millisecond); err != nil {
		t.Fatalf("ContextSleep: %v", err)
	}
}

func TestWaitDaemonAbsentRefusal_NonWindowsIsNil(t *testing.T) {
	if err := waitDaemonAbsentRefusal(); err != nil {
		t.Fatalf("waitDaemonAbsentRefusal() = %v, want nil on this platform", err)
	}
}
