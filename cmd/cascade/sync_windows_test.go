//go:build windows

// Purpose (this file): proves on the Windows lane ITSELF (R-14.131: a
//
//	platform-specific behavior needs a test that RUNS on that platform,
//	not merely one that builds there) that `sync conflicts resolve --keep
//	local` refuses with the tier-2 refusal when nothing injects a GOOS —
//	which is the only configuration a real Windows operator has.
//
// SPORT: cmd/cascade sync windows (ADD tests) — P1-E17-W4-S38-T3.
package main

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestResolveKeepLocalRefusesOnTheRealWindowsLane leaves syncDeps.GOOS
// empty so the refusal is decided by this build's own runtime.GOOS.
func TestResolveKeepLocalRefusesOnTheRealWindowsLane(t *testing.T) {
	gate := &countingGate{}
	_, err := runSyncCmd(t, newSyncConflictsResolveCmd(testSyncDeps(gate)), "cfg-3", "--keep", "local")
	if !isCLIKind(err, cascade.KindUnsupported) {
		t.Fatalf("resolve --keep local on the real Windows lane = %v, want the tier-2 refusal", err)
	}
	if len(gate.verbs) != 0 {
		t.Errorf("the tier-2 refusal still consulted the elevation gate: %v", gate.verbs)
	}
}

// TestResolveKeepServerIsAllowedOnTheRealWindowsLane proves the platform
// rule did not lock a Windows operator out of closing a conflict at all.
func TestResolveKeepServerIsAllowedOnTheRealWindowsLane(t *testing.T) {
	if _, err := runSyncCmd(t, newSyncConflictsResolveCmd(testSyncDeps(&countingGate{})), "cfg-3", "--keep", "server"); err != nil {
		t.Fatalf("resolve --keep server on Windows = %v, want it allowed", err)
	}
}
