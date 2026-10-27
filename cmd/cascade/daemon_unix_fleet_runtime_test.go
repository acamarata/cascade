//go:build !windows

// Purpose: the fleet runtime's composition: every registration of a
//
//	production composition reads the one daemon.FleetRuntime from w.Deps, a
//	malformed [governor] table stops composeDaemon, and shutdown joins the
//	sampler goroutine and closes the runtime before the store closes.
//
// Constraints: the goroutine-dump test re-runs itself in a child process
//
//	(inDaemonChild) so no other test's goroutines are present. The system
//	ticker drives the real sampler, so the one wait for a first sample polls
//	on a short timer; a longer timer only fails a hung test.
//
// SPORT: cmd/cascade/daemon (ADD, fleet runtime composition tests).
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/governor"
)

// compositionPhases are the phases a production registration may run in.
var compositionPhases = []daemonPhase{
	phaseMiddleware, phaseCore, phaseNamespaces, phaseConductor, phaseSupervised,
	phaseLate, phaseOptions, phaseFleet, phaseEventTopics,
	phaseMCPLast - 1, // phaseMCPLast itself belongs to the one MCP registration
}

// TestCompositionFleetRuntimePresent: a registration in every phase sees the
// same non-nil Deps.Fleet, with its admission controller built.
func TestCompositionFleetRuntimePresent(t *testing.T) {
	var mu sync.Mutex
	seen := map[daemonPhase]*daemon.FleetRuntime{}
	for _, phase := range compositionPhases {
		addTestRegistration(t, daemonRegistration{
			Name: fmt.Sprintf("test-fleet-present-%d", phase), Phase: phase, Order: 999,
			Wire: func(w *daemonWiring) error {
				mu.Lock()
				seen[phase] = w.Deps.Fleet
				mu.Unlock()
				return nil
			},
		})
	}
	t.Setenv("HOME", t.TempDir())
	w, cancel, done := startTestDaemon(t, newRunTestDeps(t, nil), observeWiring(t))
	cancel()
	awaitDaemonReturn(t, done)
	if w.Deps.Fleet == nil || w.Deps.Fleet.Admission() == nil {
		t.Fatalf("Deps.Fleet = %v, want a runtime with an admission controller", w.Deps.Fleet)
	}
	for _, phase := range compositionPhases {
		if got := seen[phase]; got == nil || got != w.Deps.Fleet {
			t.Errorf("phase %d saw Fleet %p, want the one runtime %p", phase, got, w.Deps.Fleet)
		}
	}
}

