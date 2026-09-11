package notify

import (
	"sync"
	"testing"
)

type stubSubscriber struct {
	id      string
	matches bool
	recv    func(Notification) error
}

func (s stubSubscriber) ID() string                   { return s.id }
func (s stubSubscriber) Match(Notification) bool      { return s.matches }
func (s stubSubscriber) Receive(n Notification) error { return s.recv(n) }

func TestRegistrySubscribeUnsubscribe(t *testing.T) {
	r := NewRegistry()
	sub := stubSubscriber{id: "a", matches: true, recv: func(Notification) error { return nil }}
	r.Subscribe(sub, session("a"))
	if r.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", r.Len())
	}
	r.Unsubscribe("a")
	if r.Len() != 0 {
		t.Fatalf("Len() after Unsubscribe = %d, want 0", r.Len())
	}
	// Unsubscribing an unknown id is a no-op, not a panic or error.
	r.Unsubscribe("never-registered")
}

func TestRegistrySubscribeReplaces(t *testing.T) {
	r := NewRegistry()
	r.Subscribe(stubSubscriber{id: "a", matches: false, recv: func(Notification) error { return nil }}, session("a"))
	r.Subscribe(stubSubscriber{id: "a", matches: true, recv: func(Notification) error { return nil }}, session("a"))
	if r.Len() != 1 {
		t.Fatalf("Len() = %d, want 1 (re-subscribe replaces)", r.Len())
	}
	snap := r.Snapshot()
	if !snap[0].Sub.Match(Notification{}) {
		t.Fatal("expected the second Subscribe call to win")
	}
}

func TestRegistrySnapshotIsIndependent(t *testing.T) {
	r := NewRegistry()
	r.Subscribe(stubSubscriber{id: "a", matches: true, recv: func(Notification) error { return nil }}, session("a"))
	snap := r.Snapshot()
	r.Subscribe(stubSubscriber{id: "b", matches: true, recv: func(Notification) error { return nil }}, session("b"))
	if len(snap) != 1 {
		t.Fatalf("mutating the registry after Snapshot changed the snapshot's length: %d", len(snap))
	}
}

func TestRegistryConcurrentAccess(_ *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := string(rune('a' + n%26))
			r.Subscribe(stubSubscriber{id: id, matches: true, recv: func(Notification) error { return nil }}, session(id))
			_ = r.Snapshot()
			r.Unsubscribe(id)
		}(i)
	}
	wg.Wait()
}
