// Purpose: proves poll.go's two never-pay guard call sites
// (guardActionsPoll inside PollRuns and PollJobs, P1-E25-W5-S51-T5) are
// still wired -- not just that guardActionsPoll itself refuses correctly,
// which route_test.go already covers in isolation. Without this file, both
// guard() calls in poll.go could be deleted and every test in this package
// would stay green, because poll_test.go/poll_pagination_test.go build
// every Client with allowActionsRoutes, the one resolver that always lets
// the call through.
//
// Constraints: never assert with errors.Is against a cascade sentinel --
// (*cascade.Error).Is compares Kind only, so it would pass for ANY error of
// the same kind and miss a guard that fired for the wrong reason
// (lesson_errors_is_compares_kind_only). Use cascade.HasKind for the kind
// check, plus a message/call-count assertion for the specific guard.
//
// SPORT: ci.poll-guard (TEST) -- P1-E25-W5-S51-T5.
package ci

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestPollRunsRefusesALocallyRoutedRepo proves PollRuns's guardActionsPoll
// call site is live: a repository the policy routes to the local gate must
// be refused with KindPolicyDenied naming the route, before the Doer is
// ever invoked.
func TestPollRunsRefusesALocallyRoutedRepo(t *testing.T) {
	doer := &fakeDoer{}
	c := NewClient(doer, testEngine(t), "https://api.github.com", 1, localOnlyRoutes)

	_, err := c.PollRuns(context.Background(), "acamarata", "secret")
	if err == nil {
		t.Fatal("expected a refusal to poll Actions for a locally-routed repo")
	}
	if !cascade.HasKind(err, cascade.KindPolicyDenied) {
		t.Errorf("KindOf(err) does not carry KindPolicyDenied: %v", err)
	}
	if !strings.Contains(err.Error(), string(RouteLocal)) {
		t.Errorf("error = %q, want it to name the %q route", err.Error(), RouteLocal)
	}
	if len(doer.calls) != 0 {
		t.Errorf("doer received %d requests, want 0 -- the guard must refuse before any byte leaves", len(doer.calls))
	}
}

// TestPollJobsRefusesALocallyRoutedRepo is PollRunsRefusesALocallyRoutedRepo's
// twin for PollJobs's own guardActionsPoll call site.
func TestPollJobsRefusesALocallyRoutedRepo(t *testing.T) {
	doer := &fakeDoer{}
	c := NewClient(doer, testEngine(t), "https://api.github.com", 1, localOnlyRoutes)

	_, err := c.PollJobs(context.Background(), "acamarata", "secret", 1)
	if err == nil {
		t.Fatal("expected a refusal to poll Actions jobs for a locally-routed repo")
	}
	if !cascade.HasKind(err, cascade.KindPolicyDenied) {
		t.Errorf("KindOf(err) does not carry KindPolicyDenied: %v", err)
	}
	if !strings.Contains(err.Error(), string(RouteLocal)) {
		t.Errorf("error = %q, want it to name the %q route", err.Error(), RouteLocal)
	}
	if len(doer.calls) != 0 {
		t.Errorf("doer received %d requests, want 0 -- the guard must refuse before any byte leaves", len(doer.calls))
	}
}

// TestPollRunsAndJobsFailClosedWithoutAResolver proves both call sites fail
// closed when the Client was built with a nil RouteResolver. NewClient does
// not itself reject a nil resolver (routes is an ordinary struct field, no
// constructor guard) -- the fail-closed behavior lives entirely in
// guardActionsPoll's own nil check, so this asserts KindUnavailable
// (errNoPolicy's kind) on both verbs, with zero requests reaching the Doer.
func TestPollRunsAndJobsFailClosedWithoutAResolver(t *testing.T) {
	doer := &fakeDoer{}
	c := NewClient(doer, testEngine(t), "https://api.github.com", 1, nil)

	if _, err := c.PollRuns(context.Background(), "acamarata", "cascade"); err == nil {
		t.Fatal("expected PollRuns to refuse with no route resolver installed")
	} else if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("PollRuns: KindOf(err) does not carry KindUnavailable: %v", err)
	}

	if _, err := c.PollJobs(context.Background(), "acamarata", "cascade", 1); err == nil {
		t.Fatal("expected PollJobs to refuse with no route resolver installed")
	} else if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("PollJobs: KindOf(err) does not carry KindUnavailable: %v", err)
	}

	if len(doer.calls) != 0 {
		t.Errorf("doer received %d requests, want 0 -- a nil resolver must refuse before any byte leaves", len(doer.calls))
	}
}
