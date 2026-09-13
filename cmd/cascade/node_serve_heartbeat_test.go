// Purpose: unit coverage for startOutboundHeartbeat's documented no-op
//
//	branch: a node that has never enrolled (no ControllerBinding on disk)
//	has nothing to heartbeat to, and the function must return immediately
//	without spawning the background loop -- this file shipped with no
//	direct test of its own.
//
// SPORT: cmd.cascade.node-serve/TEST (node serve heartbeat wiring).
package main

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/secrets"
)

// TestStartOutboundHeartbeat_NoBindingReturnsWithoutBlocking proves the
// documented no-op: a fresh dataDir with no controller_binding.json must
// hit the early `!ok` return, never reach the `go nodes.RunHeartbeatLoop`
// launch. Both branches return control to the caller immediately (the
// loop itself runs in a goroutine), so the observable difference this
// test can assert is that the call never panics building a real
// NodeKeystore/Identity pair and completes well within a generous bound
// -- a hang here would mean the early-return branch stopped short-
// circuiting before the (untestable without a live controller) loop
// setup.
func TestStartOutboundHeartbeat_NoBindingReturnsWithoutBlocking(t *testing.T) {
	dataDir := t.TempDir()
	keystore, err := nodes.NewNodeKeystore(secrets.Config{
		Service: "cascade-node-serve-test", Dir: dataDir, Passphrase: "test-pass", ForceFileVault: true,
	})
	if err != nil {
		t.Fatalf("NewNodeKeystore: %v", err)
	}

	done := make(chan struct{})
	go func() {
		startOutboundHeartbeat(context.Background(), dataDir, nodes.Identity{NodeID: "node-1"}, keystore)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("startOutboundHeartbeat with no controller binding did not return within 2s")
	}
}
