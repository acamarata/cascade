package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/plugins/claude"
)

// Purpose (this file): the translation between the fleet's session
//   vocabulary and the plugin's lifecycle one, and the emitter that puts a
//   transition on the real bus.
// Constraints: no socket. The state mapping is pure, and the bus is
//   constructed in-process over a memory store — Art.7.2's default unit
//   lane forbids importing net at all.
// SPORT: internal/plugins tests (ADD) — P1-E16-W4-S34-T1.

// TestTheFleetStatesMapOntoLifecycleStates is the substantive half of the
// wiring, and it is asserted over EVERY state the fleet defines rather
// than the three that happen to be interesting — a state added later
// without a mapping decision should fail here.
func TestTheFleetStatesMapOntoLifecycleStates(t *testing.T) {
	for _, tc := range []struct {
		state   sessions.SessionState
		want    claude.SessionState
		forward bool
	}{
		{sessions.StateActive, claude.SessionRunning, true},
		{sessions.StateIdle, claude.SessionIdle, true},
		{sessions.StateBlocked, claude.SessionIdle, true},
		{sessions.StateStalled, claude.SessionIdle, true},
		{sessions.StateClosed, claude.SessionStopped, true},
		// The fail-closed sentinel is DROPPED, never mapped: turning it
		// into a lifecycle event would invent a fact the fleet
		// explicitly declined to assert.
		{sessions.StateUnknown, "", false},
	} {
		got, ok := claudeSessionEvent(sessions.SessionRecord{
			SessionID: "s1", Harness: "claude", PID: 7, State: tc.state.String(),
		})
		if ok != tc.forward {
			t.Errorf("state %q: forwarded = %v, want %v", tc.state, ok, tc.forward)
			continue
		}
		if !tc.forward {
			continue
		}
		if got.State != tc.want {
			t.Errorf("state %q mapped to %q, want %q", tc.state, got.State, tc.want)
		}
		if got.ID != "s1" || got.Harness != "claude" || got.PID != 7 {
			t.Errorf("state %q: record fields were lost: %+v", tc.state, got)
		}
	}
}

// TestAnUnrecognizedStateIsDropped proves a value from a newer build is
// not forwarded as some default.
func TestAnUnrecognizedStateIsDropped(t *testing.T) {
	if _, ok := claudeSessionEvent(sessions.SessionRecord{
		SessionID: "s1", Harness: "claude", State: "hibernating",
	}); ok {
		t.Fatal("a state this build does not recognize was forwarded")
	}
}

// TestTheEmitterPutsTheTransitionOnTheBus asserts STATE — the event is
// really on the bus and carries the fields a consumer reads — rather than
// that a call was made.
func TestTheEmitterPutsTheTransitionOnTheBus(t *testing.T) {
	bus := events.New(storetest.NewMemStore(), runtime.NewSystemClock())
	t.Cleanup(func() { _ = bus.Close() })

	emit := claudeLifecycleEmitter(bus)
	err := emit(context.Background(), claude.LifecycleEvent{
		Session:     claude.SessionEvent{ID: "s1", Harness: "claude", PID: 7, State: claude.SessionStopped},
		Transition:  claude.LifecycleStopped,
		Synthesized: true,
	})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}

	replayed, err := bus.Replay(context.Background(), claudeWatchNamespace, 0)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(replayed) != 1 {
		t.Fatalf("the bus carries %d events, want 1", len(replayed))
	}

	var payload map[string]any
	if err := json.Unmarshal(replayed[0].Payload, &payload); err != nil {
		t.Fatalf("the payload is not JSON: %v", err)
	}
	for field, want := range map[string]any{
		"session_id":  "s1",
		"harness":     "claude",
		"transition":  string(claude.LifecycleStopped),
		"synthesized": true,
	} {
		if payload[field] != want {
			t.Errorf("payload[%q] = %v, want %v", field, payload[field], want)
		}
	}
}

