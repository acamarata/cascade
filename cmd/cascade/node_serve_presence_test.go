// Purpose: proves node_serve.go's presence-subsystem wiring
//
//	(startPresenceSubsystems, called from startNodeServeBackgroundLoops)
//	is REAL per R-16.80 Ruling 1 — a production code path actually
//	calls internal/nodes.NewProber/NewNetworkWatcher, not merely a unit
//	test invoking them directly.
//
// SPORT: cmd/cascade/node (ADD — R-16.80-class wiring fix for
//
//	internal/nodes.NewProber/NewNetworkWatcher, UNOWNED in
//	testonly-allow.json before this file).
package main

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/nodes"
)

// fakeProbeTicker is nodeServeDeps.ProbeTicker's test double: a buffered
// channel the test sends on directly, exactly like nodes.NewSystemTicker's
// own production channel, so a tick sent before Prober.Run reaches its
// select is never lost.
type fakeProbeTicker struct {
	ch      chan struct{}
	stopped chan struct{}
}

func newFakeProbeTicker() *fakeProbeTicker {
	return &fakeProbeTicker{ch: make(chan struct{}, 1), stopped: make(chan struct{}, 1)}
}
func (f *fakeProbeTicker) C() <-chan struct{} { return f.ch }
func (f *fakeProbeTicker) Stop() {
	select {
	case f.stopped <- struct{}{}:
	default:
	}
}

// TestNodeServeWiresRealPresenceProbeLoop drives runNodeServe (the real
// cobra RunE entry point's target, cmd/cascade/node_serve.go's
// composition root) end to end: it enrolls one device record with no
// heartbeat evidence at all, fires one injected probe tick, and asserts
// the SAME RecordStore the RPC registry reads is updated by the real
// Prober loop — never a synthetic call to nodes.NewProber from the test
// itself.
//
// MUTATION PROOF (R-16.80 Ruling 3, run manually this session and
// recorded in the ticket journal): commenting out
// startNodeServeBackgroundLoops' call to startPresenceSubsystems makes
// this test fail with "presence never transitioned" — restoring it
// passes again. Quoted RED/GREEN output is in the journal.
func TestNodeServeWiresRealPresenceProbeLoop(t *testing.T) {
	deps := testNodeServeDeps(t)
	ticker := newFakeProbeTicker()
	deps.ProbeTicker = ticker

	dataDir := deps.Paths.DataDir()
	store := nodes.NewRecordStore(nodes.NewFileRecordBackend(dataDir), deps.Clock)
	peer, _, err := nodes.GenerateIdentity(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	if _, err := store.Enroll(peer, nodes.TierWorkerTrusted); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	before, err := store.Get(peer.NodeID)
	if err != nil {
		t.Fatalf("Get (before): %v", err)
	}
	if before.Presence == nodes.PresenceUnknown {
		t.Fatal("precondition: a freshly-enrolled record must not already be PresenceUnknown")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- runNodeServe(ctx, deps) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(5 * time.Second):
			t.Error("runNodeServe did not drain on cleanup")
		}
	})

	// The tick channel is buffered (cap 1): this send is never lost even
	// if Prober.Run has not yet reached its select loop.
	ticker.ch <- struct{}{}

	deadline := time.After(5 * time.Second)
	for {
		rec, err := store.Get(peer.NodeID)
		if err != nil {
			t.Fatalf("Get (poll): %v", err)
		}
		if rec.Presence == nodes.PresenceUnknown {
			return // the real Prober loop, driven from runNodeServe, made the transition
		}
		select {
		case <-deadline:
			t.Fatalf("presence never transitioned to unknown; last observed = %q", rec.Presence)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
