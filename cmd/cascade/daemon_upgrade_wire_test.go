//go:build !windows

// Purpose: the upgrade hand-off is wired on every unix build whose
//
//	executable can be hashed. The build identity is the content digest of
//	the running file, so a source build with no linker flags (this test
//	binary) gets a real UpgradeManager through composeDaemon, the
//	production composition path, not through a direct wireUpgrade call.
//
// SPORT: cmd/cascade/daemon (upgrade wiring).
package main

import (
	"testing"

	"github.com/acamarata/cascade/internal/daemon"
)

// TestWireUpgradeAlwaysWires: composeDaemon over a temp HOME, on a build
// with no ldflags, returns RunOptions.Upgrade non-nil, and the build hash it
// was gated on is a hex sha256 rather than the "dev" sentinel.
func TestWireUpgradeAlwaysWires(t *testing.T) {
	c := composeForTest(t)
	if c.Opts.Upgrade == nil {
		t.Fatalf("composeDaemon left RunOptions.Upgrade nil with build hash %q; want it wired on every hashable build",
			daemon.BuildHash())
	}
	if h := daemon.BuildHash(); len(h) != 64 {
		t.Fatalf("BuildHash = %q; want the 64-hex sha256 of the running test binary", h)
	}
	if c.Opts.Upgrade.BeforeRelaunch == nil {
		t.Error("composeDaemon wired Upgrade without BeforeRelaunch; the relaunch would skip the run-context join")
	}
}