// TestASynthesizedStopIsMarkedAsInferred is the field a consumer needs to
// tell an observed stop from one this plugin concluded. Without the
// marker an inferred stop would read as authoritative.
func TestASynthesizedStopIsMarkedAsInferred(t *testing.T) {
	bus := events.New(storetest.NewMemStore(), runtime.NewSystemClock())
	t.Cleanup(func() { _ = bus.Close() })
	emit := claudeLifecycleEmitter(bus)

	for _, synthesized := range []bool{false, true} {
		if err := emit(context.Background(), claude.LifecycleEvent{
			Session:     claude.SessionEvent{ID: "s1", Harness: "claude"},
			Transition:  claude.LifecycleStopped,
			Synthesized: synthesized,
		}); err != nil {
			t.Fatalf("emit: %v", err)
		}
	}

	replayed, err := bus.Replay(context.Background(), claudeWatchNamespace, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed) != 2 {
		t.Fatalf("the bus carries %d events, want 2", len(replayed))
	}
	for i, wantSynth := range []bool{false, true} {
		var payload map[string]any
		if err := json.Unmarshal(replayed[i].Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["synthesized"] != wantSynth {
			t.Errorf("event %d: synthesized = %v, want %v", i, payload["synthesized"], wantSynth)
		}
	}
}

// TestTheWatcherIsBuiltWithBothSeamsWired proves the constructor produces
// something runnable rather than a half-wired struct whose failure would
// only appear when the daemon started it.
func TestTheWatcherIsBuiltWithBothSeamsWired(t *testing.T) {
	bus := events.New(storetest.NewMemStore(), runtime.NewSystemClock())
	t.Cleanup(func() { _ = bus.Close() })

	w := NewClaudeSessionWatcher(nil, bus)
	if w == nil {
		t.Fatal("NewClaudeSessionWatcher returned nil")
	}
	if w.Subscribe == nil {
		t.Error("the watcher has no subscriber")
	}
	if w.Emit == nil {
		t.Error("the watcher has no emitter")
	}
}

// openWith returns an opener that replays recs and records its release.
//
// This is the seam that used to be a dial. While the subscriber opened its
// own socket, everything below — the translation, the drop rule, the
// release, the cancellation — was reachable only from a daemon with a
// live stream, which is to say not asserted anywhere.
func openWith(recs []sessions.SessionRecord, released *bool) SessionStreamOpener {
	return func(context.Context) (<-chan sessions.SessionRecord, func(), error) {
		ch := make(chan sessions.SessionRecord, len(recs))
		for _, r := range recs {
			ch <- r
		}
		close(ch)
		return ch, func() { *released = true }, nil
	}
}

// TestTheSubscriberForwardsOnlyRecordsItCanMap proves the drop rule
// survives the translation loop: an unmappable state must not reach the
// plugin as some default, and must not stop the records after it.
func TestTheSubscriberForwardsOnlyRecordsItCanMap(t *testing.T) {
	released := false
	subscribe := claudeSessionSubscriber(openWith([]sessions.SessionRecord{
		{SessionID: "s1", Harness: "claude", PID: 1, State: sessions.StateActive.String()},
		{SessionID: "s2", Harness: "claude", PID: 2, State: "hibernating"},
		{SessionID: "s3", Harness: "claude", PID: 3, State: sessions.StateClosed.String()},
	}, &released))

	events, release, err := subscribe(context.Background())
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	var got []claude.SessionEvent
	for ev := range events {
		got = append(got, ev)
	}
	if len(got) != 2 {
		t.Fatalf("forwarded %d events, want the unmappable one dropped and the rest kept: %+v", len(got), got)
	}
	if got[0].ID != "s1" || got[0].State != claude.SessionRunning {
		t.Errorf("first event = %+v", got[0])
	}
	if got[1].ID != "s3" || got[1].State != claude.SessionStopped {
		t.Errorf("second event = %+v, want the record AFTER the dropped one", got[1])
	}

	release()
	if !released {
		t.Error("closing the subscription did not release the underlying stream")
	}
}

// TestTheSubscriberRefusesWithNoStreamWired proves a half-wired watcher
// fails as a typed error rather than a nil-pointer panic. For a background
// watch a panic means a dead goroutine and a daemon that silently stops
// observing, which is worse than a loud failure.
func TestTheSubscriberRefusesWithNoStreamWired(t *testing.T) {
	if _, _, err := claudeSessionSubscriber(nil)(context.Background()); err == nil {
		t.Fatal("a subscriber with no stream opened one")
	}
}

// TestTheSubscriberPropagatesAnOpenFailure proves a stream that cannot be
// opened is reported, not swallowed into an empty channel that would read
// as "no sessions".
func TestTheSubscriberPropagatesAnOpenFailure(t *testing.T) {
	want := errors.New("no daemon socket")
	subscribe := claudeSessionSubscriber(func(context.Context) (<-chan sessions.SessionRecord, func(), error) {
		return nil, nil, want
	})
	events, _, err := subscribe(context.Background())
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want the open failure", err)
	}
	if events != nil {
		t.Error("a failed open still returned a channel a caller would range over forever")
	}
}

// TestTheSubscriberStopsOnCancellation proves an abandoned watch ends its
// goroutine instead of blocking forever on a consumer that has gone away.
func TestTheSubscriberStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// One more record than the output buffer holds, so the loop is
	// certain to block on the send and must notice the cancellation.
	recs := make([]sessions.SessionRecord, 32)
	for i := range recs {
		recs[i] = sessions.SessionRecord{SessionID: "s", Harness: "claude", State: sessions.StateActive.String()}
	}
	released := false
	events, _, err := claudeSessionSubscriber(openWith(recs, &released))(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	cancel()

	// The channel must close rather than hang; ranging to completion is
	// the assertion, and the test times out if the goroutine leaks.
	for range events { //nolint:revive // draining is the assertion
	}
}
