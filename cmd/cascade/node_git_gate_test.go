// Purpose: prove the git-gate install verb is gated by the real elevation
// wiring and writes nothing without a proof.

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestGitGateInstallRefusesUnelevated drives `node git-gate install` through
// the production nodeCLIDeps.Gate path with an unsigned challenge: it returns
// KindElevationRequired and writes neither the hook nor the registry store.
func TestGitGateInstallRefusesUnelevated(t *testing.T) {
	skipWithoutLocalElevation(t)
	deps := gitGateTestDeps(t)
	f := newGateFixture(t, nil)
	f.ks.sign = func([]byte) ([]byte, error) { return nil, nil }
	deps.Gate = f.gate
	repo := gitGateBareRepo(t)
	node := &cobra.Command{Use: "node"}
	mountNodeCLICmds(node, deps)
	node.SetArgs([]string{"git-gate", "install", "--repo", repo, "--repo-id", "r1"})
	node.SetContext(t.Context())
	err := node.Execute()
	if !cascade.HasKind(err, cascade.KindElevationRequired) {
		t.Fatalf("install without a proof = %v, want ELEVATION_REQUIRED", err)
	}
	if _, serr := os.Stat(filepath.Join(repo, "hooks", "pre-receive")); !os.IsNotExist(serr) {
		t.Fatalf("unelevated install wrote the hook: %v", serr)
	}
	if _, serr := os.Stat(deps.Paths.DataDir()); !os.IsNotExist(serr) {
		t.Fatalf("unelevated install wrote the store: %v", serr)
	}
}
