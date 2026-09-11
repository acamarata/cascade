package supervision

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// TestSubscriptionPushesOnBlockedEvent fires a real SessionRecord-shaped
// fleet.sessions.changed event on a real *events.Bus and asserts the
// queue gains exactly one item within one delivery cycle. Never an
// unguarded <-ch: every receive below has a timeout.
func TestSubscriptionPushesOnBlockedEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bus := events.New(storetest.NewMemStore(), runtime.NewFixedClock(time.Now()))
	store := NewStore(storetest.NewMemStore(), runtime.NewFixedClock(time.Now()), nil, sequentialIDGenerator(), 0)
	sub := NewSubscription(store)

	done := make(chan error, 1)
	go func() { done <- sub.Run(ctx, bus) }()
	waitUntil(t, 2*time.Second, sub.Alive)

	rec := sessions.SessionRecord{SessionID: "sess-blocked-1", State: "blocked"}
	payload, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal SessionRecord: %v", err)
	}
	if _, err := bus.Publish(ctx, "fleet.sessions", events.EventKind("fleet.sessions.changed"), "sessions:sess-blocked-1", payload); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	waitUntil(t, 2*time.Second, func() bool {
		items, err := store.ListInScopes(ctx, []ScopeRef{sessionScope("sess-blocked-1")}, Filter{})
		return err == nil && len(items) == 1
	})

	items, err := store.ListInScopes(ctx, []ScopeRef{sessionScope("sess-blocked-1")}, Filter{})
	if err != nil {
		t.Fatalf("ListInScopes: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if items[0].Kind != KindPolicyAsk {
		t.Errorf("items[0].Kind = %s, want KindPolicyAsk for a blocked transition", items[0].Kind)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v after cancel, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of ctx cancellation")
	}
}

// TestSubscriptionIgnoresNonAttentionState asserts an "active" state
// transition does not push anything.
func TestSubscriptionIgnoresNonAttentionState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bus := events.New(storetest.NewMemStore(), runtime.NewFixedClock(time.Now()))
	store := NewStore(storetest.NewMemStore(), runtime.NewFixedClock(time.Now()), nil, sequentialIDGenerator(), 0)
	sub := NewSubscription(store)

	go func() { _ = sub.Run(ctx, bus) }()
	waitUntil(t, 2*time.Second, sub.Alive)

	rec := sessions.SessionRecord{SessionID: "sess-active-1", State: "active"}
	payload, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal SessionRecord: %v", err)
	}
	if _, err := bus.Publish(ctx, "fleet.sessions", events.EventKind("fleet.sessions.changed"), "sessions:sess-active-1", payload); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Give the (non-)delivery a moment, then assert nothing landed.
	time.Sleep(100 * time.Millisecond)
	items, err := store.ListInScopes(ctx, []ScopeRef{sessionScope("sess-active-1")}, Filter{})
	if err != nil {
		t.Fatalf("ListInScopes: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("len(items) = %d, want 0 for a non-attention state", len(items))
	}
}

// waitUntil polls cond until it reports true or timeout elapses, failing
// the test on timeout. Bounded, never an unguarded blocking receive.
func waitUntil(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}
