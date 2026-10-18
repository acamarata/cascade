package plugins

// Purpose: the host bridge binding's turn-origin stamp, split out of
//   cascadepa_bridge_chat_wiring_test.go (at the 300-line file cap).
// SPORT: internal/plugins:cascadepa-bridge-chat-wiring (TEST: origin stamp).

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestBridgeChatService_StampsUntrustedOrigin is the real counterpart for
// the host stamp: bridgeChatService.AppendTurn over the real chat adapter
// leaves a stored turn marked bridge-telegram / untrusted-source, a
// constant of the binding (the caller passes only thread, role and text).
func TestBridgeChatService_StampsUntrustedOrigin(t *testing.T) {
	r := newMapRig(t)
	c, ctx := bridgeChatService{doer: r.daemon}, context.Background()
	thread, err := c.CreateThread(ctx, "restricted")
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := c.AppendTurn(ctx, thread, "user", "hello from the bridge"); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	turns, err := r.store.ListTurns(ctx, thread)
	if err != nil || len(turns) != 1 {
		t.Fatalf("ListTurns = %v, %v; want exactly one turn", turns, err)
	}
	if got := turns[0]; got.Origin != conversation.OriginBridgeTelegram || got.Provenance != provider.ProvenanceUntrustedSource {
		t.Fatalf("stored marker = %q/%q, want bridge-telegram/untrusted-source", got.Origin, got.Provenance)
	}
}
