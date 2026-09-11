package nodes

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeClock is a manually-advanced Clock for deterministic presence
// tests, shared by every *_test.go file in this package. Guarded by a
// mutex: prober_test.go/netwatch_test.go drive it from a test goroutine
// while a Prober/NetworkWatcher goroutine reads it concurrently.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	return c.now
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }

func TestPresenceParseUnrecognizedIsTypedError(t *testing.T) {
	_, err := parsePresence("bogus")
	if err == nil {
		t.Fatal("expected an error for an unrecognized presence string")
	}
	var kerr *cascade.Error
	if !errors.As(err, &kerr) || kerr.Kind != cascade.KindInvalidInput {
		t.Fatalf("err = %v, want KindInvalidInput", err)
	}
}

func TestPresenceHysteresisToUnavailable(t *testing.T) {
	clock := newFakeClock()
	rec := DeviceRecord{NodeID: "n1", Presence: PresenceReachable}
	for i := 0; i < ProbeMissThreshold-1; i++ {
		now := clock.advance(15 * time.Second)
		var transitioned bool
		rec, transitioned = AdvancePresence(rec, PresenceObservation{OK: false, At: now, Authenticated: true}, now)
		if transitioned {
			t.Fatalf("miss %d transitioned early, want no transition before threshold+dwell", i+1)
		}
	}
	now := clock.advance(15 * time.Second)
	rec, transitioned := AdvancePresence(rec, PresenceObservation{OK: false, At: now, Authenticated: true}, now)
	if !transitioned || rec.Presence != PresenceUnavailable {
		t.Fatalf("after %d misses over >=30s: presence=%q transitioned=%v, want unavailable/true", ProbeMissThreshold, rec.Presence, transitioned)
	}
}

func TestPresenceHysteresisToReachable(t *testing.T) {
	clock := newFakeClock()
	rec := DeviceRecord{NodeID: "n1", Presence: PresenceUnavailable}
	now := clock.advance(40 * time.Second)
	rec, transitioned := AdvancePresence(rec, PresenceObservation{OK: true, At: now, Authenticated: true}, now)
	if transitioned {
		t.Fatal("one success transitioned; want two successes spanning >=60s")
	}
	now = clock.advance(65 * time.Second)
	rec, transitioned = AdvancePresence(rec, PresenceObservation{OK: true, At: now, Authenticated: true}, now)
	if !transitioned || rec.Presence != PresenceReachable {
		t.Fatalf("after 2 successes over >=60s: presence=%q transitioned=%v, want reachable/true", rec.Presence, transitioned)
	}
}

func TestPresenceSingleProbeNeverTransitions(t *testing.T) {
	clock := newFakeClock()
	rec := DeviceRecord{NodeID: "n1", Presence: PresenceReachable}
	now := clock.advance(time.Second)
	_, transitioned := AdvancePresence(rec, PresenceObservation{OK: false, At: now, Authenticated: true}, now)
	if transitioned {
		t.Fatal("a single miss transitioned presence; want no transition below ProbeMissThreshold")
	}
	rec2 := DeviceRecord{NodeID: "n2", Presence: PresenceUnavailable}
	_, transitioned = AdvancePresence(rec2, PresenceObservation{OK: true, At: now, Authenticated: true}, now)
	if transitioned {
		t.Fatal("a single success transitioned presence; want no transition below ProbeHitThreshold")
	}
}

func TestPresenceStaleProbeDegradesToUnknown(t *testing.T) {
	clock := newFakeClock()
	rec := DeviceRecord{NodeID: "n1", Presence: PresenceReachable}
	staleAt := clock.now
	now := clock.advance(FreshnessWindow + time.Second)
	rec, transitioned := AdvancePresence(rec, PresenceObservation{OK: true, At: staleAt, Authenticated: true}, now)
	if !transitioned || rec.Presence != PresenceUnknown {
		t.Fatalf("stale evidence: presence=%q transitioned=%v, want unknown/true", rec.Presence, transitioned)
	}
}

