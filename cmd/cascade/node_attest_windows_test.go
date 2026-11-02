//go:build windows

// Purpose: windows counterpart of node_bind_test.go and
// node_attest_integration_test.go. The installation key and the node serve
// attest leg are not available on windows tier-2, so each named test skips
// with the tier-2 message instead of passing silently; bind itself is proven
// to refuse.
// SPORT: cmd/cascade/node bind (windows, P1-CI-13).
package main

import (
	"testing"

	"github.com/acamarata/cascade/internal/nodes"
)

const windowsTier2Skip = "node verbs are not available on windows tier-2 (nodes.RefuseOnGOOS)"

func TestNodeBindRefusesOnWindows(t *testing.T) {
	deps := testNodeCLIDeps(t, nil)
	windowsRefused(t, "node bind", runNodeBind(fakeCmdWithContext(t), deps, "binding.json"))
	if err := nodes.RefuseOnGOOS("windows"); err == nil {
		t.Fatal("RefuseOnGOOS(windows) = nil")
	}
}

func TestNodeEnrollPrintsBinding(t *testing.T)               { t.Skip(windowsTier2Skip) }
func TestNodeBindWritesControllerBinding(t *testing.T)       { t.Skip(windowsTier2Skip) }
func TestNodeBindRefusedWithoutElevation(t *testing.T)       { t.Skip(windowsTier2Skip) }
func TestNodeBindPrintsControllerKeyID(t *testing.T)         { t.Skip(windowsTier2Skip) }
func TestAttestSignSucceedsAfterRealEnrollment(t *testing.T) { t.Skip(windowsTier2Skip) }
func TestNodeServeMountsAttestSign(t *testing.T)             { t.Skip(windowsTier2Skip) }
