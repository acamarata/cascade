package ci

// Purpose: P1-E25-W5-S51-T4's named acceptance target TestCIFailureAttention
// (the ticket's execution_guidance validation_plan names it, with a hard
// discovery guard), plus the two proofs the fake pusher cannot give: a
// push through a REAL supervision.Store over a real sqlite file (so
// AttentionItem.Validate actually runs on the constructed item, and the
// (Kind, SourceRef) dedup is the real one), and the [ci.policy.repos]
// private-repo data-class/scope rule enforced through
// supervision.RoutePush.
//
// Constraints: Art.7.1 -- every file lives under t.TempDir(); the sqlite
// handle is CLOSED by a defer, which runs before t.TempDir's own cleanup,
// so the windows lane can remove the directory.
//
// SPORT: internal.ci (TEST) -- P1-E25-W5-S51-T4.

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/runtime"
	sqlite "github.com/acamarata/cascade/providers/sqlite"
)

// TestCIFailureAttention is the ticket's named acceptance target: AC1
// (every routable conclusion for a watched tuple produces one entry),
// AC2 (success and unwatched route nothing) and AC3 (an unrecognised
// conclusion never panics) in one table.
func TestCIFailureAttention(t *testing.T) {
	cases := []struct {
		name       string
		conclusion RunConclusion
		opts       RouteOptions
		wantRouted bool
	}{
		{"AC1 failure routes", ConclusionFailure, matchedOptions(), true},
		{"AC1 cancelled routes", ConclusionCancelled, matchedOptions(), true},
		{"AC1 timed_out routes", ConclusionTimedOut, matchedOptions(), true},
		{"AC2 success never routes", ConclusionSuccess, matchedOptions(), false},
		{"AC2 unwatched repo never routes", ConclusionFailure,
			RouteOptions{Watches: []runtime.CIWatchEntry{{Repo: "other/repo"}}}, false},
		{"AC3 unknown conclusion routes fail-closed without panicking", ConclusionUnknown, matchedOptions(), true},
		{"AC3 an unconcluded run never routes", ConclusionNone, matchedOptions(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("routing panicked on conclusion %q: %v", tc.conclusion, r)
				}
			}()
			pusher := &fakePusher{}
			result := failingResult()
			result.Conclusion = tc.conclusion
			res, err := RouteWaitFailure(context.Background(), pusher, tc.opts, result, conflictErr("x"))
			if err != nil {
				t.Fatalf("RouteWaitFailure: %v", err)
			}
			if res.Routed != tc.wantRouted {
				t.Fatalf("Routed = %v, want %v", res.Routed, tc.wantRouted)
			}
			if got := len(pusher.pushed); (got == 1) != tc.wantRouted {
				t.Fatalf("Push called %d times, want %v", got, tc.wantRouted)
			}
			if tc.wantRouted && res.Candidate.Conclusion != tc.conclusion {
				t.Errorf("Conclusion = %q, want the run's own %q verbatim", res.Candidate.Conclusion, tc.conclusion)
			}
		})
	}
}

// newRealAttentionRouter builds a ci.Router over a REAL supervision.Store
// backed by a real sqlite file under t.TempDir(). The handle is closed by
// the caller's defer, before t.TempDir's cleanup removes the directory.
func newRealAttentionRouter(t *testing.T, private []string) (Router, func()) {
	t.Helper()
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "cascade.db"))
	if err != nil {
		t.Fatalf("opening the real sqlite store: %v", err)
	}
	clock := runtime.NewFixedClock(time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC))
	store := supervision.NewStore(db, clock, nil, supervision.NewSystemIDGenerator(), 0)
	return Router{Store: store, Class: NewCIDataClassChecker(private)}, func() { _ = db.Close() }
}

// TestCIFailureAttention_RealStoreDedupAndValidate is the proof the fake
// cannot give: the item is pushed through supervision.RoutePush into a
// real Store, so AttentionItem.Validate runs on it, and the SAME run
// observed twice -- the second time with an extra failed job -- collapses
// onto ONE item whose second RouteResult reports Existing, never Routed.
func TestCIFailureAttention_RealStoreDedupAndValidate(t *testing.T) {
	router, closeDB := newRealAttentionRouter(t, nil)
	defer closeDB()
	ctx := context.Background()

	first, err := RouteWaitFailure(ctx, router, matchedOptions(), failingResult(), conflictErr("x"))
	if err != nil {
		t.Fatalf("first RouteWaitFailure: %v", err)
	}
	if !first.Routed || first.Existing || first.Item.ID == "" {
		t.Fatalf("first RouteResult = %+v, want Routed=true Existing=false with a minted ID", first)
	}
	assertItemShape(t, first.Item)

	second := failingResult()
	second.Checks = append(second.Checks, CheckStatus{Name: "test", Status: RunStatusCompleted, Conclusion: ConclusionFailure})
	again, err := RouteWaitFailure(ctx, router, matchedOptions(), second, conflictErr("x"))
	if err != nil {
		t.Fatalf("second RouteWaitFailure: %v", err)
	}
	if again.Routed || !again.Existing || again.Acked {
		t.Fatalf("second RouteResult = %+v, want Routed=false Existing=true Acked=false", again)
	}
	if again.Item.ID != first.Item.ID {
		t.Fatalf("second observation produced item %q, want the first item %q", again.Item.ID, first.Item.ID)
	}
	assertQueueDepth(ctx, t, router, supervision.ScopeRef{Kind: scope.ScopeKindGlobal}, 1)

	other := failingResult()
	other.RunID = 43
	if _, err := RouteWaitFailure(ctx, router, matchedOptions(), other, conflictErr("x")); err != nil {
		t.Fatalf("third RouteWaitFailure: %v", err)
	}
	assertQueueDepth(ctx, t, router, supervision.ScopeRef{Kind: scope.ScopeKindGlobal}, 2)
}