func TestPresenceUnauthenticatedProbeRejected(t *testing.T) {
	clock := newFakeClock()
	rec := DeviceRecord{NodeID: "n1", Presence: PresenceReachable}
	now := clock.advance(time.Second)
	updated, transitioned := AdvancePresence(rec, PresenceObservation{OK: false, At: now, Authenticated: false}, now)
	if transitioned || updated.ConsecutiveMisses != 0 {
		t.Fatalf("unauthenticated reply moved state: transitioned=%v misses=%d, want untouched", transitioned, updated.ConsecutiveMisses)
	}
	if updated.Presence != PresenceReachable {
		t.Fatalf("unauthenticated reply changed Presence to %q, want it left at reachable", updated.Presence)
	}
}

func TestResolveHeartbeatTimeoutIsUnknownNotUnavailable(t *testing.T) {
	clock := newFakeClock()
	rec := DeviceRecord{NodeID: "n1", Presence: PresenceReachable, LastSeen: clock.now}
	now := clock.advance(DefaultHeartbeatTimeout + time.Second)
	rec, transitioned := ResolveHeartbeatTimeoutPresence(rec, now, DefaultHeartbeatTimeout)
	if !transitioned || rec.Presence != PresenceUnknown {
		t.Fatalf("heartbeat timeout with no probe evidence: presence=%q transitioned=%v, want unknown/true (never unavailable)", rec.Presence, transitioned)
	}
}

func TestResolveHeartbeatTimeoutNoOpWhenProbeEvidenceExists(t *testing.T) {
	clock := newFakeClock()
	now := clock.advance(time.Hour)
	rec := DeviceRecord{NodeID: "n1", Presence: PresenceReachable, ConsecutiveMisses: 1, FirstMissAt: now.Add(-10 * time.Second)}
	_, transitioned := ResolveHeartbeatTimeoutPresence(rec, now, DefaultHeartbeatTimeout)
	if transitioned {
		t.Fatal("resolved heartbeat-timeout unknown despite live probe-miss evidence in the window")
	}
}

// TestPresenceAffectsNewPlacementsOnly proves R-21.197's scope rule at the
// only place this ticket can prove it structurally: an in-flight job
// model (a plain scope-release slice, since no real reservation/lease/
// worktree scheduler exists in this tree yet — see the ticket journal)
// is untouched by AdvancePresence itself. Only an explicit
// CancelInFlight call, made by the caller (never by a presence
// transition), ever releases anything.
func TestPresenceAffectsNewPlacementsOnly(t *testing.T) {
	released := map[string]bool{}
	inFlight := []ScopeRelease{
		func() error { released["reservation"] = true; return nil },
		func() error { released["lease"] = true; return nil },
		func() error { released["worktree"] = true; return nil },
	}

	clock := newFakeClock()
	rec := DeviceRecord{NodeID: "n1", Presence: PresenceReachable}
	for i := 0; i < ProbeMissThreshold; i++ {
		now := clock.advance(15 * time.Second)
		rec, _ = AdvancePresence(rec, PresenceObservation{OK: false, At: now, Authenticated: true}, now)
	}
	if rec.Presence != PresenceUnavailable {
		t.Fatalf("setup: presence=%q, want unavailable", rec.Presence)
	}
	if len(released) != 0 {
		t.Fatalf("a presence transition alone released scope: %v, want none", released)
	}
	_ = inFlight // only CancelInFlight (below) may consume these
}

func TestCancelReleasesReservationLeaseWorktree(t *testing.T) {
	released := map[string]bool{}
	releases := []ScopeRelease{
		func() error { released["reservation"] = true; return nil },
		func() error { released["lease"] = true; return nil },
		func() error { released["worktree"] = true; return nil },
	}
	if err := CancelInFlight(releases...); err != nil {
		t.Fatalf("CancelInFlight() = %v, want nil", err)
	}
	for _, k := range []string{"reservation", "lease", "worktree"} {
		if !released[k] {
			t.Errorf("release[%q] not called", k)
		}
	}
}

func TestCancelInFlightRunsAllDespiteOneFailure(t *testing.T) {
	var calls []string
	releases := []ScopeRelease{
		func() error { calls = append(calls, "reservation"); return errBoom },
		func() error { calls = append(calls, "lease"); return nil },
		func() error { calls = append(calls, "worktree"); return nil },
	}
	if err := CancelInFlight(releases...); err != errBoom {
		t.Fatalf("CancelInFlight() = %v, want the first error", err)
	}
	if len(calls) != 3 {
		t.Fatalf("calls = %v, want all three releases attempted despite the first failing", calls)
	}
}

var errBoom = cascade.New(cascade.KindInternal, "boom")
