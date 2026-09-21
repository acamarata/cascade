package ci

// Purpose: P1-E25-W5-S51-T4's acceptance tests for the CI-failure-to-
// attention routing path (attention.go): AC1 (a matched watch's failure
// produces a fully-populated attention entry), AC2 (success / unwatched
// repo route nothing), AC3 (an unrecognised conclusion never panics),
// plus the exact deep-link/ScopeRef shape, the dedup identity and the
// wait-timeout-is-not-a-conclusion invariant this file's header records.
//
// SPORT: internal.ci (TEST) -- P1-E25-W5-S51-T4.

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fakePusher records every Push call for assertion, and can be told to
// fail once (proving RouteFailure propagates a real Push error rather
// than swallowing it). It implements AttentionLister too, over the same
// recorded slice, so the Existing/Acked probe is exercised on the fake
// path as well as against the real Store (attention_queue_test.go).
type fakePusher struct {
	pushed   []supervision.AttentionItem
	failWith error
	// notify, when non-nil, receives every accepted push. The bus
	// subscriber's tests use it as a DETERMINISTIC barrier (the bus
	// delivers in strict Seq order, so a later event's push proves every
	// earlier event was handled) rather than sleeping -- Art.11.
	notify chan supervision.AttentionItem
}

func (f *fakePusher) Push(_ context.Context, item supervision.AttentionItem) (supervision.AttentionItem, error) {
	if f.failWith != nil {
		return supervision.AttentionItem{}, f.failWith
	}
	for _, existing := range f.pushed {
		if existing.Kind == item.Kind && existing.SourceRef == item.SourceRef {
			return existing, nil
		}
	}
	item.ID = "pushed-1"
	f.pushed = append(f.pushed, item)
	if f.notify != nil {
		f.notify <- item
	}
	return item, nil
}

func (f *fakePusher) ListInScopes(
	_ context.Context, scopes []supervision.ScopeRef, _ supervision.Filter,
) ([]supervision.AttentionItem, error) {
	var out []supervision.AttentionItem
	for _, item := range f.pushed {
		for _, s := range scopes {
			if item.ScopeRef == s {
				out = append(out, item)
			}
		}
	}
	return out, nil
}

func matchedWatch() []runtime.CIWatchEntry {
	return []runtime.CIWatchEntry{{Repo: "acamarata/cascade", Branch: "main", Workflow: "build*"}}
}

func matchedOptions() RouteOptions {
	return RouteOptions{Watches: matchedWatch()}
}

func failingChecks() []CheckStatus {
	return []CheckStatus{
		{Name: "lint", Status: RunStatusCompleted, Conclusion: ConclusionSuccess},
		{Name: "build-amd64", Status: RunStatusCompleted, Conclusion: ConclusionFailure},
	}
}

func failingResult() WaitResult {
	return WaitResult{
		Owner: "acamarata", Repo: "cascade", Ref: "main", RunID: 42,
		Conclusion: ConclusionFailure, WorkflowName: "build",
		Checks: failingChecks(),
	}
}

func conflictErr(msg string) error {
	return cascade.New(cascade.KindConflict, msg)
}

// TestRouteWaitFailure_MatchedFailureRoutesToAttention is AC1: a failure
// conclusion for a watched repo+branch+workflow produces an attention
// entry carrying repo, ref, run_id, conclusion, failed_jobs, the GitHub
// Actions run URL, the EXACT deep-link format, and the EXACT ScopeRef.
func TestRouteWaitFailure_MatchedFailureRoutesToAttention(t *testing.T) {
	pusher := &fakePusher{}
	res, err := RouteWaitFailure(context.Background(), pusher, matchedOptions(), failingResult(), conflictErr("check failed"))
	if err != nil {
		t.Fatalf("RouteWaitFailure: unexpected error: %v", err)
	}
	if !res.Routed || res.Existing {
		t.Fatalf("RouteResult = %+v, want Routed=true Existing=false", res)
	}
	if res.Item.Kind != supervision.KindError {
		t.Errorf("Kind = %q, want %q", res.Item.Kind, supervision.KindError)
	}
	if len(pusher.pushed) != 1 {
		t.Fatalf("Push called %d times, want 1", len(pusher.pushed))
	}
	assertItemShape(t, res.Item)
	assertCandidateShape(t, res.Candidate)
}

