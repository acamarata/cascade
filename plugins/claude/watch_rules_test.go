package claude

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Purpose (this file): the watch's transition RULES — what an observation
//   means, whose sessions it speaks for, and the shutdown teardown. Split
//   from watch_test.go, which keeps the two contract-named error paths, to
//   stay under Art.10.3's 300-line cap (it counts tests).
// SPORT: plugins/claude tests (ADD) — P1-E16-W4-S34-T1.

// TestWatchEmitsOneStartPerSession proves repeat observations of the same
// live session do not become a stream of duplicate lifecycle events. The
// session stream re-reports state on every refresh, so without this the
// bus would carry one "started" per refresh for the life of the session.
func TestWatchEmitsOneStartPerSession(t *testing.T) {
	rec := &recorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := &Watcher{
		Emit:  rec.emit,
		Sleep: func(context.Context, time.Duration) {},
		Subscribe: func(context.Context) (<-chan SessionEvent, func(), error) {
			events := make(chan SessionEvent, 8)
			for i := 0; i < 3; i++ {
				events <- SessionEvent{ID: "s1", State: SessionRunning, Harness: HarnessName}
			}
			events <- SessionEvent{ID: "s1", State: SessionIdle, Harness: HarnessName}
			events <- SessionEvent{ID: "s1", State: SessionIdle, Harness: HarnessName}
			events <- SessionEvent{ID: "s1", State: SessionStopped, Harness: HarnessName}
			close(events)
			return events, func() { cancel() }, nil
		},
	}

	runWatch(ctx, t, w)

	want := []string{"s1:session_started", "s1:session_idle", "s1:session_stopped"}
	if got := rec.transitions(); !equalStrings(got, want) {
		t.Fatalf("transitions = %v, want %v", got, want)
	}
}

// TestWatchIgnoresOtherHarnesses proves this plugin does not speak for
// processes it does not manage. The stream carries every agent's sessions.
func TestWatchIgnoresOtherHarnesses(t *testing.T) {
	rec := &recorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := &Watcher{
		Emit:  rec.emit,
		Sleep: func(context.Context, time.Duration) {},
		Subscribe: func(context.Context) (<-chan SessionEvent, func(), error) {
			events := make(chan SessionEvent, 3)
			events <- SessionEvent{ID: "other", State: SessionRunning, Harness: "some-other-agent"}
			events <- SessionEvent{ID: "", State: SessionRunning, Harness: HarnessName}
			events <- SessionEvent{ID: "mine", State: SessionRunning, Harness: HarnessName}
			close(events)
			return events, func() { cancel() }, nil
		},
	}

	runWatch(ctx, t, w)

	if got := rec.transitions(); !equalStrings(got, []string{"mine:session_started"}) {
		t.Fatalf("transitions = %v, want only this harness's session", got)
	}
}

// TestWatchRefusesToRunUnwired proves a half-built watcher fails loudly at
// its entry point rather than silently observing nothing forever.
func TestWatchRefusesToRunUnwired(t *testing.T) {
	if err := (&Watcher{}).Run(context.Background()); err == nil {
		t.Fatal("an unwired Watcher ran")
	}
	if err := (&Watcher{Subscribe: func(context.Context) (<-chan SessionEvent, func(), error) {
		return nil, nil, nil
	}}).Run(context.Background()); err == nil {
		t.Fatal("a Watcher with no emitter ran")
	}
}

// TestWatchSynthesizesStopsOnShutdown covers the other teardown trigger:
// the daemon shutting down while sessions are still live. The stops must
// still be emitted — a consumer reading the bus after a restart would
// otherwise see sessions that started and never ended.
func TestWatchSynthesizesStopsOnShutdown(t *testing.T) {
	rec := &recorder{}
	ctx, cancel := context.WithCancel(context.Background())
	released := make(chan struct{})
	observed := make(chan struct{}, 1)

	w := &Watcher{
		Emit: func(ctx context.Context, ev LifecycleEvent) error {
			err := rec.emit(ctx, ev)
			select {
			case observed <- struct{}{}:
			default:
			}
			return err
		},
		Sleep: func(context.Context, time.Duration) {},
		Subscribe: func(context.Context) (<-chan SessionEvent, func(), error) {
			// A stream that stays OPEN: the only thing that ends this
			// watch is the context, which is the shutdown path.
			events := make(chan SessionEvent, 1)
			events <- SessionEvent{ID: "live", State: SessionRunning, Harness: HarnessName}
			return events, func() { close(released) }, nil
		},
	}

	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	// Shut down only once the session has actually been observed. The
	// handshake is the emit itself rather than a poll: spinning on a
	// snapshot would race the observation and burn a core doing it.
	select {
	case <-observed:
	case <-time.After(5 * time.Second):
		t.Fatal("the session was never observed")
	}
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after shutdown")
	}
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream was never released")
	}

	want := []string{"live:session_started", "live:session_stopped!"}
	if got := rec.transitions(); !equalStrings(got, want) {
		t.Fatalf("transitions = %v, want %v", got, want)
	}
}

// TestWatchUsesARealTimerWhenNoSleepIsInjected covers the production
// backoff path, whose whole job is to not spin. The first step is short by
// design, so this costs a fraction of a second rather than being skipped.
func TestWatchUsesARealTimerWhenNoSleepIsInjected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := &recorder{}
	attempts := 0

	w := &Watcher{
		Emit: rec.emit,
		// Sleep deliberately nil: exercise the real time.Timer path.
		Subscribe: func(context.Context) (<-chan SessionEvent, func(), error) {
			attempts++
			if attempts >= 2 {
				cancel()
			}
			return nil, nil, errors.New("down")
		},
	}

	start := time.Now()
	runWatch(ctx, t, w)
	if elapsed := time.Since(start); elapsed < Backoff[0] {
		t.Errorf("returned after %v, want at least one real backoff step (%v)", elapsed, Backoff[0])
	}
}
