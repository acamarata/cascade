// Purpose: the wait-on-green LOOP's control paths -- the platform gate, the
// never-pay pre-flight, and the transient-retry/terminal-abort split --
// split out of waitmerge_wait_test.go under Art.10.3's 300-line cap.
//
// SPORT: internal.ci.WaitOnGreen/TESTED (P1-E25-W5-S51-T3).
package ci

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// recordingSleeper is fakeSleepLog plus the delays it was asked for, so a
// backoff can be asserted rather than assumed.
type recordingSleeper struct {
	clock  *runtime.FixedClock
	delays []time.Duration
}

func (r *recordingSleeper) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.delays = append(r.delays, d)
	r.clock.Advance(d)
	return nil
}

// flakyDoer fails its first failures calls with err, then answers from
// inner -- one api.github.com 502 followed by a healthy response.
type flakyDoer struct {
	failures int
	err      error
	inner    *fakeDoer
}

func (f *flakyDoer) Do(ctx context.Context, req HTTPRequest) (HTTPResponse, error) {
	if f.failures > 0 {
		f.failures--
		return HTTPResponse{}, f.err
	}
	return f.inner.Do(ctx, req)
}

// TestWaitOnGreen_PlatformGateRunsBeforeAnyPoll proves AC7 on the
// PRODUCTION path (review finding 3: the refusal had zero production
// callers). waitPlatformRefusal is the build-tag-selected gate; this
// replaces it so the call site is observable on every lane, and
// waitmerge_windows_test.go still proves the real refusal's own text.
//
// MUTATION TARGET (e): deleting the waitPlatformRefusal() call from
// WaitOnGreen or MergeOnGreen turns this red.
func TestWaitOnGreen_PlatformGateRunsBeforeAnyPoll(t *testing.T) {
	refusal := cascade.New(cascade.KindUnsupported, "ci: this platform has no daemon (test gate)")
	restore := waitPlatformRefusal
	waitPlatformRefusal = func() error { return refusal }
	t.Cleanup(func() { waitPlatformRefusal = restore })

	doer := &fakeDoer{}
	clock := runtime.NewFixedClock(time.Unix(0, 0))
	deps := waitTestDeps(t, doer, clock, (&recordingSleeper{clock: clock}).sleep)
	_, err := WaitOnGreen(context.Background(), deps, WaitOptions{Owner: "acamarata", Repo: "cascade", Ref: "main"})
	if err == nil || err.Error() != refusal.Error() {
		t.Fatalf("WaitOnGreen err = %v, want the platform refusal %v", err, refusal)
	}
	if len(doer.calls) != 0 {
		t.Fatalf("doer.calls = %d, want 0 -- the gate must run before any poll", len(doer.calls))
	}

	f := newMergeFixture(t)
	_, mergeErr := MergeOnGreen(context.Background(), f.deps(), baseMergeOptions(), passedWait("acamarata", "cascade", "deadbeef"))
	if mergeErr == nil || mergeErr.Error() != refusal.Error() {
		t.Fatalf("MergeOnGreen err = %v, want the platform refusal %v", mergeErr, refusal)
	}
	if len(f.caller.calls) != 0 {
		t.Fatalf("caller.calls = %d, want 0", len(f.caller.calls))
	}
}

// TestWaitOnGreen_NeverPayRefusalAbortsBeforeAnyRequest proves the
// pre-flight: a repository the policy keeps local, and a missing policy,
// both stop the wait before a byte leaves -- and the missing-policy case
// aborts rather than being retried to the deadline (it is KindUnavailable,
// the same kind a 502 carries).
func TestWaitOnGreen_NeverPayRefusalAbortsBeforeAnyRequest(t *testing.T) {
	skipWhenWaitIsRefusedHere(t)
	cases := []struct {
		name   string
		routes RouteResolver
		kind   cascade.Kind
	}{
		{"no policy at all", nil, cascade.KindUnavailable},
		{"routed to the local gate", localOnlyRoutes, cascade.KindPolicyDenied},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doer := &fakeDoer{}
			clock := runtime.NewFixedClock(time.Unix(0, 0))
			sleeper := &recordingSleeper{clock: clock}
			deps := WaitDeps{
				Client: NewClient(doer, testEngine(t), "", 1, c.routes),
				Clock:  clock, Sleep: sleeper.sleep,
			}
			_, err := WaitOnGreen(context.Background(), deps, WaitOptions{Owner: "acamarata", Repo: "cascade", Ref: "main"})
			if !cascade.HasKind(err, c.kind) {
				t.Fatalf("err = %v, want %v", err, c.kind)
			}
			if len(doer.calls) != 0 || len(sleeper.delays) != 0 {
				t.Fatalf("doer.calls=%d sleeper.delays=%d, want 0/0", len(doer.calls), len(sleeper.delays))
			}
		})
	}
}

