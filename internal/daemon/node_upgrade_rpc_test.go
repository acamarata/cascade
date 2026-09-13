// Purpose: unit coverage for node_upgrade_rpc.go's composition root --
//
//	proves RegisterNodeUpgradeHandler mounts "node.upgrade" on a real
//	*rpc.Registry, and that resolveNodeUpgradeDeps fails closed at each
//	documented staging precondition (no staged artifact, no staged
//	signature, unset CASCADE_MINISIGN_PUBKEY, unreadable pubkey file)
//	rather than silently building an UpgradeDeps with a missing piece.
//
// SPORT: internal/daemon:node-upgrade-rpc (TEST) -- P1-E17-W4-S36-T5.
package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
)

// stageNodeUpgradeArtifact writes the two files resolveNodeUpgradeDeps
// requires under root's nodes/upgrade staging directory.
func stageNodeUpgradeArtifact(t *testing.T, root string, artifact, sig bool) {
	t.Helper()
	stageDir := filepath.Join(root, "data", "nodes", "upgrade")
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(stageDir): %v", err)
	}
	if artifact {
		if err := os.WriteFile(filepath.Join(stageDir, "artifact"), []byte("binary-bytes"), 0o600); err != nil {
			t.Fatalf("write artifact: %v", err)
		}
	}
	if sig {
		if err := os.WriteFile(filepath.Join(stageDir, "artifact.minisig"), []byte("untrusted comment: x\nsig-bytes"), 0o600); err != nil {
			t.Fatalf("write artifact.minisig: %v", err)
		}
	}
}

// TestRegisterNodeUpgradeHandler_MountsMethod proves the handler is
// actually wired to the registry it is given, closing the same class of
// gap R-14.223 named for fleet.journal_show/replay.
func TestRegisterNodeUpgradeHandler_MountsMethod(t *testing.T) {
	registry := rpc.NewRegistry()
	paths := quotaTestPaths{root: t.TempDir()}
	clock := fixedDaemonClock{t: time.Now()}

	RegisterNodeUpgradeHandler(registry, paths, clock)

	if !registry.Registered("node.upgrade") {
		t.Error(`registry.Registered("node.upgrade") = false, want true`)
	}
}

func TestResolveNodeUpgradeDeps_NoStagedArtifact(t *testing.T) {
	paths := quotaTestPaths{root: t.TempDir()}
	_, err := resolveNodeUpgradeDeps(context.Background(), paths, fixedDaemonClock{t: time.Now()})
	if err == nil {
		t.Fatal("resolveNodeUpgradeDeps: err = nil, want a missing-artifact refusal")
	}
	if got := err.Error(); !strings.Contains(got, "no staged artifact") {
		t.Errorf("resolveNodeUpgradeDeps: err = %q, want it to name the missing artifact", got)
	}
}

func TestResolveNodeUpgradeDeps_NoStagedSignature(t *testing.T) {
	root := t.TempDir()
	stageNodeUpgradeArtifact(t, root, true, false)
	paths := quotaTestPaths{root: root}
	_, err := resolveNodeUpgradeDeps(context.Background(), paths, fixedDaemonClock{t: time.Now()})
	if err == nil {
		t.Fatal("resolveNodeUpgradeDeps: err = nil, want a missing-signature refusal")
	}
	if got := err.Error(); !strings.Contains(got, "no staged artifact signature") {
		t.Errorf("resolveNodeUpgradeDeps: err = %q, want it to name the missing signature", got)
	}
}

func TestResolveNodeUpgradeDeps_MissingPubkeyEnv(t *testing.T) {
	root := t.TempDir()
	stageNodeUpgradeArtifact(t, root, true, true)
	t.Setenv("CASCADE_MINISIGN_PUBKEY", "")
	paths := quotaTestPaths{root: root}
	_, err := resolveNodeUpgradeDeps(context.Background(), paths, fixedDaemonClock{t: time.Now()})
	if err == nil {
		t.Fatal("resolveNodeUpgradeDeps: err = nil, want a refusal for the unset env var")
	}
	if got := err.Error(); !strings.Contains(got, "CASCADE_MINISIGN_PUBKEY is not set") {
		t.Errorf("resolveNodeUpgradeDeps: err = %q, want it to name CASCADE_MINISIGN_PUBKEY", got)
	}
}

func TestResolveNodeUpgradeDeps_UnreadablePubkeyFile(t *testing.T) {
	root := t.TempDir()
	stageNodeUpgradeArtifact(t, root, true, true)
	t.Setenv("CASCADE_MINISIGN_PUBKEY", filepath.Join(root, "does-not-exist.pub"))
	paths := quotaTestPaths{root: root}
	_, err := resolveNodeUpgradeDeps(context.Background(), paths, fixedDaemonClock{t: time.Now()})
	if err == nil {
		t.Fatal("resolveNodeUpgradeDeps: err = nil, want a refusal for the unreadable pubkey file")
	}
	if got := err.Error(); !strings.Contains(got, "CASCADE_MINISIGN_PUBKEY") {
		t.Errorf("resolveNodeUpgradeDeps: err = %q, want it to name CASCADE_MINISIGN_PUBKEY", got)
	}
}
