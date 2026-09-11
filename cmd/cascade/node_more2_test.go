// Purpose: continuation of node_more_test.go's coverage additions (split
//
//	out to stay under the 300-line cap): openRecordStore's empty-data-dir
//	refusal driven through each verb's own CLI entry point, and the
//	remaining unknown-node-id/success branches for drain/remove.
//
// SPORT: cmd/cascade/node (ADD tests, P1-E17-W4-S36-T4 coverage follow-up).
package main

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/pkg/cascade"
)

// emptyDataDirNodeDeps builds nodeCLIDeps whose Paths.DataDir is always
// empty (past GOOS/elevation checks), isolating each verb's OWN
// openRecordStore call site from openRecordStore's already-covered unit
// test in node_more_test.go.
func emptyDataDirNodeDeps(t *testing.T) nodeCLIDeps {
	t.Helper()
	deps := testNodeCLIDeps(t, nil)
	deps.Gate = allowingGate(nil)
	deps.Paths = emptyDataDirPaths{}
	return deps
}

func TestNodeListCmd_EmptyDataDirRefusedThroughCLI(t *testing.T) {
	cmd := newNodeListCmd(emptyDataDirNodeDeps(t))
	cmd.SetContext(context.Background())
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected a refusal for an unresolvable data directory")
	}
}

func TestNodeStatusCmd_EmptyDataDirRefusedThroughCLI(t *testing.T) {
	cmd := newNodeStatusCmd(emptyDataDirNodeDeps(t))
	cmd.SetArgs([]string{"n"})
	cmd.SetContext(context.Background())
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected a refusal for an unresolvable data directory")
	}
}

func TestNodeDrainCmd_EmptyDataDirRefusedThroughCLI(t *testing.T) {
	cmd := newNodeDrainCmd(emptyDataDirNodeDeps(t))
	cmd.SetArgs([]string{"n"})
	cmd.SetContext(context.Background())
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected a refusal for an unresolvable data directory")
	}
}

func TestNodeRemoveCmd_EmptyDataDirRefusedThroughCLI(t *testing.T) {
	cmd := newNodeRemoveCmd(emptyDataDirNodeDeps(t))
	cmd.SetArgs([]string{"n"})
	cmd.SetContext(context.Background())
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected a refusal for an unresolvable data directory")
	}
}

func TestNodeRevokeCmd_EmptyDataDirRefusedThroughCLI(t *testing.T) {
	cmd := newNodeRevokeCmd(emptyDataDirNodeDeps(t))
	cmd.SetArgs([]string{"n"})
	cmd.SetContext(context.Background())
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected a refusal for an unresolvable data directory")
	}
}

func TestNodeDrainCmd_UnknownNodeNotFound(t *testing.T) {
	deps := testNodeCLIDeps(t, nil)
	cmd := newNodeDrainCmd(deps)
	cmd.SetArgs([]string{"does-not-exist"})
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindNotFound {
		t.Fatalf("got %v (ok=%v), want KindNotFound", err, ok)
	}
}

// TestNodeRemoveCmd_Success drives remove past its elevation gate to a
// real deletion, also exercising nodeRemovedView's String() rendering
// (the non-JSON output path Result takes by default).
func TestNodeRemoveCmd_Success(t *testing.T) {
	deps := testNodeCLIDeps(t, nil)
	deps.Gate = allowingGate(nil)
	id := seedRecord(t, deps, nodes.TierWorkerTrusted)

	cmd := newNodeRemoveCmd(deps)
	cmd.SetArgs([]string{id.NodeID})
	cmd.SetContext(context.Background())
	if err := cmd.Execute(); err != nil {
		t.Fatalf("remove: %v", err)
	}
	store, err := openRecordStore(deps)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(id.NodeID); err == nil {
		t.Fatal("expected the record to be gone after remove")
	}
}

func TestNodeRemoveCmd_UnknownNodeNotFound(t *testing.T) {
	deps := testNodeCLIDeps(t, nil)
	deps.Gate = allowingGate(nil)
	cmd := newNodeRemoveCmd(deps)
	cmd.SetArgs([]string{"does-not-exist"})
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindNotFound {
		t.Fatalf("got %v (ok=%v), want KindNotFound", err, ok)
	}
}

// TestRequireElevation_PreconditionsUnmetWithoutNoInput isolates
// requireElevation's !enrolled||!available refusal branch, distinct from
// the CASCADE_NO_INPUT=1 short-circuit node_test.go's elevation tests
// exercise: the default gate (nil keystore/backend factories) reports
// both preconditions false with no CASCADE_NO_INPUT set at all.
func TestRequireElevation_PreconditionsUnmetWithoutNoInput(t *testing.T) {
	deps := testNodeCLIDeps(t, nil) // env=nil: no CASCADE_NO_INPUT, default gate
	id := seedRecord(t, deps, nodes.TierWorkerTrusted)

	cmd := newNodeRevokeCmd(deps)
	cmd.SetArgs([]string{id.NodeID})
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindElevationRequired {
		t.Fatalf("got %v (kind ok=%v), want ELEVATION_REQUIRED", err, ok)
	}
}
