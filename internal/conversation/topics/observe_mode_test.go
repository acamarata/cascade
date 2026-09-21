// Package topics (observe_mode_test.go): the 7-day gate's mode branching -
// the TestObserveMode table over every elapsed case, the two window-boundary
// cases asserted on their own, apply mode's error propagation, and Art.5's
// per-platform parity assertion. Helpers and doubles come from
// observe_doubles_test.go; see observe_log_test.go's header for the rest.
package topics

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/storetest"
)

// TestObserveMode is the mode-branching table (AC1/AC4/AC5): at each elapsed
// time it asserts which branch ran (ObserveResult.Observed), whether Route
// reached the ThreadStore at all (fakeThreadStore.threadSeq), and how many
// audit events were published.
func TestObserveMode(t *testing.T) {
	cases := []struct {
		name          string
		elapsed       time.Duration
		seed          bool // seed first_use_at, else let Observe claim it
		turns         []Turn
		wantObserved  bool
		wantProposals int
		wantThreads   int
		wantEvents    int
	}{
		{"first use claims the window and observes", 0, false, observeTurns(), true, 2, 0, 1},
		{"mid-window observes", 72 * time.Hour, true, observeTurns(), true, 2, 0, 1},
		{"one hour short of the window observes", observeWindow - time.Hour, true, observeTurns(), true, 2, 0, 1},
		{"exactly 7 days applies", observeWindow, true, observeTurns(), false, 0, 2, 0},
		{"past 7 days applies", observeWindow + time.Hour, true, observeTurns(), false, 0, 2, 0},
		{"empty window is neither mode", 72 * time.Hour, true, nil, false, 0, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clock, fts, pub := newFixedClock(), newFakeThreadStore(), &fakePublisher{}
			store := storetest.NewMemStore()
			if c.seed {
				seedFirstUseAt(t, store, clock.now.Add(-c.elapsed))
			}
			ol := mustObserveLogger(t, mustAutoThreader(t, codeBoundarySegmenter(), fts), store, clock, pub)
			got, err := ol.Observe(context.Background(), c.turns)
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			wantEq(t, got.Observed, c.wantObserved, "ObserveResult.Observed")
			wantEq(t, len(got.Proposals), c.wantProposals, "proposals")
			wantEq(t, len(got.ThreadIDs), c.wantThreads, "apply-mode thread ids")
			wantEq(t, fts.threadSeq, c.wantThreads, "threads created (0 proves Route never ran)")
			wantEq(t, len(pub.calls), c.wantEvents, "audit events published")
		})
	}
}

// TestObserveLoggerAppliesAtAndPastTheWindow: elapsed = 168h (exactly 7*24h)
// and 169h both call Route, return its ids, and emit no event.
func TestObserveLoggerAppliesAtAndPastTheWindow(t *testing.T) {
	for _, hours := range []int{168, 169} {
		t.Run(fmt.Sprintf("%dh", hours), func(t *testing.T) {
			clock, fts, pub := newFixedClock(), newFakeThreadStore(), &fakePublisher{}
			store := storetest.NewMemStore()
			seedFirstUseAt(t, store, clock.now.Add(-time.Duration(hours)*time.Hour))
			ol := mustObserveLogger(t, mustAutoThreader(t, codeBoundarySegmenter(), fts), store, clock, pub)
			got, err := ol.Observe(context.Background(), observeTurns())
			if err != nil {
				t.Fatalf("Observe (apply mode): %v", err)
			}
			wantEq(t, got.Observed, false, "ObserveResult.Observed in apply mode")
			wantEq(t, len(got.ThreadIDs), 2, "Observe (apply mode) thread id count")
			wantEq(t, len(got.Proposals), 0, "proposals in apply mode")
			wantEq(t, fts.threadSeq, 2, "threads Route created")
			wantEq(t, len(pub.calls), 0, "audit events published in apply mode")
			observing, err := ol.IsObserving()
			if err != nil {
				t.Fatalf("IsObserving: %v", err)
			}
			wantEq(t, observing, false, fmt.Sprintf("IsObserving() at elapsed %dh", hours))
		})
	}
}

func TestObserveLoggerApplyModeSegmenterErrorPropagates(t *testing.T) {
	clock := newFixedClock()
	wantErr := errors.New("segmenter down")
	store := storetest.NewMemStore()
	seedFirstUseAt(t, store, clock.now.Add(-8*24*time.Hour))
	ol := mustObserveLogger(t, mustAutoThreader(t, &fakeSegmenter{err: wantErr}, newFakeThreadStore()),
		store, clock, &fakePublisher{})
	if _, err := ol.Observe(context.Background(), observeTurns()); !errors.Is(err, wantErr) {
		t.Fatalf("Observe (apply mode) error = %v, want the injected segmenter error %v", err, wantErr)
	}
}

// TestObservePlatformParity is Art.5's per-platform assertion: pure Go, no
// CGO and no OS-specific path anywhere in observe_log.go or observe_state.go,
// driving the real pipeline through both modes, so the CI matrix running this
// same named test to a pass on macOS, Linux and Windows is the explicit
// per-platform result Art.5 requires.
func TestObservePlatformParity(t *testing.T) {
	t.Logf("observe-log platform parity: GOOS=%s GOARCH=%s", runtime.GOOS, runtime.GOARCH)
	clock, fts := newFixedClock(), newFakeThreadStore()
	ol := mustObserveLogger(t, mustAutoThreader(t, codeBoundarySegmenter(), fts),
		storetest.NewMemStore(), clock, &fakePublisher{})
	observed, err := ol.Observe(context.Background(), observeTurns())
	if err != nil {
		t.Fatalf("Observe (observe mode): %v", err)
	}
	observing, err := ol.IsObserving()
	if err != nil {
		t.Fatalf("IsObserving: %v", err)
	}
	if !observed.Observed || !observing || fts.threadSeq != 0 {
		t.Fatalf("platform parity on %s/%s: observe mode did not gate (Observed=%v, IsObserving=%v, threadSeq=%d)",
			runtime.GOOS, runtime.GOARCH, observed.Observed, observing, fts.threadSeq)
	}
	clock.now = clock.now.Add(8 * 24 * time.Hour)
	applied, err := ol.Observe(context.Background(), observeTurns())
	if err != nil {
		t.Fatalf("Observe (apply mode): %v", err)
	}
	wantEq(t, len(applied.ThreadIDs), 2,
		fmt.Sprintf("platform parity on %s/%s: apply mode thread id count", runtime.GOOS, runtime.GOARCH))
}
