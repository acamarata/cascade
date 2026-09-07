package governor

// Purpose: Subscribe's test suite: every transition delivered to every
// subscriber, cancel() stopping delivery, ctx cancellation doing the
// same automatically, and no goroutine leak across either path. Driven
// entirely by tick() calls against a FixedClock - never a real sleep.

import (
	"context"
	"runtime"
	"testing"
	"time"

	rt "github.com/acamarata/cascade/internal/runtime"
)

// waitGoroutineCount polls runtime.NumGoroutine (yielding via
// runtime.Gosched, never time.Sleep) until it matches want or a bounded
// number of attempts is exhausted, returning the last observed count.
// This is the standard non-sleeping idiom for a goroutine-leak
// assertion: the watcher goroutines Subscribe starts exit asynchronously
// relative to cancel(), so some scheduling slack is unavoidable, but no
// wall-clock wait is ever needed to prove convergence.
func waitGoroutineCount(t *testing.T, want int) int {
	t.Helper()
	got := runtime.NumGoroutine()
	for i := 0; i < 10000 && got != want; i++ {
		runtime.Gosched()
		got = runtime.NumGoroutine()
	}
	return got
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

	before := waitGoroutineCount(t, -1) // just a snapshot; -1 never matches

	ch, cancel := l.Subscribe(context.Background())
	src.p = 0.99
	l.tick()
	<-ch // drain the one real transition

	cancel()
	if got := waitGoroutineCount(t, before); got != before {
		t.Fatalf("goroutine count after cancel = %d, want back to pre-Subscribe baseline %d (leak)", got, before)
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

	before := waitGoroutineCount(t, -1)

	ctx, ctxCancel := context.WithCancel(context.Background())
	ch, cancel := l.Subscribe(ctx)
	defer cancel()
	ctxCancel()

	if got := waitGoroutineCount(t, before); got != before {
		t.Fatalf("goroutine count after ctx cancellation = %d, want back to baseline %d (leak)", got, before)
	}
	if _, ok := <-ch; ok {
		t.Fatal("channel still open (or delivering) after its context was cancelled")
	}
}