// TestCIFailureAttention_AckedItemIsNeverReportedRouted pins CR finding
// 5's collapse input C: an acked item re-observed reports Existing+Acked,
// and the caller is never told a failure was routed when nothing queued.
func TestCIFailureAttention_AckedItemIsNeverReportedRouted(t *testing.T) {
	router, closeDB := newRealAttentionRouter(t, nil)
	defer closeDB()
	ctx := context.Background()

	first, err := RouteWaitFailure(ctx, router, matchedOptions(), failingResult(), conflictErr("x"))
	if err != nil {
		t.Fatalf("first RouteWaitFailure: %v", err)
	}
	if _, err := router.Store.Ack(ctx, first.Item.ID); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	again, err := RouteWaitFailure(ctx, router, matchedOptions(), failingResult(), conflictErr("x"))
	if err != nil {
		t.Fatalf("second RouteWaitFailure: %v", err)
	}
	if again.Routed || !again.Existing || !again.Acked {
		t.Fatalf("RouteResult = %+v, want Routed=false Existing=true Acked=true", again)
	}
}

// TestCIFailureAttention_PrivateRepoIsFiledAtProjectScope is D6/CR
// finding 6: a repository listed in [ci.policy.repos].private never lands
// in a GLOBAL-scope item, and the data-class check is what would refuse it
// if it tried.
func TestCIFailureAttention_PrivateRepoIsFiledAtProjectScope(t *testing.T) {
	router, closeDB := newRealAttentionRouter(t, []string{"acamarata/*"})
	defer closeDB()
	ctx := context.Background()

	opts := RouteOptions{Watches: matchedWatch(), PrivateRepos: []string{"acamarata/*"}}
	res, err := RouteWaitFailure(ctx, router, opts, failingResult(), conflictErr("x"))
	if err != nil {
		t.Fatalf("RouteWaitFailure: %v", err)
	}
	want := supervision.ScopeRef{Kind: scope.ScopeKindProject, ID: "acamarata/cascade"}
	if res.Item.ScopeRef != want {
		t.Fatalf("ScopeRef = %+v, want %+v", res.Item.ScopeRef, want)
	}
	assertQueueDepth(ctx, t, router, supervision.ScopeRef{Kind: scope.ScopeKindGlobal}, 0)
	assertQueueDepth(ctx, t, router, want, 1)
}

// TestCIFailureAttention_DataClassRefusalIsTheRoutingResult proves the
// refusal is surfaced, never swallowed: a globally-scoped item naming a
// private repo is refused by supervision.RoutePush's data-class check and
// the error reaches the caller.
func TestCIFailureAttention_DataClassRefusalIsTheRoutingResult(t *testing.T) {
	router, closeDB := newRealAttentionRouter(t, []string{"acamarata/*"})
	defer closeDB()

	_, err := router.Push(context.Background(), supervision.AttentionItem{
		Kind:      supervision.KindError,
		SourceRef: "ci:acamarata/cascade:42",
		ScopeRef:  supervision.ScopeRef{Kind: scope.ScopeKindGlobal},
	})
	if err == nil {
		t.Fatalf("a global-scope item naming a private repo was accepted; want a data-class refusal")
	}
}

// assertQueueDepth counts the items filed under one scope.
func assertQueueDepth(ctx context.Context, t *testing.T, router Router, ref supervision.ScopeRef, want int) {
	t.Helper()
	items, err := router.ListInScopes(ctx, []supervision.ScopeRef{ref}, supervision.Filter{IncludeAcked: true})
	if err != nil {
		t.Fatalf("ListInScopes(%+v): %v", ref, err)
	}
	if len(items) != want {
		t.Fatalf("queue depth at %+v = %d, want %d", ref, len(items), want)
	}
}
