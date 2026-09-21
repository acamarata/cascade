//go:build !windows

// Purpose: proves wireCIAttentionSubscription (daemon_unix_ci_attention.go)
// -- the exact composition helper wireBackgroundSubsystems calls to start
// internal/ci.RouteCIResults -- against a REAL *events.Bus and a REAL
// TempDir sqlite provider.Store (openMCPPolicyStore, the same store the
// production daemon and the CLI path both open): a published
// EventKindRunCompleted failure for a watched repo lands in the real
// supervision store, reachable through the exact ci.Router
// (newCIAttentionPusher) production wiring shares with the CLI path.
//
// Constraints: Art.7.1 -- the store lives under t.TempDir(); its handle is
// closed by a defer that runs before t.TempDir's own cleanup. Art.11 -- no
// sleep is used as synchronization: the barrier is the REAL
// fleet.attention.changed event supervision.Store.emit publishes on a
// successful push (internal/fleet/supervision/attention_store.go), read
// off its own subscription on the same bus.
//
// SPORT: cmd/cascade/daemon (TEST) -- P1-E25-W5-S51-T4.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/provider"
)

// wiringBarrierTimeout is the failsafe the barrier below fails the test
// after -- not a synchronization delay, matching
// internal/ci/attention_subscribe_test.go's own barrierTimeout rationale.
const wiringBarrierTimeout = 30 * time.Second

// ciResultsWireEvent mirrors internal/ci's unexported runCompletedPayload
// wire shape (runner.go): the JSON contract EventKindRunCompleted
// publishes. This package cannot import ci's unexported type -- only the
// bytes it puts on the bus, which is the real boundary a subscriber in a
// different package crosses.
type ciResultsWireEvent struct {
	RunID      int64  `json:"run_id"`
	Repo       string `json:"repo,omitempty"`
	Passed     bool   `json:"passed"`
	FailedStep string `json:"failed_step,omitempty"`
}

// wiringTestBus opens the real TempDir sqlite store openMCPPolicyStore
// (the production composition-root helper) opens, and the real *events.Bus
// sharing it -- the exact daemon_unix.go pairing (bus := events.New(store,
// deps.Clock)). The store's closer is the caller's to defer, before
// t.TempDir's own cleanup (Art.7.1).
func wiringTestBus(t *testing.T) (*events.Bus, provider.Store, runtime.Clock, func()) {
	t.Helper()
	paths := fakeDaemonPaths{root: t.TempDir()}
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC))
	store, closeStore, err := openMCPPolicyStore(paths, clock)
	if err != nil {
		t.Fatalf("openMCPPolicyStore: %v", err)
	}
	bus := events.New(store, clock)
	return bus, store, clock, func() {
		_ = bus.Close()
		closeStore()
	}
}

// TestWireCIAttentionSubscription_FailedRunForWatchedRepoLandsInStore is
// this ticket's wiring proof: wireCIAttentionSubscription, started over a
// real bus and a real store, turns one published ci_results failure for a
// watched repo into one item a real supervision.Store query finds.
func TestWireCIAttentionSubscription_FailedRunForWatchedRepoLandsInStore(t *testing.T) {
	bus, store, clock, cleanup := wiringTestBus(t)
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Subscribed BEFORE wireCIAttentionSubscription starts, so this
	// barrier cannot miss the push it is waiting on.
	barrier, err := bus.Subscribe(ctx, fleet.AttentionNamespace, "test-ci-attention-barrier", 4)
	if err != nil {
		t.Fatalf("Subscribe(%s): %v", fleet.AttentionNamespace, err)
	}

	watches := func(context.Context) (ci.RouteOptions, error) {
		return ci.RouteOptions{Watches: []runtime.CIWatchEntry{{Repo: "acamarata/cascade"}}}, nil
	}
	wireCIAttentionSubscription(ctx, watches, store, clock, bus, nil, slog.Default())

	raw, err := json.Marshal(ciResultsWireEvent{RunID: -55, Repo: "acamarata/cascade", Passed: false, FailedStep: "test"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := bus.Publish(ctx, ci.EventNamespace, ci.EventKindRunCompleted, "test-producer", raw); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	awaitAttentionBarrier(t, barrier)

	verify := daemon.NewAttentionStore(store, clock, nil)
	items, err := verify.ListInScopes(ctx, []supervision.ScopeRef{{Kind: scope.ScopeKindGlobal}}, supervision.Filter{IncludeAcked: true})
	if err != nil {
		t.Fatalf("ListInScopes: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("global-scope items = %d, want exactly 1", len(items))
	}
	if want := "ci:acamarata/cascade:-55"; items[0].SourceRef != want {
		t.Errorf("SourceRef = %q, want %q", items[0].SourceRef, want)
	}
}

// awaitAttentionBarrier blocks until the real fleet.attention.changed
// event supervision.Store.emit publishes on a successful push arrives, or
// fails the test -- never a sleep (Art.11).
func awaitAttentionBarrier(t *testing.T, barrier *events.Subscription) {
	t.Helper()
	select {
	case <-barrier.Events:
	case err := <-barrier.Errs:
		t.Fatalf("barrier subscription errored: %v", err)
	case <-time.After(wiringBarrierTimeout):
		t.Fatalf("no fleet.attention.changed event within %s: the wired subscriber never pushed", wiringBarrierTimeout)
	}
}

// TestWireCIAttentionSubscription_NilBusIsANoOp proves the guard: a nil
// bus (never true in production -- platformDaemonRun always constructs a
// real one -- but the defensive check earns its keep the same way every
// other wireX nil-guard in this composition root does) starts nothing and
// panics on nothing.
func TestWireCIAttentionSubscription_NilBusIsANoOp(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("wireCIAttentionSubscription(nil bus) panicked: %v", r)
		}
	}()
	watches := func(context.Context) (ci.RouteOptions, error) { return ci.RouteOptions{}, nil }
	wireCIAttentionSubscription(context.Background(), watches, nil, testkit.NewFrozenClock(time.Now()), nil, nil, slog.Default())
}

// TestCIWatchSourceFromReloader_ReadsHotReloaderCurrent proves the
// ci.WatchSource closure ciWatchSourceFromReloader returns actually reads
// hr.Current() at CALL time -- config_ci_watch.go's and
// attention_subscribe.go's own documented "the [ci.watch] key is hot"
// contract: an operator's config edit is visible to the NEXT event this
// subscriber handles, no restart required. wireCIAttentionSubscription's
// own wiring test above passes a hand-written watches func instead, so
// this closure's body was otherwise never invoked.
func TestCIWatchSourceFromReloader_ReadsHotReloaderCurrent(t *testing.T) {
	cfg := &runtime.Config{}
	cfg.CIWatch.Entries = []runtime.CIWatchEntry{{Repo: "acamarata/cascade", Branch: "main"}}
	cfg.CIPolicy.PrivateRepos = []string{"acamarata/*"}
	hr := runtime.NewHotReloader("", runtime.LoadOptions{}, cfg, runtime.NewSystemClock(), nil, nil, nil)

	source := ciWatchSourceFromReloader(hr)
	opts, err := source(context.Background())
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	if len(opts.Watches) != 1 || opts.Watches[0].Repo != "acamarata/cascade" || opts.Watches[0].Branch != "main" {
		t.Fatalf("Watches = %+v, want the reloader's current entry", opts.Watches)
	}
	if len(opts.PrivateRepos) != 1 || opts.PrivateRepos[0] != "acamarata/*" {
		t.Fatalf("PrivateRepos = %+v, want the reloader's current private-repo globs", opts.PrivateRepos)
	}
}
