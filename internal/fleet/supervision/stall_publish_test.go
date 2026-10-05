package supervision

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/pkg/cascade"
)

// stallQuiet makes s1 an active session that has been quiet past the
// threshold, and returns its stall start (unix millis).
func stallQuiet(fx *stallFixture, id string) int64 {
	fx.sess.put(id, "active", fx.nowMs())
	fx.d.Touch(id)
	since := fx.nowMs()
	fx.clock.Advance(2 * time.Minute)
	return since
}

func TestStallPublisherPayloadShape(t *testing.T) {
	fx := newStallFixture(t, fixtureOpts{})
	since := stallQuiet(fx, "s1")
	fx.markAlive()
	_ = fx.d.Poll(context.Background())
	evs, err := fx.bus.Replay(context.Background(), "supervision", 0)
	if err != nil || len(evs) != 1 {
		t.Fatalf("Replay = (%d, %v), want 1 event", len(evs), err)
	}
	ev := evs[0]
	p, err := events.DecodeNotificationPayload(ev.Payload)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	var body stalledBody
	if err := json.Unmarshal(p.Body, &body); err != nil {
		t.Fatalf("body: %v", err)
	}
	wantID := fmt.Sprintf("stall:s1:%d", since)
	if ev.Kind != "supervision.stalled" || ev.Source != "supervision.stall" || p.ID != wantID || p.CorrelationID != "s1" ||
		p.TargetSession != "s1" || p.DeepLink != "cascade://fleet/sessions/s1" || p.Visibility != "private" ||
		body.StallKind != "idle" || body.StalledSince != since || body.ElapsedSeconds != 120 {
		t.Errorf("event = %+v payload = %+v body = %+v, want the contract shape (id %s)", ev, p, body, wantID)
	}
}

// TestStallPublisherOncePerEpisode hammers one episode with repeated Polls
// (each allowed to advance) and Observes, then asserts on the STORED event
// log: exactly one supervision.stalled.
func TestStallPublisherOncePerEpisode(t *testing.T) {
	ctx := context.Background()
	fx := newStallFixture(t, fixtureOpts{})
	fx.markAlive()
	stallQuiet(fx, "s1")
	for i := 0; i < 4; i++ {
		_ = fx.d.Poll(ctx)
		_ = fx.d.Observe(ctx, StallSignal{Kind: SignalBlocked, SessionID: "s1", At: fx.nowMs()})
		fx.clock.Advance(testRungDelay)
	}
	if got := fx.escalations("s1"); got < 4 {
		t.Fatalf("only %d ladder advances happened; the loop did not exercise repeated escalation", got)
	}
	if ids := fx.stalledEvents(); len(ids) != 1 {
		t.Fatalf("stored supervision.stalled events = %v, want exactly one for the episode", ids)
	}
}

func TestStallPublisherTouchEndsEpisode(t *testing.T) {
	ctx := context.Background()
	fx := newStallFixture(t, fixtureOpts{})
	fx.markAlive()
	first := stallQuiet(fx, "s1")
	_ = fx.d.Poll(ctx)
	_ = fx.d.Poll(ctx)
	if ids := fx.stalledEvents(); len(ids) != 1 {
		t.Fatalf("episode 1 published %v, want one", ids)
	}
	fx.d.Touch("s1") // progress: the episode ends
	second := fx.nowMs()
	fx.clock.Advance(2 * time.Minute)
	_ = fx.d.Poll(ctx)
	ids := fx.stalledEvents()
	want := []string{fmt.Sprintf("stall:s1:%d", first), fmt.Sprintf("stall:s1:%d", second)}
	if len(ids) != 2 || ids[0] != want[0] || ids[1] != want[1] {
		t.Fatalf("stored events = %v, want %v (a new episode publishes again)", ids, want)
	}
}