// TestWaitOnGreen_TransientPollErrorIsRetriedWithBackoff proves D7's first
// half and the review's finding 7 input: ONE api.github.com 502 must not
// end a 30-minute wait.
func TestWaitOnGreen_TransientPollErrorIsRetriedWithBackoff(t *testing.T) {
	skipWhenWaitIsRefusedHere(t)
	inner := &fakeDoer{responses: map[string]HTTPResponse{
		waitRunsURL:   {Status: 200, Body: waitRunsBody("completed", "success")},
		waitJobsURL(): {Status: 200, Body: waitJobsBody("build", "completed", "success")},
	}}
	doer := &flakyDoer{failures: 2, err: cascade.New(cascade.KindUnavailable, "502 Bad Gateway"), inner: inner}
	clock := runtime.NewFixedClock(time.Unix(0, 0))
	sleeper := &recordingSleeper{clock: clock}
	deps := WaitDeps{Client: NewClient(doer, testEngine(t), "", 1, allowActionsRoutes), Clock: clock, Sleep: sleeper.sleep}

	result, err := WaitOnGreen(context.Background(), deps, WaitOptions{
		Owner: "acamarata", Repo: "cascade", Ref: "main", Timeout: time.Hour, PollInterval: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("WaitOnGreen: %v -- a transient poll failure must be retried, not returned", err)
	}
	if !result.Passed {
		t.Fatal("result.Passed = false after the transport recovered")
	}
	want := []time.Duration{10 * time.Second, 20 * time.Second}
	if len(sleeper.delays) != len(want) || sleeper.delays[0] != want[0] || sleeper.delays[1] != want[1] {
		t.Fatalf("delays = %v, want exponential backoff %v", sleeper.delays, want)
	}
}

// TestWaitOnGreen_TerminalPollErrorAborts proves D7's second half: an error
// that asking again cannot fix ends the wait immediately.
func TestWaitOnGreen_TerminalPollErrorAborts(t *testing.T) {
	doer := &fakeDoer{responses: map[string]HTTPResponse{
		waitRunsURL: {Status: 200, Body: []byte(`{"total_count":1,"workflow_runs":[{`)},
	}}
	clock := runtime.NewFixedClock(time.Unix(0, 0))
	sleeper := &recordingSleeper{clock: clock}
	deps := WaitDeps{Client: NewClient(doer, testEngine(t), "", 1, allowActionsRoutes), Clock: clock, Sleep: sleeper.sleep}

	_, err := WaitOnGreen(context.Background(), deps, WaitOptions{
		Owner: "acamarata", Repo: "cascade", Ref: "main", Timeout: time.Hour, PollInterval: 10 * time.Second,
	})
	if err == nil {
		t.Fatal("err = nil, want the terminal decode refusal")
	}
	if cascade.HasKind(err, cascade.KindTimeout) {
		t.Fatalf("err = %v, want the terminal error itself rather than a retried timeout", err)
	}
	if len(sleeper.delays) != 0 {
		t.Fatalf("sleeper.delays = %v, want none -- a terminal error must not be retried", sleeper.delays)
	}
}

// TestWaitOnGreen_ExplicitRequiredChecksAreHonored drives the explicit mode
// end to end, so the loop's own use of an explicit required set (not only
// evaluateChecks in isolation) is proven.
//
// The run itself is completed/success here. It used to be in_progress, and
// this test passed: that was the confirming review's finding 2 -- the run
// gate applied only in auto mode, so an explicit required set resolved
// green on an unfinished run. evaluateChecks now applies the gate in every
// mode (see TestEvaluateChecks_ExplicitRequiredSetStillNeedsTheRunItself),
// so an in_progress run here would correctly run to the timeout.
func TestWaitOnGreen_ExplicitRequiredChecksAreHonored(t *testing.T) {
	skipWhenWaitIsRefusedHere(t)
	doer := &fakeDoer{responses: map[string]HTTPResponse{
		waitRunsURL:   {Status: 200, Body: waitRunsBody("completed", "success")},
		waitJobsURL(): {Status: 200, Body: waitJobsBody("build", "completed", "success")},
	}}
	clock := runtime.NewFixedClock(time.Unix(0, 0))
	sleeper := &recordingSleeper{clock: clock}
	deps := waitTestDeps(t, doer, clock, sleeper.sleep)

	result, err := WaitOnGreen(context.Background(), deps, WaitOptions{
		Owner: "acamarata", Repo: "cascade", Ref: "main", RequiredChecks: []string{"build"},
		Timeout: time.Minute, PollInterval: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("WaitOnGreen: %v", err)
	}
	if !result.Passed || result.HeadSHA != "deadbeef" {
		t.Fatalf("result = %+v, want Passed with the run's head SHA", result)
	}
}

// TestMergeOptions_DataClassFloorsAtInternal pins 06 §5.16's inheritance
// rule: unset reads as internal, and a higher tier is carried through.
func TestMergeOptions_DataClassFloorsAtInternal(t *testing.T) {
	if got := (MergeOptions{}).dataClass(); got != policy.DataClassInternal {
		t.Fatalf("unset DataClass = %s, want internal", got)
	}
	opts := MergeOptions{DataClass: policy.DataClassConfidential}
	if got := opts.dataClass(); got != policy.DataClassConfidential {
		t.Fatalf("DataClass = %s, want confidential carried through", got)
	}
}
