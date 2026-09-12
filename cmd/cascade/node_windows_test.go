//go:build windows

// Purpose: R-21.226 — proves, natively in the windows/amd64 CI lane, that
//
//	every daemon-backed `node` verb S-36.T4 mounts refuses with the typed
//	tier-2 message, never a silent skip.
//
// SPORT: cmd/cascade/node (windows refusal, P1-E17-W4-S36-T4).
package main

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// windowsRefused asserts err is the typed tier-2 refusal
// (internal/nodes.RefuseOnGOOS's KindUnsupported), never nil and never an
// untyped/panic failure.
func windowsRefused(t *testing.T, verb string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected a tier-2 refusal on windows, got nil", verb)
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnsupported {
		t.Fatalf("%s: got kind %v (ok=%v), want KindUnsupported", verb, kind, ok)
	}
}

// TestNodeVerbsRefuseOnWindows drives every daemon-backed `node` verb
// S-36.T4 mounts through the REAL production cobra tree (productionNodeCLIDeps,
// runtime.GOOS resolved natively as "windows" on this CI lane) and asserts
// each refuses before touching any real file or socket.
func TestNodeVerbsRefuseOnWindows(t *testing.T) {
	deps := productionNodeCLIDeps()
	if deps.GOOS != "windows" {
		t.Fatalf("this file is windows-build-tagged; runtime.GOOS reported %q", deps.GOOS)
	}

	windowsRefused(t, "serve", runNodeServe(context.Background(), nodeServeDeps{GOOS: deps.GOOS}))

	list := newNodeListCmd(deps)
	list.SetContext(context.Background())
	list.SetArgs(nil)
	windowsRefused(t, "list", list.Execute())

	status := newNodeStatusCmd(deps)
	status.SetContext(context.Background())
	status.SetArgs([]string{"some-node"})
	windowsRefused(t, "status", status.Execute())

	drain := newNodeDrainCmd(deps)
	drain.SetContext(context.Background())
	drain.SetArgs([]string{"some-node"})
	windowsRefused(t, "drain", drain.Execute())

	remove := newNodeRemoveCmd(deps)
	remove.SetContext(context.Background())
	remove.SetArgs([]string{"some-node"})
	windowsRefused(t, "remove", remove.Execute())

	rotate := newNodeRotateKeyCmd(deps)
	rotate.SetContext(context.Background())
	rotate.SetArgs([]string{"some-node"})
	windowsRefused(t, "rotate-key", rotate.Execute())

	revoke := newNodeRevokeCmd(deps)
	revoke.SetContext(context.Background())
	revoke.SetArgs([]string{"some-node"})
	windowsRefused(t, "revoke", revoke.Execute())

	admit := newNodeAdmitCmd(deps)
	admit.SetContext(context.Background())
	admit.SetArgs([]string{"worker@host1", "--trust-tier", "worker-trusted"})
	windowsRefused(t, "enroll", admit.Execute())

	upgrade := newNodeUpgradeCmd(deps)
	upgrade.SetContext(context.Background())
	upgrade.SetArgs([]string{"some-node", "--artifact", "x", "--signature", "y"})
	windowsRefused(t, "upgrade", upgrade.Execute())
}