// assertItemShape pins the two AttentionItem fields that govern dedup and
// visibility. Both were unasserted before, which let a mutation of either
// pass the whole suite (CR "MUTATIONS", uncaught).
func assertItemShape(t *testing.T, item supervision.AttentionItem) {
	t.Helper()
	if want := "ci:acamarata/cascade:42"; item.SourceRef != want {
		t.Errorf("SourceRef = %q, want the exact run identity %q", item.SourceRef, want)
	}
	want := supervision.ScopeRef{Kind: scope.ScopeKindGlobal}
	if item.ScopeRef != want {
		t.Errorf("ScopeRef = %+v, want %+v (a public repo is filed globally)", item.ScopeRef, want)
	}
}

// assertCandidateShape pins every AC1 field, including the deep link's
// EXACT format. internal/notify/notify.go documents DeepLink as an opaque
// cascade:// URI and docs/architecture.md:194 repeats it; this package's
// format is cascade://ci/<owner>/<repo>/runs/<run_id>, asserted verbatim
// so a change to it cannot pass silently.
func assertCandidateShape(t *testing.T, c AttentionCandidate) {
	t.Helper()
	if c.Repo != "acamarata/cascade" || c.Ref != "main" || c.RunID != 42 {
		t.Errorf("identity = %q/%q/%d, want acamarata/cascade/main/42", c.Repo, c.Ref, c.RunID)
	}
	if c.Conclusion != ConclusionFailure {
		t.Errorf("Conclusion = %q, want failure", c.Conclusion)
	}
	if c.Workflow != "build" {
		t.Errorf("Workflow = %q, want the workflow name build", c.Workflow)
	}
	if len(c.FailedJobs) != 1 || c.FailedJobs[0].Name != "build-amd64" || c.FailedJobs[0].Conclusion != ConclusionFailure {
		t.Errorf("FailedJobs = %+v, want exactly one build-amd64/failure entry", c.FailedJobs)
	}
	if want := "https://github.com/acamarata/cascade/actions/runs/42"; c.RunURL != want {
		t.Errorf("RunURL = %q, want %q", c.RunURL, want)
	}
	if want := "cascade://ci/acamarata/cascade/runs/42"; c.DeepLink != want {
		t.Errorf("DeepLink = %q, want the exact format %q", c.DeepLink, want)
	}
}

