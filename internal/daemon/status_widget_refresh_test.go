package daemon

// Purpose (this file): the refresh-loop and emit-path acceptance tests
// (P1-WID-08): a registry change written through another handle emits once
// with seq+1, an unchanged step emits nothing though updated_at advanced,
// an attention push through another Store reaches the next frame, a failed
// read ages the rows instead of dropping or refreshing them, the loop
// recovers a tick panic and joins on cancel, and a push uses the run context.
//
// Inputs: widgetHarness (status_widget_harness_test.go).
// Outputs: none.
// Constraints: the bus is a real events.Bus over the in-memory store and is
// asserted through what it RECORDED (Replay), never through a subscriber's
// timing.
//
// SPORT: daemon.status_widget (tests, P1-WID-08).

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/runtime"
)

func TestStatusWidgetRefreshEmitsOnRegistryChange(t *testing.T) {
	h := newWidgetHarness(t)
	h.seed("alpha", registry.LaneStateAvailable, time.Time{})

	h.ticker.step(t)
	frames := h.frames()
	if len(frames) != 1 || frames[0].Seq != 1 || rowOf(t, frames[0], "alpha").State != "available" {
		t.Fatalf("first step recorded %+v, want one frame, seq 1, alpha available", frames)
	}

	// Another process's path: a second registry handle flips the lane.
	h.clock.Advance(10 * time.Second)
	h.setLane("alpha", registry.LaneStateExhausted, h.clock.Now().Add(2*time.Hour))
	h.ticker.step(t)
	frames = h.frames()
	if len(frames) != 2 || frames[1].Seq != 2 {
		t.Fatalf("after the lane change the bus holds %d frames (last seq %d), want 2 with seq 2", len(frames), frames[len(frames)-1].Seq)
	}
	row := rowOf(t, frames[1], "alpha")
	if row.State != "exhausted" || row.FiveHour.ResetsIn == nil || *row.FiveHour.ResetsIn != 2*time.Hour || row.SevenDay.ResetsIn != nil {
		t.Fatalf("emitted row = %+v, want exhausted, five_hour 2h, seven_day null", row)
	}

	// No change, but time moves: updated_at and the reset countdown advance,
	// and nothing may be published.
	h.clock.Advance(10 * time.Second)
	h.ticker.step(t)
	if got := len(h.frames()); got != 2 {
		t.Fatalf("an unchanged step left %d frames on the bus, want 2", got)
	}
	snap, err := h.rpcSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if got := rowOf(t, snap, "alpha"); !got.UpdatedAt.Equal(h.clock.Now()) || snap.Seq != 2 {
		t.Fatalf("status.widget row updated_at %v seq %d, want now and last emitted seq 2", got.UpdatedAt, snap.Seq)
	}
}

