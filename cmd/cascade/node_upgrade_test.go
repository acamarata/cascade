package main

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestNodeUpgradeCmd_RequiresTargetOrAll(t *testing.T) {
	deps := testNodeCLIDeps(t, nil)
	cmd := newNodeUpgradeCmd(deps)
	cmd.SetArgs([]string{})
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("got %v (kind ok=%v), want KindInvalidInput", err, ok)
	}
}

func TestNodeUpgradeCmd_RequiresElevation(t *testing.T) {
	deps := testNodeCLIDeps(t, map[string]string{"CASCADE_NO_INPUT": "1"})
	id := seedRecord(t, deps, nodes.TierWorkerTrusted)

	cmd := newNodeUpgradeCmd(deps)
	cmd.SetArgs([]string{id.NodeID, "--artifact", "/does/not/matter", "--signature", "/does/not/matter"})
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindElevationRequired {
		t.Fatalf("got %v (kind ok=%v), want ELEVATION_REQUIRED", err, ok)
	}
}

func TestNodeUpgradeCmd_RequiresArtifactAndSignature(t *testing.T) {
	deps := testNodeCLIDeps(t, nil)
	deps.Gate = allowingGate(nil)
	id := seedRecord(t, deps, nodes.TierWorkerTrusted)

	cmd := newNodeUpgradeCmd(deps)
	cmd.SetArgs([]string{id.NodeID})
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("got %v (kind ok=%v), want KindInvalidInput (missing --artifact/--signature)", err, ok)
	}
}

func TestNodeUpgradeCmd_RefusedOnWindows(t *testing.T) {
	deps := testNodeCLIDeps(t, nil)
	deps.GOOS = "windows"
	cmd := newNodeUpgradeCmd(deps)
	cmd.SetArgs([]string{"--all", "--artifact", "x", "--signature", "y"})
	cmd.SetContext(context.Background())
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected a Windows tier-2 refusal")
	}
}

func TestNodeUpgradeView_String(t *testing.T) {
	v := nodeUpgradeView{Outcomes: []nodes.UpgradeOutcome{
		{NodeID: "n1", Installed: true, PreviousVersion: "v1", NewVersion: "v2"},
		{NodeID: "n2", Skipped: true, Reason: "no configured ssh/VPN route"},
		{NodeID: "n3", Skipped: true, NewVersion: "v2"},
		{NodeID: "n4", Err: "unreachable"},
	}}
	s := v.String()
	for _, want := range []string{"n1: upgraded v1 -> v2", "n2: skipped (no configured ssh/VPN route)", "n3: skipped (already at v2)", "n4: FAILED (unreachable)"} {
		if !nodeUpgradeTestContains(s, want) {
			t.Fatalf("String() = %q, missing %q", s, want)
		}
	}
}

func nodeUpgradeTestContains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
