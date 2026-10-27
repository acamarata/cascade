package governor

// Purpose: Subscribe's test suite: every transition delivered to every
// subscriber, cancel() stopping delivery, ctx cancellation doing the
// same automatically, and no goroutine leak across either path. Driven
// by tick() calls against a FixedClock; the only waiting is the bounded
// poll that lets a cancelled watcher goroutine finish exiting.

import (
	"bytes"
	"context"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	rt "github.com/acamarata/cascade/internal/runtime"
)

// watcherFrame is the stack-frame prefix of the goroutine Subscribe
// starts (and of the cancel closure it runs on ctx.Done()). A goroutine
// leak in Subscribe is by definition a goroutine whose stack still
// contains this frame, so counting exactly those goroutines measures the
// property under test and nothing else.
const watcherFrame = "(*ThrottleLadder).Subscribe.func"

// subscribeWatchers returns how many goroutines currently have a
// Subscribe watcher frame on their stack. Unlike runtime.NumGoroutine it
// is immune to unrelated goroutines: the race detector's helpers, the
// runtime's GC and finalizer workers, the test framework's own timers
// and goroutines still winding down from a previous test all come and go
// independent of this package, and counting them is what flaked the
// hosted -race job (count 3 against a baseline of 2 with no leak).
func subscribeWatchers() int {
	var buf bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&buf, 2); err != nil {
		panic(err) // a bytes.Buffer cannot fail; a profile error is a harness bug
	}
	return strings.Count(buf.String(), watcherFrame)
}

// waitSubscribeWatchers polls subscribeWatchers until done(n) holds or a
// generous wall-clock bound elapses, returning the last observed count.
// The watcher exits asynchronously relative to cancel(), and on a loaded
// single-core runner the scheduler may not run it for many milliseconds,
// so the wait is bounded by time (seconds), never by an iteration count.
// A real leak never converges and fails after the bound; a healthy run
// returns on the first or second poll.
func waitSubscribeWatchers(done func(n int) bool) int {
	deadline := time.Now().Add(10 * time.Second)
	n := subscribeWatchers()
	for !done(n) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		n = subscribeWatchers()
	}
	return n
}

// subscribeBaseline returns the number of Subscribe watchers alive
// before the test registers its own. It first waits for the count to
// reach zero so a watcher still winding down from the previous test is
// not mistaken for the baseline (which would let a later leak hide
// behind it); if something genuinely never exits, the bounded wait ends
// and the remaining count is the baseline, so that earlier leak is
// excluded here and left for the test that caused it to report.
func subscribeBaseline() int {
	return waitSubscribeWatchers(func(n int) bool { return n == 0 })
}

// requireWatcherStarted proves the measurement can see the watcher at
// all: after Subscribe the count must reach baseline+1. Without this a
// renamed closure would make every "no leak" assertion vacuously pass.
func requireWatcherStarted(t *testing.T, baseline int) {
	t.Helper()
	if got := waitSubscribeWatchers(func(n int) bool { return n >= baseline+1 }); got != baseline+1 {
		t.Fatalf("subscribe watchers after Subscribe = %d, want %d: the measurement cannot see the watcher goroutine", got, baseline+1)
	}
}

func TestThrottleLadderSubscribeDeliversToAllSubscribers(t *testing.T) {
	clk := rt.NewFixedClock(time.Unix(0, 0))
	src := &stubPressureSource{}
	l := newTestLadder(LadderConfig{StepDownDwell: -time.Second}, src, clk)

	ch1, cancel1 := l.Subscribe(context.Background())
	ch2, cancel2 := l.Subscribe(context.Background())
	defer cancel1()
	defer cancel2()

	src.p = 0.99
	l.tick()

	for i, ch := range []<-chan ThrottleEvent{ch1, ch2} {
		select {
		case ev := <-ch:
			if ev.Stage != StageHalt || ev.PreviousStage != StageNormal {
				t.Fatalf("subscriber %d event = %+v, want Stage=StageHalt PreviousStage=StageNormal", i, ev)
			}
		default:
			t.Fatalf("subscriber %d received no event after a real transition", i)
		}
	}
}

func TestThrottleLadderSubscribeNoEventOnUnchangedStage(t *testing.T) {
	clk := rt.NewFixedClock(time.Unix(0, 0))
	src := &stubPressureSource{}
	l := newTestLadder(LadderConfig{StepDownDwell: -time.Second}, src, clk)

	ch, cancel := l.Subscribe(context.Background())
	defer cancel()

	src.p = 0.0 // StageNormal, same as the initial stage: no transition
	l.tick()
	select {
	case ev := <-ch:
		t.Fatalf("received unexpected event %+v for a tick that left the stage unchanged", ev)
	default:
	}
}

func TestThrottleLadderSubscribeCancelStopsDelivery(t *testing.T) {
	clk := rt.NewFixedClock(time.Unix(0, 0))
	src := &stubPressureSource{}
	l := newTestLadder(LadderConfig{StepDownDwell: -time.Second}, src, clk)

	before := subscribeBaseline()

	ch, cancel := l.Subscribe(context.Background())
	requireWatcherStarted(t, before)
	src.p = 0.99
	l.tick()
	<-ch // drain the one real transition

	cancel()
	if got := waitSubscribeWatchers(func(n int) bool { return n <= before }); got > before {
		t.Fatalf("subscribe watchers after cancel = %d, want at or below pre-Subscribe baseline %d (leak)", got, before)
	}

	src.p = 0.0
	l.tick() // a real transition, but nothing should be listening anymore
	if ev, ok := <-ch; ok {
		t.Fatalf("received %+v on a channel after cancel(), want it closed with no further delivery", ev)
	}

	cancel() // idempotent: must not panic on a second call
}

func TestThrottleLadderSubscribeContextCancelAutoUnsubscribes(t *testing.T) {
	clk := rt.NewFixedClock(time.Unix(0, 0))
	src := &stubPressureSource{}
	l := newTestLadder(LadderConfig{StepDownDwell: -time.Second}, src, clk)

	before := subscribeBaseline()

	ctx, ctxCancel := context.WithCancel(context.Background())
	ch, cancel := l.Subscribe(ctx)
	defer cancel()
	requireWatcherStarted(t, before)
	ctxCancel()

	if got := waitSubscribeWatchers(func(n int) bool { return n <= before }); got > before {
		t.Fatalf("subscribe watchers after ctx cancellation = %d, want at or below baseline %d (leak)", got, before)
	}
	if _, ok := <-ch; ok {
		t.Fatal("channel still open (or delivering) after its context was cancelled")
	}
}