// TestRouteWaitFailure_SuccessNeverRoutes is half of AC2: WaitOnGreen
// returning success (nil error) never reaches the attention queue.
func TestRouteWaitFailure_SuccessNeverRoutes(t *testing.T) {
	pusher := &fakePusher{}
	result := failingResult()
	result.Passed = true
	result.Conclusion = ConclusionSuccess
	res, err := RouteWaitFailure(context.Background(), pusher, matchedOptions(), result, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Routed {
		t.Fatalf("routed=true on a nil (success) waitErr, want false")
	}
	if len(pusher.pushed) != 0 {
		t.Fatalf("Push called %d times on success, want 0", len(pusher.pushed))
	}
}

// TestRouteWaitFailure_UnwatchedRepoNeverRoutes is the other half of
// AC2: a real failure for a repo with no matching watch entry routes
// nothing.
func TestRouteWaitFailure_UnwatchedRepoNeverRoutes(t *testing.T) {
	pusher := &fakePusher{}
	opts := RouteOptions{Watches: []runtime.CIWatchEntry{{Repo: "someone-else/other-repo"}}}
	res, err := RouteWaitFailure(context.Background(), pusher, opts, failingResult(), conflictErr("x"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Routed {
		t.Fatalf("routed=true for an unwatched repo, want false")
	}
	if len(pusher.pushed) != 0 {
		t.Fatalf("Push called for an unwatched repo, want 0 calls")
	}
}

// TestRouteWaitFailure_UnknownConclusionNeverRoutes is AC3's fail-closed
// half: a run that concluded SUCCESS, or that has not concluded at all,
// never produces an entry and never panics, whatever its checks say.
// (Under the run-level decision an unrecognised conclusion now DOES
// route -- fail-closed toward telling the operator -- which
// TestCIFailureAttention's "unknown conclusion" row pins.)
func TestRouteWaitFailure_UnknownConclusionNeverRoutes(t *testing.T) {
	for _, conclusion := range []RunConclusion{ConclusionSuccess, ConclusionNone} {
		t.Run(string(conclusion), func(t *testing.T) {
			pusher := &fakePusher{}
			result := failingResult()
			result.Conclusion = conclusion
			result.Checks = []CheckStatus{{Name: "build-amd64", Status: RunStatusCompleted, Conclusion: ConclusionUnknown}}
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("RouteWaitFailure panicked on conclusion %q: %v", conclusion, r)
				}
			}()
			res, err := RouteWaitFailure(context.Background(), pusher, matchedOptions(), result, conflictErr("x"))
			if err != nil {
				t.Fatalf("unexpected error for conclusion %q: %v", conclusion, err)
			}
			if res.Routed {
				t.Fatalf("routed=true for non-routable conclusion %q, want false", conclusion)
			}
		})
	}
}

// TestRouteWaitFailure_WaitTimeoutIsNotACIConclusion proves the
// KindConflict discriminator: WaitOnGreen's own wait-loop timeout
// (KindTimeout) and context cancellation (KindCanceled) are NOT CI
// conclusions and must never route, even when Checks happens to carry a
// failing entry from the last observed poll.
func TestRouteWaitFailure_WaitTimeoutIsNotACIConclusion(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"timeout", cascade.New(cascade.KindTimeout, "wait-on-green timed out")},
		{"canceled", cascade.New(cascade.KindCanceled, "wait-on-green canceled")},
		{"unsupported", cascade.New(cascade.KindUnsupported, "windows tier-2")},
		{"plain error, no taxonomy", errors.New("boom")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pusher := &fakePusher{}
			res, err := RouteWaitFailure(context.Background(), pusher, matchedOptions(), failingResult(), tc.err)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Routed {
				t.Fatalf("routed=true for a non-KindConflict error (%v), want false", tc.err)
			}
		})
	}
}

// TestRouteWaitFailure_NilPusherAndEmptyWatchesNoOp covers the two
// upfront guards: a nil Pusher and an empty watch list both no-op rather
// than panicking or erroring.
func TestRouteWaitFailure_NilPusherAndEmptyWatchesNoOp(t *testing.T) {
	if res, err := RouteWaitFailure(context.Background(), nil, matchedOptions(), failingResult(), conflictErr("x")); err != nil || res.Routed {
		t.Fatalf("nil pusher: routed=%v err=%v, want false/nil", res.Routed, err)
	}
	pusher := &fakePusher{}
	if res, err := RouteWaitFailure(context.Background(), pusher, RouteOptions{}, failingResult(), conflictErr("x")); err != nil || res.Routed {
		t.Fatalf("empty watches: routed=%v err=%v, want false/nil", res.Routed, err)
	}
}

// TestRouteWaitFailure_PushErrorPropagates proves a genuine Push failure
// is surfaced, not swallowed.
func TestRouteWaitFailure_PushErrorPropagates(t *testing.T) {
	pusher := &fakePusher{failWith: cascade.New(cascade.KindUnavailable, "store closed")}
	res, err := RouteWaitFailure(context.Background(), pusher, matchedOptions(), failingResult(), conflictErr("x"))
	if err == nil {
		t.Fatalf("want a propagated Push error, got nil")
	}
	if res.Routed {
		t.Fatalf("routed=true alongside a Push error, want false")
	}
}
