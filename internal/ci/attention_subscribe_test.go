package ci

// Purpose: RouteCIResults against a REAL internal/events.Bus -- a real
// Publish into the real "ci_results" namespace with runner.go's real
// EventKindRunCompleted payload, and a real Subscription, so the
// subscriber is proven end to end rather than by calling its helper
// directly. Covers: a failed run for a watched repo pushes one item; a
// passing run, an unwatched repo and an identity-less run push nothing; a
// malformed payload and an unrelated Kind are dropped without stopping the
// loop; a canceled context returns ctx.Err().
//
// Constraints: Art.11 -- no sleep is used as synchronization. Every wait
// is on a channel the production code writes to, and the bus's strict
// Seq-ordered delivery makes a LATER event's push a sound barrier for
// every earlier event having been handled. The time.After arms in the
// barrier are failsafes that FAIL the test, never a timing assumption it
// depends on.
//
// SPORT: internal.ci (TEST) -- P1-E25-W5-S51-T4.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// barrierTimeout is the failsafe a barrier fails the test after. It is not
// a synchronization delay: a barrier that reaches it is a real hang.
const barrierTimeout = 30 * time.Second

// tripwireRunID is the run id of the final event every "nothing must be
// pushed" case publishes: its push is the barrier proving the events
// BEFORE it were handled (the bus delivers in strict Seq order).
const tripwireRunID = -999

// newCIResultsBus returns a real Bus over an in-memory provider.Store and
// a subscription on the real ci_results namespace.
func newCIResultsBus(t *testing.T) (*events.Bus, *events.Subscription) {
	t.Helper()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	bus := events.New(storetest.NewMemStore(), clock)
	sub, err := bus.Subscribe(context.Background(), EventNamespace, "ci-attention-test", 8)
	if err != nil {
		t.Fatalf("Subscribe(%s): %v", EventNamespace, err)
	}
	return bus, sub
}

// publishRunCompleted publishes one real EventKindRunCompleted event with
// runner.go's own payload shape.
func publishRunCompleted(t *testing.T, bus *events.Bus, payload runCompletedPayload) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshalling the payload: %v", err)
	}
	if _, err := bus.Publish(context.Background(), EventNamespace, EventKindRunCompleted, "ci-runner-local", raw); err != nil {
		t.Fatalf("Publish: %v", err)
	}
}

// runSubscriber starts RouteCIResults, waits on barrier, then closes the
// bus (which closes the subscription's Events channel, the loop's own
// documented stop condition) and returns the loop's error.
func runSubscriber(
	t *testing.T, bus *events.Bus, sub *events.Subscription,
	watches WatchSource, pusher AttentionPusher, barrier func(),
) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- RouteCIResults(context.Background(), sub, watches, pusher) }()
	barrier()
	if err := bus.Close(); err != nil {
		t.Fatalf("closing the bus: %v", err)
	}
	return <-done
}

// awaitPush blocks until one push arrives, failing the test if none does.
func awaitPush(t *testing.T, pusher *fakePusher) {
	t.Helper()
	select {
	case <-pusher.notify:
	case <-time.After(barrierTimeout):
		t.Fatalf("no attention push arrived within %s: the subscriber never handled the event", barrierTimeout)
	}
}

func staticWatches(opts RouteOptions) WatchSource {
	return func(context.Context) (RouteOptions, error) { return opts, nil }
}

func watchedOnly() RouteOptions {
	return RouteOptions{Watches: []runtime.CIWatchEntry{{Repo: "acamarata/cascade"}}}
}

// TestRouteCIResults_FailedRunForAWatchedRepoRoutes is the subscriber's
// AC1: a real completed-failure event on the real ci_results bus produces
// exactly one attention item carrying the run's identity, and the local
// run's "local" workflow name is what a workflow glob sees.
func TestRouteCIResults_FailedRunForAWatchedRepoRoutes(t *testing.T) {
	bus, sub := newCIResultsBus(t)
	pusher := &fakePusher{notify: make(chan supervision.AttentionItem, 4)}
	publishRunCompleted(t, bus, runCompletedPayload{
		RunID: -7, RepoID: 2001, Repo: "acamarata/cascade", Passed: false, FailedStep: StepTest,
	})
	err := runSubscriber(t, bus, sub, staticWatches(watchedOnly()), pusher, func() { awaitPush(t, pusher) })
	if err != nil {
		t.Fatalf("RouteCIResults: %v", err)
	}
	if len(pusher.pushed) != 1 {
		t.Fatalf("pushed %d items, want exactly 1", len(pusher.pushed))
	}
	if want := "ci:acamarata/cascade:-7"; pusher.pushed[0].SourceRef != want {
		t.Errorf("SourceRef = %q, want %q", pusher.pushed[0].SourceRef, want)
	}
}

// TestRouteCIResults_LocalRunCandidateShape pins what a LOCAL run's
// candidate carries: the "local" workflow name, no ref, no Actions URL
// (inventing one would link to a run that does not exist) and the exact
// deep link.
func TestRouteCIResults_LocalRunCandidateShape(t *testing.T) {
	c, ok := candidateFromRunCompleted(runCompletedPayload{
		RunID: -7, Repo: "acamarata/cascade", FailedStep: StepTest,
	})
	if !ok {
		t.Fatalf("candidateFromRunCompleted: ok=false, want true")
	}
	if c.Workflow != LocalRunWorkflowName || c.Ref != "" || c.RunURL != "" {
		t.Errorf("workflow/ref/url = %q/%q/%q, want %q/\"\"/\"\"", c.Workflow, c.Ref, c.RunURL, LocalRunWorkflowName)
	}
	if want := "cascade://ci/acamarata/cascade/runs/-7"; c.DeepLink != want {
		t.Errorf("DeepLink = %q, want %q", c.DeepLink, want)
	}
	if len(c.FailedJobs) != 1 || c.FailedJobs[0].Name != string(StepTest) {
		t.Errorf("FailedJobs = %+v, want the failed step named once", c.FailedJobs)
	}
	if c.Conclusion != ConclusionFailure {
		t.Errorf("Conclusion = %q, want failure", c.Conclusion)
	}
}