func TestStatusWidgetRefreshEmitsOnAttentionFromOtherStore(t *testing.T) {
	h := newWidgetHarness(t)
	h.seed("alpha", registry.LaneStateAvailable, time.Time{})
	h.ticker.step(t)
	if got := h.frames(); len(got) != 1 || got[0].AttentionCount != 0 {
		t.Fatalf("baseline frames = %+v, want one with attention_count 0", got)
	}

	// A Store the widget does not own, over the same data: no hook of ours
	// fires, so only the next tick can see it.
	other := supervision.NewStore(h.store, h.clock, nil, supervision.NewSystemIDGenerator(), 0)
	if _, err := other.Push(context.Background(), supervision.AttentionItem{Kind: supervision.KindStall, SourceRef: "session-1", ScopeRef: defaultWidgetScope()}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if got := len(h.frames()); got != 1 {
		t.Fatalf("a push through another Store emitted %d frames before the tick, want none", got-1)
	}
	h.ticker.step(t)
	frames := h.frames()
	if len(frames) != 2 || frames[1].AttentionCount != 1 || frames[1].Seq != 2 {
		t.Fatalf("frames after the tick = %+v, want a second frame (seq 2) with attention_count 1", frames)
	}
}

func TestStatusWidgetUpdatedAtAgesOnReadError(t *testing.T) {
	h := newWidgetHarness(t)
	h.seed("alpha", registry.LaneStateExhausted, harnessNow.Add(time.Hour))
	h.ticker.step(t)
	first := rowOf(t, h.frames()[0], "alpha")

	h.source.failLanes.Store(true)
	h.clock.Advance(20 * time.Second)
	h.ticker.step(t)
	if got := len(h.frames()); got != 1 {
		t.Fatalf("a failed read emitted: %d frames, want 1", got)
	}
	snap, err := h.rpcSnapshot()
	if err != nil {
		t.Fatalf("status.widget after a read error: %v, want the last rows", err)
	}
	row := rowOf(t, snap, "alpha")
	if len(snap.Rows) != 1 || !row.UpdatedAt.Equal(first.UpdatedAt) || !row.UpdatedAt.Before(snap.GeneratedAt) {
		t.Fatalf("rows %d, updated_at %v vs first %v, generated_at %v: want the last row, its old updated_at, older than generated_at",
			len(snap.Rows), row.UpdatedAt, first.UpdatedAt, snap.GeneratedAt)
	}
	if row.State != "exhausted" || row.FiveHour.ResetsIn == nil || *row.FiveHour.ResetsIn != time.Hour-20*time.Second {
		t.Fatalf("row after one failed read = %+v, want exhausted, reset rebased to 40m40s", row)
	}

	// Past the compositor TTL the row falls to unknown, still not dropped.
	h.clock.Advance(2 * time.Minute)
	h.ticker.step(t)
	snap, err = h.rpcSnapshot()
	row = rowOf(t, snap, "alpha")
	if err != nil || row.State != "unknown" || row.FiveHour.ResetsIn != nil || !row.UpdatedAt.Equal(first.UpdatedAt) || len(h.frames()) != 1 {
		t.Fatalf("after the TTL: err %v row %+v frames %d, want unknown, no reset, the old updated_at, nothing emitted", err, row, len(h.frames()))
	}
}

func TestStatusWidgetNeverReadSourceIsAnError(t *testing.T) {
	h := newWidgetHarness(t)
	h.source.failLanes.Store(true)
	h.seed("alpha", registry.LaneStateAvailable, time.Time{})
	h.ticker.step(t)
	if _, err := h.rpcSnapshot(); err == nil {
		t.Fatal("status.widget over a source that never read: nil error, an empty row list would read as no providers")
	}
	if got := len(h.frames()); got != 0 {
		t.Fatalf("a source that never read emitted %d frames", got)
	}
}

func TestStatusWidgetRefreshRecoversFromTickPanic(t *testing.T) {
	h := newWidgetHarness(t)
	h.seed("alpha", registry.LaneStateAvailable, time.Time{})
	h.source.panicLanes.Store(true)
	h.ticker.step(t)
	if got := len(h.frames()); got != 0 {
		t.Fatalf("the panicking tick emitted %d frames", got)
	}
	if !strings.Contains(h.logs.String(), "tick panicked") {
		t.Fatalf("the recovered panic was not logged: %q", h.logs.String())
	}
	h.ticker.step(t)
	if got := h.frames(); len(got) != 1 || got[0].Seq != 1 {
		t.Fatalf("the tick after the panic recorded %+v, want one frame with seq 1 (the loop survived)", got)
	}
}

func TestStatusWidgetRefreshJoinsOnCancel(t *testing.T) {
	h := newWidgetHarness(t)
	h.cancel()
	done := make(chan struct{})
	go func() { h.manifest.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Manifest.Wait did not return after the run context was cancelled: the loop leaked")
	}
	if !h.ticker.stopped.Load() {
		t.Fatal("the loop returned without stopping its ticker")
	}
}

// TestStatusWidgetAttentionPushUsesRunContext: the hook emits under the run
// context, so a push after cancel publishes nothing, while the same push
// before cancel does. context.Background() at that call site would emit.
func TestStatusWidgetAttentionPushUsesRunContext(t *testing.T) {
	h := newWidgetHarness(t)
	h.seed("alpha", registry.LaneStateAvailable, time.Time{})
	push := func(ref string) {
		t.Helper()
		item := supervision.AttentionItem{Kind: supervision.KindStall, SourceRef: ref, ScopeRef: defaultWidgetScope()}
		if _, err := h.deps.attention.Push(context.Background(), item); err != nil {
			t.Fatalf("Push: %v", err)
		}
	}
	push("session-1")
	if got := h.frames(); len(got) != 1 || got[0].AttentionCount != 1 {
		t.Fatalf("a push through deps' own Store recorded %+v, want one frame with attention_count 1", got)
	}
	h.cancel()
	h.manifest.Wait()
	push("session-2")
	if got := len(h.frames()); got != 1 {
		t.Fatalf("a push after the run context ended emitted %d frames, want it to publish nothing", got-1)
	}
}

func TestStatusWidgetRefreshLoopNeedsNoBus(t *testing.T) {
	clock := runtime.NewFixedClock(harnessNow)
	deps := &StatusWidgetDeps{clock: clock}
	ctx, cancel := context.WithCancel(context.Background())
	ticker := newStepTicker()
	done := make(chan error, 1)
	go func() { done <- RunStatusWidgetRefresh(ctx, deps, nil, ticker, nil) }()
	ticker.waitIdle(t)
	ticker.step(t) // a nil-compositor deps and a nil bus must not panic or publish
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("RunStatusWidgetRefresh = %v, want nil on cancel", err)
	}
}
