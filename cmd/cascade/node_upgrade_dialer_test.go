// Purpose: unit coverage for composeUpgradeDialer, which shipped with no
//
//	direct test of its own: the empty-data-dir refusal, and a real
//	success path building a real ssh signing identity over a temp-dir
//	file vault (ForceFileVault, never the real OS keychain).
//
// SPORT: cmd.cascade.node-upgrade/TEST (node upgrade dialer composition).
package main

import (
	"context"
	"testing"
)

func TestComposeUpgradeDialer_EmptyDataDirRefuses(t *testing.T) {
	deps := nodeCLIDeps{Paths: emptyDataDirPaths{}}
	_, _, err := composeUpgradeDialer(context.Background(), deps)
	if err == nil {
		t.Fatal("composeUpgradeDialer with an unresolvable data dir = nil error, want a refusal")
	}
}

func TestComposeUpgradeDialer_BuildsRealDialerAndVerifier(t *testing.T) {
	dataDir := t.TempDir()
	deps := nodeCLIDeps{Paths: fakeDaemonPaths{root: dataDir}, SecretsDir: dataDir}
	dialer, verifyFor, err := composeUpgradeDialer(context.Background(), deps)
	if err != nil {
		t.Fatalf("composeUpgradeDialer: %v", err)
	}
	if dialer == nil {
		t.Error("composeUpgradeDialer: dialer is nil")
	}
	if verifyFor == nil {
		t.Fatal("composeUpgradeDialer: verifyFor is nil")
	}
}
