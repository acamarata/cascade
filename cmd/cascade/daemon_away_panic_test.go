//go:build !windows

package main

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/notify"
)

// panicPresence is an operator-presence source whose seam dies on its Nth
// call, which is while the controller holds its mutex. Counting calls ties
// the panic to one specific Tick with no race against the tick before it.
type panicPresence struct {
	calls atomic.Int32
	dieOn int32
}

func (p *panicPresence) LastActivity() time.Time {
	if p.calls.Add(1) == p.dieOn {
		panic("presence seam died")
	}
	return time.Time{}
}

// TestAwayTickPanicFailsDeliveryOpen: a panic inside a seam Tick calls under
// its mutex, while Away, turns delivery back on, and the panic still reaches
// the supervisor (the row goes to Error with a "panic:" detail) instead of
// FailOpen blocking on the mutex the dead tick held.
func TestAwayTickPanicFailsDeliveryOpen(t *testing.T) {
	r := newAwayRig(t, nil)
	pres := &panicPresence{dieOn: 4}
	ctrl := notify.NewAwayController(notify.AwayDeps{
		Router: r.svc.Router, Presence: pres, Admission: awayIdle{}, Clock: r.clock, EntityID: awayEntityID,
	}, notify.AwayConfig{Threshold: 10 * time.Minute, DigestUrgentDeepLinks: 4})
	const name = "notify.away.panictest"
	r.w.Manifest.GoSupervised(r.w.Ctx, name, "test", func(ctx context.Context) error {
		return runAwayTicks(ctx, ctrl, r.tick)
	})
	r.clock.Advance(11 * time.Minute)
	for i := 0; i < 3; i++ { // ticks 1 and 2 enter Away; the third send returns once tick 2 finished
		select {
		case r.tick.c <- struct{}{}:
		case <-time.After(awayTestBound):
			t.Fatal("the tick loop never took a tick")
		}
	}
	if ctrl.State() != notify.StateAway {
		t.Fatalf("setup: state = %v, want Away before the panic", ctrl.State())
	}
	if !r.svc.Router.Accumulating() {
		t.Fatal("setup: not accumulating in Away")
	}
	select { // the fourth send is taken once tick 3 finished; tick 4 dies in the presence seam
	case r.tick.c <- struct{}{}:
	case <-time.After(awayTestBound):
		t.Fatal("the tick loop never took the fourth tick")
	}
	deadline := time.Now().Add(awayTestBound)
	for {
		if s := r.row(name); s.State == daemon.SubsystemError && strings.HasPrefix(s.Detail, "panic:") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the panic never reached the supervisor: row %+v (FailOpen blocked on the held mutex?)", r.row(name))
		}
		time.Sleep(5 * time.Millisecond)
	}
	if r.svc.Router.Accumulating() {
		t.Fatal("a dead tick loop left accumulation on")
	}
}

// awayIdle is an admission source that never reports work.
type awayIdle struct{}

func (awayIdle) Inflight() int   { return 0 }
func (awayIdle) QueueDepth() int { return 0 }
