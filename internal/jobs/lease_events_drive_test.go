package jobs

// Purpose: closes the coverage gap on lease_events.go's renewed/
//   released/reclaimed/contended fan-outs by driving REAL lease
//   transitions (Acquire/Renew/Release/SweepExpired/Reclaim) through a
//   LeaseManager wired to a REAL leaseEventSink -- never calling the
//   sink's methods directly (lease_events_test.go already covers
//   acquired/expired/fenced that way; these four were still at 0%).
// Inputs: nothing external -- each test builds its own store/clock/sink.
// Outputs: n/a (test file).
// Constraints: every receive on sub.Events is bounded (2s); every
//   elapsed-time transition drives *runtime.FixedClock.Advance, never a
//   bare time.Now/time.Sleep.
// SPORT: jobs/lease-model (FIX, coverage floor restoration).

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/runtime"
)

// drainEvent asserts the next bus event within 2s carries want's Kind.
func drainEvent(t *testing.T, sub *events.Subscription, want events.EventKind) {
	t.Helper()
	select {
	case ev := <-sub.Events:
		if ev.Kind != want {
			t.Fatalf("event kind = %v, want %v", ev.Kind, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for a %v event (bounded receive)", want)
	}
}

// newDrivenLeaseManager builds a LeaseManager wired to a REAL sink (bus,
// journal, attention -- newTestSink's counterparts), for tests that
// drive a genuine lease transition and assert on what it actually
// emits, rather than calling the sink's methods directly.
func newDrivenLeaseManager(t *testing.T) (*LeaseManager, *events.Bus, journal.Store) {
	t.Helper()
	sink, bus, j, _ := newTestSink(t)
	store := newTestStore(t)
	clock := runtime.NewFixedClock(time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC))
	return NewLeaseManager(store, clock, alwaysController, DefaultLeaseDefaults(), sink), bus, j
}

// TestLeaseEventsRenewedJournaledAndPublished drives a real Acquire
// then Renew and asserts the renewed path's real journal entry
// (KindCheckpoint/"renewed") and real bus event.
func TestLeaseEventsRenewedJournaledAndPublished(t *testing.T) {
	m, bus, j := newDrivenLeaseManager(t)
	ctx := context.Background()

	sub, err := bus.Subscribe(ctx, leaseEventNamespace, "test-cursor", 8)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	granted, err := m.Acquire(ctx, "repo-1", "internal/jobs/**", "job-a")
	if err != nil || !granted.Granted {
		t.Fatalf("Acquire: %+v, %v", granted, err)
	}
	drainEvent(t, sub, EventLeaseAcquired)

	renewed, err := m.Renew(ctx, "repo-1", granted.Lease.ScopeGlob, granted.Lease.Epoch)
	if err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if renewed.RenewCount != 1 {
		t.Fatalf("RenewCount after Renew = %d, want 1", renewed.RenewCount)
	}
	drainEvent(t, sub, EventLeaseRenewed)

	entries, err := j.Replay(ctx, leaseEntityID("repo-1", granted.Lease.ScopeGlob), journal.Cursor{}, nil)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	var sawRenewed bool
	for _, e := range entries {
		if e.Kind == journal.KindCheckpoint && e.OperationID == "renewed" {
			sawRenewed = true
		}
	}
	if !sawRenewed {
		t.Fatalf("journal entries = %+v, want a KindCheckpoint \"renewed\" entry", entries)
	}
}

// TestLeaseEventsReleasedJournaledAndPublished drives a real Acquire
// then Release and asserts the released path's real journal entry
// (KindAck/"released") and real bus event.
func TestLeaseEventsReleasedJournaledAndPublished(t *testing.T) {
	m, bus, j := newDrivenLeaseManager(t)
	ctx := context.Background()

	sub, err := bus.Subscribe(ctx, leaseEventNamespace, "test-cursor", 8)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	granted, err := m.Acquire(ctx, "repo-1", "internal/jobs/**", "job-a")
	if err != nil || !granted.Granted {
		t.Fatalf("Acquire: %+v, %v", granted, err)
	}
	drainEvent(t, sub, EventLeaseAcquired)

	if err := m.Release(ctx, "repo-1", granted.Lease.ScopeGlob, granted.Lease.Epoch); err != nil {
		t.Fatalf("Release: %v", err)
	}
	drainEvent(t, sub, EventLeaseReleased)

	entries, err := j.Replay(ctx, leaseEntityID("repo-1", granted.Lease.ScopeGlob), journal.Cursor{}, nil)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	var sawReleased bool
	for _, e := range entries {
		if e.Kind == journal.KindAck && e.OperationID == "released" {
			sawReleased = true
		}
	}
	if !sawReleased {
		t.Fatalf("journal entries = %+v, want a KindAck \"released\" entry", entries)
	}
}

// TestLeaseEventsContendedJournaledAndPublished constructs a REAL
// conflict (job-b's Acquire against job-a's intersecting scope) and
// asserts the contended path journals under the WINNING lease's entity
// id (job-a's), naming both holders, and publishes on the bus.
func TestLeaseEventsContendedJournaledAndPublished(t *testing.T) {
	m, bus, j := newDrivenLeaseManager(t)
	ctx := context.Background()

	sub, err := bus.Subscribe(ctx, leaseEventNamespace, "test-cursor", 8)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	first, err := m.Acquire(ctx, "repo-1", "internal/jobs/**", "job-a")
	if err != nil || !first.Granted {
		t.Fatalf("first Acquire: %+v, %v", first, err)
	}
	drainEvent(t, sub, EventLeaseAcquired)

	second, err := m.Acquire(ctx, "repo-1", "internal/jobs/lease.go", "job-b")
	if err != nil {
		t.Fatalf("second Acquire: %v", err)
	}
	if second.Granted {
		t.Fatalf("second Acquire on an intersecting scope was granted, want Contended")
	}
	drainEvent(t, sub, EventLeaseContended)

	entries, err := j.Replay(ctx, leaseEntityID("repo-1", first.Lease.ScopeGlob), journal.Cursor{}, nil)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	var payload map[string]string
	var sawContended bool
	for _, e := range entries {
		if e.Kind == journal.KindEscalation && e.OperationID == "contended" {
			sawContended = true
			if err := json.Unmarshal(e.Payload, &payload); err != nil {
				t.Fatalf("unmarshal contended payload: %v", err)
			}
		}
	}
	if !sawContended {
		t.Fatalf("journal entries = %+v, want a KindEscalation \"contended\" entry under the winner's entity id", entries)
	}
	if payload["requesting_holder"] != "job-b" || payload["conflicting_holder"] != "job-a" {
		t.Fatalf("contended payload = %+v, want requesting_holder=job-b conflicting_holder=job-a", payload)
	}
}

// TestLeaseEventsReclaimedJournaled drives a real Acquire, SweepExpired
// (dead-holder expiry) and Reclaim, and asserts the reclaimed path's
// real journal entry (KindAck/"reclaimed"). reclaimed never publishes
// to the bus (lease_events.go's own reclaimed method), so this test
// only asserts the journal side -- asserting a bus event here would be
// asserting behavior the function does not have.
func TestLeaseEventsReclaimedJournaled(t *testing.T) {
	sink, _, j, _ := newTestSink(t)
	store := newTestStore(t)
	clock := runtime.NewFixedClock(time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC))
	m := NewLeaseManager(store, clock, alwaysController, DefaultLeaseDefaults(), sink)
	ctx := context.Background()

	granted, err := m.Acquire(ctx, "repo-1", "internal/jobs/**", "job-a")
	if err != nil || !granted.Granted {
		t.Fatalf("Acquire: %+v, %v", granted, err)
	}
	if err := store.PutJob(ctx, baseJob("job-a")); err != nil {
		t.Fatalf("PutJob: %v", err)
	}
	if err := store.PutExecution(ctx, Execution{ID: "exec-1", JobID: "job-a", Attempt: 1, State: ExecutionRunning, PGID: 4242}); err != nil {
		t.Fatalf("PutExecution: %v", err)
	}
	deadline := granted.Lease.IssuedAt + granted.Lease.TTLSeconds + m.defaults.ExpiryGraceSeconds
	clock.Advance(secondsUntil(clock, deadline+1))
	if _, err := m.SweepExpired(ctx); err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}

	reclaimed, err := m.Reclaim(ctx, "repo-1", granted.Lease.ScopeGlob, fakeLivenessProbe{alive: false})
	if err != nil {
		t.Fatalf("Reclaim: %v", err)
	}
	if reclaimed.State != LeaseExpiredOrphaned {
		t.Fatalf("Reclaim with a dead pgid = state %v, want expired_orphaned", reclaimed.State)
	}

	entries, err := j.Replay(ctx, leaseEntityID("repo-1", granted.Lease.ScopeGlob), journal.Cursor{}, nil)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	var sawReclaimed bool
	for _, e := range entries {
		if e.Kind == journal.KindAck && e.OperationID == "reclaimed" {
			sawReclaimed = true
		}
	}
	if !sawReclaimed {
		t.Fatalf("journal entries = %+v, want a KindAck \"reclaimed\" entry", entries)
	}
}
