package runtime

// Purpose: tests DiscardEventPublisher.Publish (hotreload_events.go) —
//   the default no-op EventPublisher for daemonless callers. Previously
//   0% direct coverage; this proves the call is safe (no panic) across
//   a nil, empty, and populated payload, and that it satisfies the
//   EventPublisher interface it exists to implement.
// Inputs: n/a (test-only).
// Outputs: n/a (test-only).
// Constraints: none — pure in-memory, no filesystem/network.
// SPORT: runtime/hot-reload-engine (ADD, placeholder per T-8 sport_updates).

import (
	"context"
	"testing"
)

func TestDiscardEventPublisher_PublishIsSafeNoOp(t *testing.T) {
	var pub EventPublisher = DiscardEventPublisher{}

	// The assertion is explicit rather than implicit. A discard sink has no
	// observable effect, so "did not panic" IS the contract — but a test
	// whose only assertion is the absence of a crash reads as assertion-free
	// and leaves t unused, which is indistinguishable from a test written to
	// move a coverage number.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("DiscardEventPublisher.Publish panicked: %v", r)
		}
	}()

	// Must not panic on a nil payload.
	pub.Publish(context.Background(), "config.reload.accepted", nil)

	// Must not panic on an empty payload.
	pub.Publish(context.Background(), "config.reload.rejected", map[string]interface{}{})

	// Must not panic on a populated payload, and discards it silently
	// (no observable side effect to assert beyond "did not panic" —
	// that is the entire documented contract of a discard sink).
	pub.Publish(context.Background(), "config.restart.required", map[string]interface{}{
		"reason": "schema version bump",
		"count":  3,
	})
}