// TestStallStaleEpisodeNotPublished reproduces the interleaving where
// progress lands after Poll built its stall event and before that event is
// recorded: the stale episode publishes nothing and stamps no RungDelay
// clock, and the next real episode publishes exactly once.
func TestStallStaleEpisodeNotPublished(t *testing.T) {
	ctx := context.Background()
	fx := newStallFixture(t, fixtureOpts{})
	fx.markAlive()
	stallQuiet(fx, "s1")
	stale, epoch, ok := fx.d.pollEvent("s1", fx.clock.Now())
	if !ok || stale.StallKind != StallKindIdle {
		t.Fatalf("pollEvent = (%+v, %v), want an idle event", stale, ok)
	}
	fx.d.Touch("s1") // progress lands between event build and record
	second := fx.nowMs()
	if err := fx.d.recordAndAdvance(ctx, stale, epoch); err != nil {
		t.Fatalf("recordAndAdvance(stale): %v", err)
	}
	if err := fx.d.publishOnce(ctx, stale, epoch); err != nil {
		t.Fatalf("publishOnce(stale): %v", err)
	}
	if ids := fx.stalledEvents(); len(ids) != 0 || fx.escalations("s1") != 0 {
		t.Errorf("stale episode stored %v events and %d escalations, want none", ids, fx.escalations("s1"))
	}
	if ev, ok := fx.d.lookupStallEvent("s1"); ok {
		t.Errorf("stale episode recorded %+v as the session's stall event, want none", ev)
	}
	fx.clock.Advance(2 * time.Minute)
	_ = fx.d.Poll(ctx)
	_ = fx.d.Poll(ctx)
	want := fmt.Sprintf("stall:s1:%d", second)
	if ids := fx.stalledEvents(); len(ids) != 1 || ids[0] != want {
		t.Errorf("after the next real episode stored %v, want exactly [%s]", ids, want)
	}
}

// TestStallProgressDuringAdvanceKeepsNextEpisodeDue: progress lands while
// the ladder's rung is running (after the episode published). The ended
// episode must not stamp the RungDelay clock the next episode starts from,
// so the next real episode publishes on its first Poll.
func TestStallProgressDuringAdvanceKeepsNextEpisodeDue(t *testing.T) {
	ctx := context.Background()
	fx := newStallFixture(t, fixtureOpts{})
	fx.markAlive()
	first := stallQuiet(fx, "s1")
	touchedAt := fx.nowMs()
	fx.sess.onGet = func() { fx.d.Touch("s1") } // runs inside the retry rung
	if err := fx.d.Poll(ctx); err != nil {
		t.Fatalf("first Poll: %v", err)
	}
	fx.clock.Advance(2 * time.Minute)
	if err := fx.d.Poll(ctx); err != nil {
		t.Fatalf("second Poll: %v", err)
	}
	want := []string{fmt.Sprintf("stall:s1:%d", first), fmt.Sprintf("stall:s1:%d", touchedAt)}
	if ids := fx.stalledEvents(); len(ids) != 2 || ids[0] != want[0] || ids[1] != want[1] {
		t.Fatalf("stored %v, want %v (episode 2 due on its first Poll)", ids, want)
	}
}

// flakyPublisher fails its first failures calls with errPubDown.
type flakyPublisher struct {
	inner    StallPublisher
	failures int
	calls    int
}

var errPubDown = cascade.New(cascade.KindUnavailable, "bus down")

func (f *flakyPublisher) PublishStalled(ctx context.Context, ev StallEvent) error {
	f.calls++
	if f.calls <= f.failures {
		return errPubDown
	}
	return f.inner.PublishStalled(ctx, ev)
}

func TestStallPublishFailureRetriedNextPoll(t *testing.T) {
	ctx := context.Background()
	var flaky *flakyPublisher
	fx := newStallFixture(t, fixtureOpts{wrapPub: func(p StallPublisher) StallPublisher {
		flaky = &flakyPublisher{inner: p, failures: 1}
		return flaky
	}})
	fx.markAlive()
	stallQuiet(fx, "s1")
	if err := fx.d.Poll(ctx); err != errPubDown {
		t.Fatalf("Poll with a failing publish = %v, want errPubDown surfaced by identity", err)
	}
	if ids := fx.stalledEvents(); len(ids) != 0 {
		t.Fatalf("a failed publish stored %v, want nothing", ids)
	}
	if got := fx.escalations("s1"); got != 1 {
		t.Errorf("ladder advances = %d, want 1 (a failed publish never blocks escalation)", got)
	}
	fx.clock.Advance(testRungDelay)
	if err := fx.d.Poll(ctx); err != nil {
		t.Fatalf("second Poll: %v", err)
	}
	if ids := fx.stalledEvents(); len(ids) != 1 {
		t.Fatalf("after the retry stored %v, want exactly one", ids)
	}
	fx.clock.Advance(testRungDelay)
	_ = fx.d.Poll(ctx)
	if ids := fx.stalledEvents(); len(ids) != 1 || flaky.calls != 2 {
		t.Errorf("stored %v after %d publish calls, want one event from two calls", ids, flaky.calls)
	}
}

func TestBusStallPublisherWithoutBusRefusesTyped(t *testing.T) {
	var p *busStallPublisher
	if err := p.PublishStalled(context.Background(), StallEvent{SessionID: "s1"}); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("err = %v, want Unavailable", err)
	}
	if err := NewBusStallPublisher(nil).PublishStalled(context.Background(), StallEvent{SessionID: "s1"}); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("nil bus err = %v, want Unavailable", err)
	}
}
