//go:build !windows

package main

import (
	"testing"

	"github.com/acamarata/cascade/internal/nodes"
)

// TestNodePlacementOptionReadsTheLiveTunnelManager proves the composition
// root hands the placement engine the REAL connection source rather than a
// convenient constant. The manager starts with no tunnel registered, so an
// honest lookup must report down; a lookup that answered "up" would make
// every node look placeable to a controller connected to none of them.
func TestNodePlacementOptionReadsTheLiveTunnelManager(t *testing.T) {
	manager := nodes.NewManager()
	opt := withNodePlacement(manager)
	if opt.nodeTunnels == nil {
		t.Fatal("a real tunnel manager produced no lookup")
	}
	if state := opt.nodeTunnels("never-registered"); state != nodes.TunnelDown {
		t.Fatalf("an unregistered node reported tunnel state %v, want TunnelDown", state)
	}
	if opt.register != nil {
		t.Fatal("the placement option registered an RPC method; it contributes a collaborator only")
	}
}

// TestNodePlacementOptionWithNoManagerWiresNothing pins the nil case. A
// caller with no tunnel manager must leave the lookup absent, which the
// placement engine reads as "no connection source wired" and reports in
// those words — distinguishable from every node merely being disconnected.
func TestNodePlacementOptionWithNoManagerWiresNothing(t *testing.T) {
	if opt := withNodePlacement(nil); opt.nodeTunnels != nil {
		t.Fatal("a nil tunnel manager produced a lookup anyway")
	}
}

// TestNodeTunnelLookupFindsThePlacementOption proves buildRPCServer can
// actually extract the collaborator from a mixed option list — the step
// between "the option exists" and "the conductor wiring receives it".
func TestNodeTunnelLookupFindsThePlacementOption(t *testing.T) {
	if got := nodeTunnelLookup(nil); got != nil {
		t.Fatal("an empty option list produced a lookup")
	}
	if got := nodeTunnelLookup([]rpcServerOption{withPolicyHandlers(nil)}); got != nil {
		t.Fatal("an unrelated option produced a tunnel lookup")
	}
	opts := []rpcServerOption{withPolicyHandlers(nil), withNodePlacement(nodes.NewManager())}
	got := nodeTunnelLookup(opts)
	if got == nil {
		t.Fatal("the placement option was not found in the option list")
	}
	if state := got("never-registered"); state != nodes.TunnelDown {
		t.Fatalf("the extracted lookup reported %v, want TunnelDown", state)
	}
}
