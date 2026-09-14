package nodes

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
)

// captureBus is a minimal EventBus fake recording every published event.
type captureBus struct {
	published []struct {
		ns, kind, source string
		payload          []byte
	}
}

func (b *captureBus) Publish(_ context.Context, ns string, kind events.EventKind, source string, payload []byte) (events.Event, error) {
	b.published = append(b.published, struct {
		ns, kind, source string
		payload          []byte
	}{ns, string(kind), source, payload})
	return events.Event{}, nil
}

func newTestRecordStore(t *testing.T) *RecordStore {
	t.Helper()
	return NewRecordStore(NewFileRecordBackend(t.TempDir()), newFakeClock())
}

// awaitProbePasses advances the clock and fires the ticker n times,
// waiting after each fire for the probe function to actually enter
// (through probed) instead of sleeping a fixed interval. The old form
// slept 10ms per pass and assumed the prober goroutine had been scheduled
// and finished within it; on a loaded CI runner it had not, the streak
// never reached ProbeMissThreshold, and the assertion failed with
// presence still "reachable". TestProberTriggerOutOfCycleRunsImmediately
// in this same file already used this handshake.
//
// The caller must still cancel the prober and wait for Run to return
// before asserting on the store: probed is sent from INSIDE the probe
// function, so the record write for the final pass happens after it, and
// Run only returns once that pass has completed.
func awaitProbePasses(t *testing.T, clock *fakeClock, ticker *fakeTicker, probed <-chan struct{}, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		clock.advance(15 * time.Second)
		ticker.fire()
		select {
		case <-probed:
		case <-time.After(10 * time.Second):
			t.Fatalf("probe pass %d of %d never ran", i+1, n)
		}
	}
}

