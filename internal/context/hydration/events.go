package hydration

// Purpose: the degraded-hydration signal (P1-E16-W4-S34-T4, R-16.6a) —
//   one namespace, one kind, one writer and one reader, over the existing
//   persistent event bus.
// Inputs: a provider.Store (the same cascade.db the daemon uses) for the
//   write; the same for the read.
// Outputs: a published event, or a count over a window.
// Constraints: no new persistence interface (R-16.6a). This is
//   internal/events' Bus and nothing else. The write is best-effort by
//   design: hydration degrading is worth knowing about, and failing to
//   RECORD that it degraded is not worth a second failure on top.
// SPORT: internal/context/hydration (ADD) — P1-E16-W4-S34-T4.

import (
	"context"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

// DegradedNamespace and DegradedKind name the event. They are constants
// because the hook writes them and the doctor check reads them, and a
// literal typed twice is a signal that half exists.
const (
	// DegradedNamespace is the event log this signal is written to.
	DegradedNamespace = "context.hydration"
	// DegradedKind is the event kind. Namespaced so a reader scanning a
	// shared log cannot confuse it with another subsystem's "degraded".
	DegradedKind events.EventKind = "context.hydration.degraded"
	// DegradedSource names the writer, per the bus's own source field.
	DegradedSource = "cascade-context-slice-hook"
)

// DegradedWindow is the trailing window the doctor check counts over.
const DegradedWindow = 24 * time.Hour

// PublishDegraded writes one degraded event, best-effort.
//
// A nil store is a no-op rather than a panic: the hook reaches this
// function on paths where the store could not be opened in the first
// place, and the whole point of those paths is that they do not fail
// loudly.
func PublishDegraded(ctx context.Context, store provider.Store, payload []byte) {
	if store == nil {
		return
	}
	bus := events.New(store, runtime.SystemClock{})
	_, _ = bus.Publish(ctx, DegradedNamespace, DegradedKind, DegradedSource, payload)
}

// CountDegraded returns how many degraded events were published within
// window of now.
//
// It reads the whole namespace rather than seeking: this log holds one
// entry per degraded hydration, which is a rare event by construction —
// a log large enough for the scan to matter is itself the finding, and
// the check that reads it will already be reporting FAIL.
func CountDegraded(ctx context.Context, store provider.Store, now time.Time, window time.Duration) (int, error) {
	if store == nil {
		return 0, nil
	}
	bus := events.New(store, runtime.SystemClock{})
	all, err := bus.Replay(ctx, DegradedNamespace, 0)
	if err != nil {
		return 0, err
	}
	return countInWindow(all, now, window), nil
}

// Replayer reads one namespace's event log from an offset. Its one method
// is transcribed from internal/events' Bus.Replay, pinned below, so this
// package can be handed the daemon's own live bus rather than opening a
// second handle to a database the daemon already holds exclusively.
type Replayer interface {
	Replay(ctx context.Context, namespace string, offset uint64) ([]events.Event, error)
}

var _ Replayer = (*events.Bus)(nil)

// CountEvents counts degraded events inside window, over an already-open
// bus.
//
// CountDegraded above opens its own bus over a store; this takes one that
// is already open. The distinction is not stylistic: the store driver
// takes an exclusive lock, so the process that already holds the database
// is the only one that can answer while it is running.
func CountEvents(ctx context.Context, bus Replayer, now time.Time, window time.Duration) (int, error) {
	if bus == nil {
		return 0, nil
	}
	all, err := bus.Replay(ctx, DegradedNamespace, 0)
	if err != nil {
		return 0, err
	}
	return countInWindow(all, now, window), nil
}

// countInWindow is the shared tail of both counters.
func countInWindow(all []events.Event, now time.Time, window time.Duration) int {
	cutoff := now.Add(-window)
	count := 0
	for _, e := range all {
		if e.Kind == DegradedKind && e.Timestamp.After(cutoff) {
			count++
		}
	}
	return count
}
