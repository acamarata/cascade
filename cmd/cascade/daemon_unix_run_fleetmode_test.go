//go:build !windows

// Purpose: proves wireFleetAndNodeHandlers -- buildRPCServer's real
//
//	composition-root call site -- actually registers fleet.mode.show/set
//	(P1-E41-W9-S79-T2), closing the same class of gap R-14.223 named for
//	fleet.journal_show. Unlike the quota/capacity precedent's
//	integration-tagged real-socket test, this exercises
//	wireFleetAndNodeHandlers directly: it operates on a plain in-memory
//	*rpc.Registry with no network, so it runs in the default unit lane
//	(the `net`-import ban only applies to a real socket, which this test
//	never opens) while still driving the REAL production call site rather
//	than a bare RegisterFleetModeHandler call.
//
// SPORT: cmd/cascade/fleet-mode-cli (ADD, P1-E41-W9-S79-T2).
package main

import (
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

type fixedFleetModeWireClock struct{ t time.Time }

func (c fixedFleetModeWireClock) Now() time.Time { return c.t }

// TestWireFleetAndNodeHandlers_RegistersFleetModeMethods is this ticket's
// mutation proof: removing
// `daemon.RegisterFleetModeHandler(registry, paths, clock)` from
// wireFleetAndNodeHandlers (daemon_unix_run_fleetjobs.go) makes this test
// fail with "registry.Registered(fleet.mode.show) = false", and restoring
// it makes the test pass again -- recorded with real RED/GREEN output in
// this ticket's journal.
func TestWireFleetAndNodeHandlers_RegistersFleetModeMethods(t *testing.T) {
	registry := rpc.NewRegistry()
	clock := fixedFleetModeWireClock{t: time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)}
	bus := events.New(storetest.NewMemStore(), runtime.NewSystemClock())
	paths := fakeMemoryPaths{root: t.TempDir()}

	if err := wireFleetAndNodeHandlers(registry, storetest.NewMemStore(), clock, bus, paths, daemon.Settings{}); err != nil {
		t.Fatalf("wireFleetAndNodeHandlers: %v", err)
	}
	if !registry.Registered(economics.MethodFleetModeShow) {
		t.Errorf("registry.Registered(%q) = false, want true", economics.MethodFleetModeShow)
	}
	if !registry.Registered(economics.MethodFleetModeSet) {
		t.Errorf("registry.Registered(%q) = false, want true", economics.MethodFleetModeSet)
	}
}