func TestProberDirectMissesTransitionToUnavailable(t *testing.T) {
	store := newTestRecordStore(t)
	if err := store.put(DeviceRecord{NodeID: "n1", Presence: PresenceReachable}); err != nil {
		t.Fatal(err)
	}
	clock := newFakeClock()
	ticker := newFakeTicker()
	bus := &captureBus{}
	probed := make(chan struct{})
	prober := NewProber(ProberDeps{
		Records: store,
		Clock:   clock,
		Ticker:  ticker,
		Bus:     bus,
		Probe: func(_ context.Context, _ DeviceRecord, now time.Time) ProbeOutcome {
			probed <- struct{}{}
			return ProbeOutcome{Reachable: false, At: now}
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { prober.Run(ctx); close(done) }()

	awaitProbePasses(t, clock, ticker, probed, ProbeMissThreshold)
	cancel()
	<-done

	assertMissesTransitioned(t, store, bus)
}

// assertMissesTransitioned checks the post-conditions of a full miss
// streak. It no longer takes a probe-call count: awaitProbePasses
// receives once per pass and fails the test if any pass does not run, so
// "the probe actually ran, ProbeMissThreshold times" is already proven
// before this is reached, and more strictly than a non-zero counter did.
func assertMissesTransitioned(t *testing.T, store *RecordStore, bus *captureBus) {
	t.Helper()
	rec, err := store.Get("n1")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Presence != PresenceUnavailable {
		t.Fatalf("presence = %q, want unavailable after %d misses", rec.Presence, ProbeMissThreshold)
	}
	found := false
	for _, e := range bus.published {
		if e.kind == string(NodePresenceChangedKind) {
			found = true
		}
	}
	if !found {
		t.Fatal("node.presence.changed was never published")
	}
}

func TestProberTriggerOutOfCycleRunsImmediately(t *testing.T) {
	store := newTestRecordStore(t)
	if err := store.put(DeviceRecord{NodeID: "n1", Presence: PresenceReachable}); err != nil {
		t.Fatal(err)
	}
	clock := newFakeClock()
	ticker := newFakeTicker()
	calls := make(chan struct{}, 4)
	prober := NewProber(ProberDeps{
		Records: store,
		Clock:   clock,
		Ticker:  ticker,
		Probe: func(_ context.Context, _ DeviceRecord, now time.Time) ProbeOutcome {
			calls <- struct{}{}
			return ProbeOutcome{Reachable: true, At: now}
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { prober.Run(ctx); close(done) }()

	prober.TriggerOutOfCycle()
	select {
	case <-calls:
	case <-time.After(2 * time.Second):
		t.Fatal("TriggerOutOfCycle did not cause an immediate probe pass")
	}
	cancel()
	<-done
}

func TestHeartbeatProbeThreeWaySplit(t *testing.T) {
	now := time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)
	cases := []struct {
		name       string
		lastSeen   time.Time
		wantReach  bool
		wantNoEvid bool
	}{
		{"never seen", time.Time{}, false, true},
		{"fresh", now.Add(-5 * time.Second), true, false},
		{"stale-but-within-timeout", now.Add(-DefaultProbeInterval - 5*time.Second), false, false},
		{"past-timeout", now.Add(-DefaultHeartbeatTimeout - time.Second), false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := HeartbeatProbe(context.Background(), DeviceRecord{LastSeen: tc.lastSeen}, now)
			if out.Reachable != tc.wantReach || out.NoEvidence != tc.wantNoEvid {
				t.Errorf("HeartbeatProbe(%s) = %+v, want Reachable=%v NoEvidence=%v", tc.name, out, tc.wantReach, tc.wantNoEvid)
			}
		})
	}
}

func TestPresenceChangedPayloadMarshals(t *testing.T) {
	raw, err := marshalPresenceChanged(presenceChangedPayload{NodeID: "n1", From: PresenceReachable, To: PresenceUnavailable})
	if err != nil {
		t.Fatal(err)
	}
	var decoded presenceChangedPayload
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.NodeID != "n1" || decoded.From != PresenceReachable || decoded.To != PresenceUnavailable {
		t.Fatalf("decoded = %+v", decoded)
	}
}

// fakeRouteChecker reports routeOK for every nodeID, counting calls so
// tests can assert it was (or was not) consulted.
type fakeRouteChecker struct {
	routeOK bool
	calls   int
}

func (f *fakeRouteChecker) Reachable(context.Context, string) bool {
	f.calls++
	return f.routeOK
}

// TestProberRouteCheckedOnlyAtThirdMiss proves the route leg (S-72.T3)
// fires only once the direct-miss streak would reach ProbeMissThreshold,
// never on the first or second miss (R-16.37 §Nodes).
func TestProberRouteCheckedOnlyAtThirdMiss(t *testing.T) {
	store := newTestRecordStore(t)
	rec := DeviceRecord{NodeID: "n1", Presence: PresenceReachable, Route: &RouteConfig{User: "u", Addr: "h:22"}}
	if err := store.put(rec); err != nil {
		t.Fatal(err)
	}
	clock, ticker, checker := newFakeClock(), newFakeTicker(), &fakeRouteChecker{routeOK: true}
	probed := make(chan struct{})
	prober := NewProber(ProberDeps{
		Records: store, Clock: clock, Ticker: ticker, RouteChecker: checker,
		Probe: func(_ context.Context, _ DeviceRecord, now time.Time) ProbeOutcome {
			probed <- struct{}{}
			return ProbeOutcome{Reachable: false, At: now}
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { prober.Run(ctx); close(done) }()

	awaitProbePasses(t, clock, ticker, probed, ProbeMissThreshold)
	cancel()
	<-done

	if checker.calls != 1 {
		t.Fatalf("RouteChecker.Reachable calls = %d, want exactly 1 (only at the 3rd miss)", checker.calls)
	}
	got, err := store.Get("n1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Presence != PresenceRemoteViaRoute {
		t.Fatalf("presence = %q, want remote-via-route", got.Presence)
	}
}

// TestProberRouteFailsStaysUnavailable proves a configured-but-
// unanswering route falls through to the ordinary unavailable
// transition, never a silent reachable.
func TestProberRouteFailsStaysUnavailable(t *testing.T) {
	store := newTestRecordStore(t)
	rec := DeviceRecord{NodeID: "n1", Presence: PresenceReachable, Route: &RouteConfig{User: "u", Addr: "h:22"}}
	if err := store.put(rec); err != nil {
		t.Fatal(err)
	}
	clock, ticker, checker := newFakeClock(), newFakeTicker(), &fakeRouteChecker{routeOK: false}
	probed := make(chan struct{})
	prober := NewProber(ProberDeps{
		Records: store, Clock: clock, Ticker: ticker, RouteChecker: checker,
		Probe: func(_ context.Context, _ DeviceRecord, now time.Time) ProbeOutcome {
			probed <- struct{}{}
			return ProbeOutcome{Reachable: false, At: now}
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { prober.Run(ctx); close(done) }()

	awaitProbePasses(t, clock, ticker, probed, ProbeMissThreshold)
	cancel()
	<-done

	got, err := store.Get("n1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Presence != PresenceUnavailable {
		t.Fatalf("presence = %q, want unavailable: route also failed", got.Presence)
	}
}
