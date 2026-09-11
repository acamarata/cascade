//go:build !windows

// Purpose: unit tests for `cascade fleet bench`'s refusal/dispatch logic,
//
//	mirroring fleet_coverage_test.go's exact hermetic-dial technique: the
//	REAL production DialContext closure against a socket path nothing
//	listens on (a local ENOENT syscall, not network I/O), so this file
//	stays in the no-network unit lane (Art.7.2) while still exercising
//	runFleetBenchLane's real body. This file carries the !windows tag
//	(matching fleet_coverage_test.go) since fakeDaemonPaths/the hermetic
//	unix-socket dial technique is unix-only; errBenchWindowsTier2's own
//	content is asserted below without needing to run on Windows.
//
// SPORT: cmd/cascade/fleet (ADD, per T-5 sport_updates; bench half).
package main

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
)

// TestFleetBenchMountedOnRoot proves `fleet bench` resolves under the
// real root command tree, mirroring TestFleetSessionsMountedOnRoot.
func TestFleetBenchMountedOnRoot(t *testing.T) {
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	cmd, _, err := root.Find([]string{"fleet", "bench"})
	if err != nil {
		t.Fatalf("Find(fleet bench): %v", err)
	}
	if cmd.Use != "bench <lane-id>" {
		t.Fatalf("resolved command Use = %q, want the bench command", cmd.Use)
	}
}

// TestRunFleetBenchLane_NoProbeState proves bench refuses with
// errBenchNoDaemon when the root's daemonless probe never ran, never
// attempting to dial.
func TestRunFleetBenchLane_NoProbeState(t *testing.T) {
	_, err := runFleetBenchLane(context.Background(), fleetSessionsDeps{}, "lane-anthropic-1", 1, 1)
	if err == nil || !strings.Contains(err.Error(), "no daemon socket reachable") {
		t.Fatalf("runFleetBenchLane = %v, want errBenchNoDaemon", err)
	}
}

// TestRunFleetBenchLane_EmbeddedState proves bench refuses the same way
// when the probe confirmed embedded mode: bench has no local/embedded
// fallback (see fleet_bench.go's CONTRACT DEVIATION note), unlike `fleet
// sessions`.
func TestRunFleetBenchLane_EmbeddedState(t *testing.T) {
	ctx := runtime.WithDaemonlessState(context.Background(), runtime.DaemonlessState{Embedded: true})
	_, err := runFleetBenchLane(ctx, fleetSessionsDeps{}, "lane-anthropic-1", 1, 1)
	if err == nil || !strings.Contains(err.Error(), "no daemon socket reachable") {
		t.Fatalf("runFleetBenchLane = %v, want errBenchNoDaemon", err)
	}
}

// TestRunFleetBenchLane_DialFails proves that once the probe confirms a
// live daemon, bench proceeds past both refusal checks into a real
// fleet.bench_lane dial, surfacing its real failure against a socket
// nothing listens on -- never a panic.
func TestRunFleetBenchLane_DialFails(t *testing.T) {
	deps := newHermeticFleetDeps(t)
	ctx := runtime.WithDaemonlessState(context.Background(), runtime.DaemonlessState{Embedded: false})
	_, err := runFleetBenchLane(ctx, deps, "lane-anthropic-1", 2, 2)
	if err == nil {
		t.Fatal("runFleetBenchLane: expected a dial error against a socket nothing listens on")
	}
}

// TestFleetBenchErrors_AreActionable proves both refusals name a
// concrete next step, mirroring TestFleetSessionsErrors_AreActionable.
func TestFleetBenchErrors_AreActionable(t *testing.T) {
	if !strings.Contains(errBenchNoDaemon.Error(), "cascade daemon run") {
		t.Errorf("errBenchNoDaemon does not suggest starting the daemon: %v", errBenchNoDaemon)
	}
	if !strings.Contains(errBenchWindowsTier2.Error(), "daemon") {
		t.Errorf("errBenchWindowsTier2 does not explain the missing daemon: %v", errBenchWindowsTier2)
	}
}
