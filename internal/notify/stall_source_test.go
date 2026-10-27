package notify

// Purpose (this file): BusStallSource over a REAL events.Bus and the REAL
//   supervision stall publisher, so the namespace, kind and payload names it
//   reads are proven against the code that writes them. Restart cases reopen a
//   bus over the same store. No sleeps: Run is paced by the afterHandle hook,
//   and the cancel cases cancel before Run.

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

type stallRig struct {
	t     *testing.T
	store provider.Store
	clock *testkit.FrozenClock
	bus   *events.Bus
	logs  *bytes.Buffer // read only after Run has been joined
}

func newStallRig(t *testing.T) *stallRig {
	t.Helper()
	r := &stallRig{t: t, store: storetest.NewMemStore(), clock: testkit.NewFrozenClock(time.Unix(1_700_000_000, 0)), logs: &bytes.Buffer{}}
	r.reopen()
	return r
}

// reopen closes the bus and opens a new one over the same store: a restart.
func (r *stallRig) reopen() {
	r.t.Helper()
	if r.bus != nil {
		r.must(r.bus.Close())
	}
	r.bus = events.New(r.store, r.clock)
	r.t.Cleanup(func() { r.must(r.bus.Close()) }) // Close is idempotent
}

func (r *stallRig) must(err error) {
	r.t.Helper()
	if err != nil {
		r.t.Fatal(err)
	}
}

func (r *stallRig) open() *BusStallSource {
	r.t.Helper()
	src, err := NewBusStallSource(context.Background(), r.bus, slog.New(slog.NewTextHandler(r.logs, nil)))
	r.must(err)
	return src
}

// publishStall publishes through the production supervision publisher.
func (r *stallRig) publishStall(session string, since int64) {
	r.t.Helper()
	ev := supervision.StallEvent{SessionID: session, StallKind: supervision.StallKindIdle, StalledSince: since, ElapsedSeconds: 9}
	r.must(supervision.NewBusStallPublisher(r.bus).PublishStalled(context.Background(), ev))
}

func (r *stallRig) publishRaw(kind events.EventKind, payload []byte) {
	r.t.Helper()
	_, err := r.bus.Publish(context.Background(), "supervision", kind, "test", payload)
	r.must(err)
}

// running starts src.Run; wait(n) blocks until n events were fully handled,
// and stop cancels and joins Run.
func running(t *testing.T, src *BusStallSource) (wait func(n int), stop func() error) {
	t.Helper()
	handled := make(chan struct{}, 1024)
	src.afterHandle = func(events.Event) { handled <- struct{}{} }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- src.Run(ctx) }()
	wait = func(n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			select {
			case <-handled:
			case <-time.After(10 * time.Second):
				t.Fatalf("only %d of %d events were handled", i, n)
			}
		}
	}
	return wait, func() error { cancel(); return <-done }
}

// bareSource is a source with no subscription, for cases that feed handle directly.
func bareSource(logs *bytes.Buffer) *BusStallSource {
	return &BusStallSource{log: slog.New(slog.NewTextHandler(logs, nil)), seen: map[string]struct{}{}}
}

func feedStall(src *BusStallSource, id string) {
	src.handle(events.Event{Kind: stallKind, Payload: events.EncodeNotificationPayload(events.NotificationPayload{ID: id})})
}

func noticeIDs(ns []StallNotice) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.NotificationID
	}
	return out
}

func TestBusStallSourceDeliversEachNoticeOnce(t *testing.T) {
	r := newStallRig(t)
	src := r.open()
	wait, stop := running(t, src)
	r.publishStall("s1", 1000)
	r.publishStall("s2", 0)
	r.publishStall("s3", 3000)
	wait(3)
	r.must(stop())
	want := fmt.Sprint([]StallNotice{
		{NotificationID: "stall:s1:1000", CorrelationID: "s1"},
		{NotificationID: "stall:s2:unknown", CorrelationID: "s2"},
		{NotificationID: "stall:s3:3000", CorrelationID: "s3"},
	})
	if got := fmt.Sprint(src.DrainStallNotices()); got != want {
		t.Fatalf("drained %s, want %s", got, want)
	}
	if again := src.DrainStallNotices(); len(again) != 0 {
		t.Fatalf("second drain returned %v, want none", again)
	}
}

func TestBusStallSourceReplayDoesNotRedeliver(t *testing.T) {
	r := newStallRig(t)
	a := r.open()
	wait, stop := running(t, a)
	r.publishStall("old1", 1)
	r.publishStall("old2", 2)
	wait(2)
	r.must(stop())
	if got := noticeIDs(a.DrainStallNotices()); len(got) != 2 {
		t.Fatalf("first run drained %v, want 2", got)
	}
	r.reopen() // a restart over the same store
	b := r.open()
	wait, stop = running(t, b)
	r.publishStall("new", 3)
	wait(1)
	r.must(stop())
	if got := noticeIDs(b.DrainStallNotices()); len(got) != 1 || got[0] != "stall:new:3" {
		t.Fatalf("reopened source drained %v, want only the new event", got)
	}
}

func TestBusStallSourceDedupesDuplicateID(t *testing.T) {
	r := newStallRig(t)
	src := r.open()
	wait, stop := running(t, src)
	r.publishStall("dup", 5)
	r.publishStall("dup", 5) // same episode published again
	r.publishStall("other", 6)
	wait(3)
	r.must(stop())
	if got := noticeIDs(src.DrainStallNotices()); len(got) != 2 || got[0] != "stall:dup:5" || got[1] != "stall:other:6" {
		t.Fatalf("drained %v, want the duplicate dropped", got)
	}
}

