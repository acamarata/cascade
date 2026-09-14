package claude

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// Purpose (this file): the session watch's two contract-named error paths —
//   a daemon that is not reachable, and a session that vanishes mid-watch —
//   plus the transition rules the emitted events must follow.
// SPORT: plugins/claude tests (ADD) — P1-E16-W4-S34-T1.

// recorder collects emitted lifecycle events.
type recorder struct {
	mu     sync.Mutex
	events []LifecycleEvent
}

func (r *recorder) emit(_ context.Context, ev LifecycleEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
	return nil
}

func (r *recorder) snapshot() []LifecycleEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]LifecycleEvent(nil), r.events...)
}

// transitions renders the recorded events as "id:transition" strings, with
// a "!" marker on a synthesized one, so a test asserts the whole sequence
// at once rather than indexing into it.
func (r *recorder) transitions() []string {
	out := []string{}
	for _, ev := range r.snapshot() {
		s := ev.Session.ID + ":" + string(ev.Transition)
		if ev.Synthesized {
			s += "!"
		}
		out = append(out, s)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// runWatch runs w until it returns, and fails if it does not.
func runWatch(ctx context.Context, t *testing.T, w *Watcher) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context ended")
	}
}

// TestWatchRetriesWhileTheDaemonIsUnreachable is the first contract-named
// error path. A daemon that is not up yet is the NORMAL state during boot,
// not a reason to abandon the watch — so a subscribe failure must be
// retried on the documented backoff, and the watch must still work once the
// daemon appears.
func TestWatchRetriesWhileTheDaemonIsUnreachable(t *testing.T) {
	var (
		mu       sync.Mutex
		attempts int
		waited   []time.Duration
	)
	rec := &recorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := &Watcher{
		Emit: rec.emit,
		Sleep: func(_ context.Context, d time.Duration) {
			mu.Lock()
			waited = append(waited, d)
			mu.Unlock()
		},
		Subscribe: func(context.Context) (<-chan SessionEvent, func(), error) {
			mu.Lock()
			attempts++
			n := attempts
			mu.Unlock()
			if n <= 3 {
				return nil, nil, errors.New("dial unix: no such file or directory")
			}
			events := make(chan SessionEvent, 1)
			events <- SessionEvent{ID: "s1", State: SessionRunning, Harness: HarnessName}
			close(events)
			return events, func() { cancel() }, nil
		},
	}

	runWatch(ctx, t, w)

	mu.Lock()
	gotWaits := append([]time.Duration(nil), waited...)
	mu.Unlock()

	if len(gotWaits) != 3 {
		t.Fatalf("waited %v, want one wait per failed attempt (3)", gotWaits)
	}
	for i, d := range gotWaits {
		if d != Backoff[i] {
			t.Errorf("wait %d = %v, want the documented step %v", i, d, Backoff[i])
		}
	}
	if got := rec.transitions(); !equalStrings(got, []string{"s1:session_started"}) {
		t.Errorf("transitions = %v, want the session seen once the daemon came up", got)
	}
}

// TestWatchGivesUpWhenTheContextEndsDuringBackoff proves the retry loop is
// bounded by the caller's context and not by a count — a watch that kept
// retrying after shutdown would keep the daemon alive.
func TestWatchGivesUpWhenTheContextEndsDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	rec := &recorder{}
	w := &Watcher{
		Emit:      rec.emit,
		Sleep:     func(context.Context, time.Duration) { cancel() },
		Subscribe: func(context.Context) (<-chan SessionEvent, func(), error) { return nil, nil, errors.New("down") },
	}
	runWatch(ctx, t, w)
	if got := rec.transitions(); len(got) != 0 {
		t.Errorf("transitions = %v, want none", got)
	}
}

// TestWatchSynthesizesAStopForAVanishedSession is the second
// contract-named error path, and the one that matters most.
//
// A process that vanishes never sends a stop of its own. Without the
// synthesized one every downstream consumer keeps believing a dead session
// is still running, which is worse than never having seen it start.
func TestWatchSynthesizesAStopForAVanishedSession(t *testing.T) {
	rec := &recorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first := true
	w := &Watcher{
		Emit:  rec.emit,
		Sleep: func(context.Context, time.Duration) {},
		Subscribe: func(context.Context) (<-chan SessionEvent, func(), error) {
			if !first {
				cancel()
				return nil, nil, errors.New("still gone")
			}
			first = false
			events := make(chan SessionEvent, 2)
			events <- SessionEvent{ID: "alive", State: SessionRunning, Harness: HarnessName}
			events <- SessionEvent{ID: "vanisher", State: SessionRunning, Harness: HarnessName}
			// The stream ends without either session ever reporting a
			// stop: the daemon went away mid-watch.
			close(events)
			return events, func() {}, nil
		},
	}

	runWatch(ctx, t, w)

	want := []string{
		"alive:session_started",
		"vanisher:session_started",
		"alive:session_stopped!",
		"vanisher:session_stopped!",
	}
	if got := rec.transitions(); !equalStrings(got, want) {
		t.Fatalf("transitions = %v, want %v", got, want)
	}
}