// TestRouteCIResults_SuccessAndUnwatchedPushNothing is the subscriber's
// AC2, plus the two identity-less payloads that must be dropped. Each case
// publishes its subject followed by a TRIPWIRE failure that does route:
// when the tripwire's push arrives, the subject was already handled, so
// "exactly one push, and it is the tripwire" is a sound assertion.
func TestRouteCIResults_SuccessAndUnwatchedPushNothing(t *testing.T) {
	cases := []struct {
		name    string
		payload runCompletedPayload
	}{
		{"a passing run", runCompletedPayload{RunID: -1, Repo: "acamarata/cascade", Passed: true}},
		{"an unwatched repo", runCompletedPayload{RunID: -2, Repo: "other/repo"}},
		{"a run with no repository identity", runCompletedPayload{RunID: -3}},
		{"a run with no id", runCompletedPayload{Repo: "acamarata/cascade"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bus, sub := newCIResultsBus(t)
			pusher := &fakePusher{notify: make(chan supervision.AttentionItem, 4)}
			publishRunCompleted(t, bus, tc.payload)
			publishRunCompleted(t, bus, runCompletedPayload{
				RunID: tripwireRunID, Repo: "acamarata/cascade", FailedStep: StepLint,
			})
			err := runSubscriber(t, bus, sub, staticWatches(watchedOnly()), pusher, func() { awaitPush(t, pusher) })
			if err != nil {
				t.Fatalf("RouteCIResults: %v", err)
			}
			if len(pusher.pushed) != 1 {
				t.Fatalf("pushed %d items, want only the tripwire", len(pusher.pushed))
			}
			if want := "ci:acamarata/cascade:-999"; pusher.pushed[0].SourceRef != want {
				t.Fatalf("the one push was %q, want the tripwire %q -- the subject routed when it must not",
					pusher.pushed[0].SourceRef, want)
			}
		})
	}
}

// TestRouteCIResults_MalformedAndUnrelatedEventsAreDropped proves one bad
// event never silences the ones after it: a garbage payload and an
// unrelated Kind are dropped, and the good event that follows still
// routes.
func TestRouteCIResults_MalformedAndUnrelatedEventsAreDropped(t *testing.T) {
	bus, sub := newCIResultsBus(t)
	pusher := &fakePusher{notify: make(chan supervision.AttentionItem, 4)}
	ctx := context.Background()
	if _, err := bus.Publish(ctx, EventNamespace, EventKindRunCompleted, "t", []byte("{not json")); err != nil {
		t.Fatalf("Publish (malformed): %v", err)
	}
	if _, err := bus.Publish(ctx, EventNamespace, events.EventKindPluginRegistered, "t", []byte(`{}`)); err != nil {
		t.Fatalf("Publish (unrelated kind): %v", err)
	}
	publishRunCompleted(t, bus, runCompletedPayload{RunID: -9, Repo: "acamarata/cascade", FailedStep: StepBuild})

	err := runSubscriber(t, bus, sub, staticWatches(watchedOnly()), pusher, func() { awaitPush(t, pusher) })
	if err != nil {
		t.Fatalf("RouteCIResults: %v", err)
	}
	if len(pusher.pushed) != 1 {
		t.Fatalf("pushed %d items, want exactly 1 (the good event after the two dropped ones)", len(pusher.pushed))
	}
}

// TestRouteCIResults_CanceledContextReturnsCtxErr covers the loop's
// cancellation leg, and the nil-subscription guard.
func TestRouteCIResults_CanceledContextReturnsCtxErr(t *testing.T) {
	bus, sub := newCIResultsBus(t)
	t.Cleanup(func() { _ = bus.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := RouteCIResults(ctx, sub, nil, &fakePusher{}); err == nil {
		t.Fatalf("want ctx.Err() from a canceled context, got nil")
	}
	if err := RouteCIResults(context.Background(), nil, nil, &fakePusher{}); err != nil {
		t.Fatalf("nil subscription: %v, want nil", err)
	}
}

// TestRouteCIResults_WatchSourceFailureDropsTheEvent proves a failing
// config read drops the event rather than pushing an unmatched item or
// killing the loop. The barrier is the source's own call, not a sleep.
func TestRouteCIResults_WatchSourceFailureDropsTheEvent(t *testing.T) {
	bus, sub := newCIResultsBus(t)
	pusher := &fakePusher{}
	publishRunCompleted(t, bus, runCompletedPayload{RunID: -4, Repo: "acamarata/cascade", FailedStep: StepLint})
	called := make(chan struct{}, 1)
	failing := func(context.Context) (RouteOptions, error) {
		called <- struct{}{}
		return RouteOptions{}, cascade.New(cascade.KindUnavailable, "config unreadable")
	}
	barrier := func() {
		select {
		case <-called:
		case <-time.After(barrierTimeout):
			t.Fatalf("the watch source was never consulted within %s", barrierTimeout)
		}
	}
	if err := runSubscriber(t, bus, sub, failing, pusher, barrier); err != nil {
		t.Fatalf("RouteCIResults: %v", err)
	}
	if len(pusher.pushed) != 0 {
		t.Fatalf("pushed %d items despite an unreadable config, want 0", len(pusher.pushed))
	}
}