// The duplicate memory is bounded: the oldest ID leaves after stallSeenCap newer ones.
func TestBusStallSourceDedupeMemoryIsBounded(t *testing.T) {
	src := bareSource(&bytes.Buffer{})
	feed := func(id string) { feedStall(src, id) }
	for i := 0; i < stallSeenCap; i++ {
		feed(fmt.Sprintf("id-%d", i))
	}
	feed("id-1") // inside the window
	if n := len(src.DrainStallNotices()); n != stallSeenCap {
		t.Fatalf("drained %d, want %d (the in-window duplicate dropped)", n, stallSeenCap)
	}
	feed("fresh")  // pushes id-0 out
	feed("id-0")   // now outside the window: accepted again
	feed("id-500") // still inside
	if got := noticeIDs(src.DrainStallNotices()); len(got) != 2 || got[0] != "fresh" || got[1] != "id-0" {
		t.Fatalf("drained %v, want [fresh id-0]", got)
	}
	if len(src.seen) > stallSeenCap {
		t.Fatalf("duplicate memory holds %d IDs, want at most %d", len(src.seen), stallSeenCap)
	}
}

func TestBusStallSourceDropsUndecodablePayload(t *testing.T) {
	r := newStallRig(t)
	src := r.open()
	wait, stop := running(t, src)
	r.publishRaw("supervision.stalled", []byte("{not json"))
	r.publishRaw("supervision.stalled", []byte(`{"correlation_id":"c"}`)) // no id
	r.publishRaw("supervision.other", events.EncodeNotificationPayload(events.NotificationPayload{ID: "x"}))
	r.publishStall("good", 7)
	wait(4)
	r.must(stop())
	if got := noticeIDs(src.DrainStallNotices()); len(got) != 1 || got[0] != "stall:good:7" {
		t.Fatalf("drained %v, want only the decodable stalled event", got)
	}
	if n := strings.Count(r.logs.String(), "undecodable"); n != 2 {
		t.Fatalf("logged %d undecodable warnings, want one per bad payload (2):\n%s", n, r.logs.String())
	}
	if strings.Contains(r.logs.String(), "not json") {
		t.Fatal("the warning echoed the payload")
	}
}

func TestBusStallSourceFirstStartSkipsHistory(t *testing.T) {
	r := newStallRig(t)
	r.publishStall("history1", 1)
	r.publishStall("history2", 2)
	src := r.open() // first start: the cursor has never committed
	wait, stop := running(t, src)
	r.publishStall("live", 3)
	wait(1)
	r.must(stop())
	if got := noticeIDs(src.DrainStallNotices()); len(got) != 1 || got[0] != "stall:live:3" {
		t.Fatalf("drained %v, want only the event published after the start", got)
	}
}

// The bus commits its cursor when it hands an event to the source. An event
// handed over but still buffered when the source is cancelled must reach
// pending anyway: after a reopen over the same store the bus will not hand it
// again, so a source that dropped it would lose the notice.
func TestBusStallSourceCursorAfterHandoff(t *testing.T) {
	r := newStallRig(t)
	a := r.open()
	r.publishStall("held", 11)
	// Wait until the bus has handed the event over (cursor committed) with Run unstarted.
	deadline := time.Now().Add(10 * time.Second)
	for len(a.sub.Events) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the bus never handed the event to the subscription")
		}
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled between the handoff and any Drain
	r.must(a.Run(ctx))
	delivered := noticeIDs(a.DrainStallNotices())
	r.reopen()
	b := r.open()
	wait, stop := running(t, b)
	r.publishStall("after", 12)
	wait(1)
	r.must(stop())
	delivered = append(delivered, noticeIDs(b.DrainStallNotices())...)
	if len(delivered) != 2 || delivered[0] != "stall:held:11" || delivered[1] != "stall:after:12" {
		t.Fatalf("delivered %v across the cancel and the reopen, want [stall:held:11 stall:after:12] exactly once each", delivered)
	}
}

func TestBusStallSourceRunsOnceAndRefusesNoBus(t *testing.T) {
	r := newStallRig(t)
	src := r.open()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.must(src.Run(ctx))
	err := src.Run(context.Background())
	if err == nil || !cascade.HasKind(err, cascade.KindConflict) || !strings.Contains(err.Error(), "already ran") {
		t.Fatalf("second Run = %v, want the already-ran conflict", err)
	}
	if _, err := NewBusStallSource(context.Background(), nil, nil); err == nil || !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("nil bus = %v, want KindInvalidInput", err)
	}
	// Two live sources would share one cursor: the bus refuses the second.
	other := newStallRig(t)
	first := other.open()
	defer func() { other.must(first.sub.Unsubscribe()) }()
	if _, err := NewBusStallSource(context.Background(), other.bus, nil); err == nil || !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("second subscription on one cursor = %v, want KindConflict", err)
	}
}

func TestBusStallSourcePendingIsBounded(t *testing.T) {
	logs := &bytes.Buffer{}
	src := bareSource(logs)
	for i := 0; i < stallPendingCap+3; i++ {
		feedStall(src, fmt.Sprintf("n-%d", i))
	}
	got := src.DrainStallNotices()
	if len(got) != stallPendingCap || got[0].NotificationID != "n-3" {
		t.Fatalf("pending holds %d starting %q, want %d starting n-3 (the oldest dropped)", len(got), got[0].NotificationID, stallPendingCap)
	}
	if !strings.Contains(logs.String(), "dropping the oldest") {
		t.Fatal("dropping undrained notices was not WARNed")
	}
}