// TestComposeDaemonRefusesMalformedGovernor: a [governor] table governor.Load
// cannot decode stops composeDaemon with the load error (C9), and the
// cleanups it returns run without leaving the sampler behind.
func TestComposeDaemonRefusesMalformedGovernor(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	deps := newRunTestDeps(t, nil)
	if err := os.WriteFile(deps.Paths.ConfigPath(), []byte("[governor]\npreset = 5\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, cleanups, err := composeDaemon(context.Background(), deps, nil)
	runCleanupsLIFO(cleanups)
	if err == nil || !strings.Contains(err.Error(), `step "admission"`) {
		t.Fatalf("composeDaemon = %v, want the admission step's governor load error", err)
	}
}

// awaitFreshAdmissionRow waits, bounded, until the admission row reports a
// memory posture, that is, the real sampler completed a tick.
func awaitFreshAdmissionRow(t *testing.T, m *daemon.Manifest) {
	t.Helper()
	guard := time.NewTimer(testDaemonBound)
	defer guard.Stop()
	for {
		for _, s := range m.Snapshot() {
			if s.Name == "admission" && s.Detail != "" && s.Detail != "posture=stale" {
				return
			}
		}
		select {
		case <-guard.C:
			t.Fatal("the admission row never left posture=stale: the sampler never completed a tick")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TestDaemonShutdownJoinsSampler: after shutdown the supervised sampler row
// is stopped, no governor goroutine is left, and the runtime was closed (the
// controller refuses with draining, not with a signal refusal).
func TestDaemonShutdownJoinsSampler(t *testing.T) {
	if !inDaemonChild(t) {
		return
	}
	t.Setenv("HOME", t.TempDir())
	w, cancel, done := startTestDaemon(t, newRunTestDeps(t, nil), observeWiring(t))
	awaitFreshAdmissionRow(t, w.Manifest)
	cancel()
	awaitDaemonReturn(t, done)
	found := map[string]daemon.SubsystemStatus{}
	for _, s := range w.Manifest.Snapshot() {
		found[s.Name] = s
	}
	for _, name := range []string{"governor-sampler", "admission-posture"} {
		if s, ok := found[name]; !ok || s.State != daemon.SubsystemSkipped || s.Detail != "stopped" {
			t.Errorf("%s = %+v (present %v) after the run, want skipped/stopped", name, s, ok)
		}
	}
	for _, g := range cascadeGoroutines() {
		if strings.Contains(g, "internal/fleet/governor.") || strings.Contains(g, "internal/runtime.(*systemTicker)") {
			t.Errorf("a sampler goroutine outlived the daemon: %s", g)
		}
	}
	// A cancelled context keeps an unclosed controller from queueing forever.
	gone, stop := context.WithCancel(context.Background())
	stop()
	permit, err := w.Deps.Fleet.Admission().Admit(gone, governor.AdmissionRequest{Kind: "generic"})
	permit.Release()
	if !governor.IsDraining(err) {
		t.Errorf("Admit after shutdown = %v, want draining (fleet runtime closed before the store)", err)
	}
}

// TestShutdownOrdersJoinCloseStore runs the cleanups composeDaemon returns one
// at a time, newest first, and checks the order the daemon documents: the
// join (the sampler row is stopped) comes before the fleet runtime closes,
// and the runtime closes before the store does.
func TestShutdownOrdersJoinCloseStore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seen := observeWiring(t)
	_, cleanups, err := composeDaemon(context.Background(), newRunTestDeps(t, nil), nil)
	if err != nil {
		runCleanupsLIFO(cleanups)
		t.Fatalf("composeDaemon: %v", err)
	}
	w := <-seen
	awaitFreshAdmissionRow(t, w.Manifest)
	gone, stop := context.WithCancel(context.Background())
	stop()
	fleetClosed := func() bool {
		permit, err := w.Deps.Fleet.Admission().Admit(gone, governor.AdmissionRequest{Kind: "generic"})
		permit.Release()
		return governor.IsDraining(err)
	}
	storeClosed := func() bool { return w.Store.Put(context.Background(), "test-shutdown-order", "k", []byte("v")) != nil }
	samplerStopped := func() bool {
		for _, s := range w.Manifest.Snapshot() {
			if s.Name == "governor-sampler" {
				return s.State == daemon.SubsystemSkipped
			}
		}
		return false
	}
	fleetAt, storeAt := -1, -1
	for i := len(cleanups) - 1; i >= 0; i-- {
		// Read the join state BEFORE the cleanup that may close the runtime:
		// Close itself ends the sampler, so only a pre-call read proves the join came first.
		joinedBefore := samplerStopped()
		cleanups[i]()
		if fleetAt < 0 && fleetClosed() {
			fleetAt = len(cleanups) - 1 - i
			if !joinedBefore {
				t.Error("the fleet runtime closed before the sampler goroutine was joined")
			}
		}
		if storeAt < 0 && storeClosed() {
			storeAt = len(cleanups) - 1 - i
		}
	}
	if fleetAt < 0 || storeAt < 0 || fleetAt >= storeAt {
		t.Fatalf("cleanup step that closed the fleet runtime = %d, the store = %d; want both observed, the fleet first", fleetAt, storeAt)
	}
}
