// Purpose: additional coverage for node.go/node_keys.go's cobra RunE
//
//	bodies (list/status/rotate-key/revoke), which node_test.go's own
//	table left largely unexecuted: it drives the elevation-refused path
//	for rotate-key/revoke but never their success path, and never
//	executes list/status through cmd.Execute() at all. Continued in
//	node_more2_test.go (300-line cap).
//
// SPORT: cmd/cascade/node (ADD tests, P1-E17-W4-S36-T4 coverage follow-up).
package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestProductionNodeCLIDeps_Basic drives the real composition root: no
// backend I/O happens until a returned closure is invoked, so this is
// safely callable directly (Art.7.1 unaffected).
func TestProductionNodeCLIDeps_Basic(t *testing.T) {
	deps := productionNodeCLIDeps()
	if deps.Paths == nil || deps.Clock == nil || deps.Gate == nil || deps.Getenv == nil {
		t.Fatalf("expected every collaborator to be populated: %+v", deps)
	}
	if deps.GOOS == "" {
		t.Fatal("expected a non-empty GOOS")
	}
}

func TestOpenRecordStore_EmptyDataDirRefused(t *testing.T) {
	deps := nodeCLIDeps{Paths: emptyDataDirPaths{}}
	if _, err := openRecordStore(deps); err == nil {
		t.Fatal("expected a refusal for an unresolvable data directory")
	}
}

func TestNodeListCmd_EmptyResultSet(t *testing.T) {
	deps := testNodeCLIDeps(t, nil)
	cmd := newNodeListCmd(deps)
	cmd.SetContext(context.Background())
	if err := cmd.Execute(); err != nil {
		t.Fatalf("list on an empty store: %v", err)
	}
}

func TestNodeListCmd_RefusedOnWindows(t *testing.T) {
	deps := testNodeCLIDeps(t, nil)
	deps.GOOS = "windows"
	cmd := newNodeListCmd(deps)
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnsupported {
		t.Fatalf("got %v (ok=%v), want KindUnsupported", err, ok)
	}
}

// TestNodeListCmd_UnparseableRecordStore proves an unreadable device
// record store (a directory blocking the expected file path, so
// os.ReadFile fails with something other than IsNotExist) surfaces as a
// real error through the CLI entry point, not a silently empty list.
func TestNodeListCmd_UnparseableRecordStore(t *testing.T) {
	deps := testNodeCLIDeps(t, nil)
	dataDir := deps.Paths.DataDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "nodes", "devices.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := newNodeListCmd(deps)
	cmd.SetContext(context.Background())
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected an error for an unreadable device record store")
	}
}

func TestNodeStatusCmd_Success(t *testing.T) {
	deps := testNodeCLIDeps(t, nil)
	id := seedRecord(t, deps, nodes.TierWorkerTrusted)
	cmd := newNodeStatusCmd(deps)
	cmd.SetArgs([]string{id.NodeID})
	cmd.SetContext(context.Background())
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status: %v", err)
	}
}

func TestNodeStatusCmd_UnknownNodeNotFound(t *testing.T) {
	deps := testNodeCLIDeps(t, nil)
	cmd := newNodeStatusCmd(deps)
	cmd.SetArgs([]string{"does-not-exist"})
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindNotFound {
		t.Fatalf("got %v (ok=%v), want KindNotFound", err, ok)
	}
}

func TestNodeStatusCmd_RefusedOnWindows(t *testing.T) {
	deps := testNodeCLIDeps(t, nil)
	deps.GOOS = "windows"
	cmd := newNodeStatusCmd(deps)
	cmd.SetArgs([]string{"whatever"})
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnsupported {
		t.Fatalf("got %v (ok=%v), want KindUnsupported", err, ok)
	}
}

// TestNodeRotateKeyCmd_Success drives rotate-key's full body: this
// machine's local self-keystore must already hold the private key
// matching the target record's CURRENT public key (this file's own
// package doc explains why rotate-key rotates local identity, not a
// remote peer's), so the fixture seeds both sides consistently.
func TestNodeRotateKeyCmd_Success(t *testing.T) {
	deps := testNodeCLIDeps(t, nil)
	deps.Gate = allowingGate(nil)

	id, priv, err := nodes.GenerateIdentity(deterministicReader{seed: 11})
	if err != nil {
		t.Fatal(err)
	}
	store, err := openRecordStore(deps)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Enroll(id, nodes.TierWorkerTrusted); err != nil {
		t.Fatal(err)
	}
	ks, err := nodes.NewNodeKeystore(secrets.Config{
		Dir:            deps.SecretsDir,
		ForceFileVault: deps.SecretsDir != "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ks.Store(context.Background(), id.NodeID, priv); err != nil {
		t.Fatal(err)
	}

	cmd := newNodeRotateKeyCmd(deps)
	cmd.SetArgs([]string{id.NodeID})
	cmd.SetContext(context.Background())
	if err := cmd.Execute(); err != nil {
		t.Fatalf("rotate-key: %v", err)
	}

	rec, err := store.Get(id.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.PubKeyB64 == id.PubKeyB64() {
		t.Fatal("expected the public key to change after rotation")
	}
	if len(rec.RevokedKeys) == 0 {
		t.Fatal("expected the superseded key to be recorded as revoked")
	}
}

// TestNodeRevokeCmd_Success drives revoke's full body past its
// elevation gate to the actual RecordStore.Revoke call.
func TestNodeRevokeCmd_Success(t *testing.T) {
	deps := testNodeCLIDeps(t, nil)
	deps.Gate = allowingGate(nil)
	id := seedRecord(t, deps, nodes.TierWorkerTrusted)

	cmd := newNodeRevokeCmd(deps)
	cmd.SetArgs([]string{id.NodeID})
	cmd.SetContext(context.Background())
	if err := cmd.Execute(); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	store, err := openRecordStore(deps)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := store.Get(id.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.RevokedKeys) == 0 {
		t.Fatal("expected the revoked key to be recorded")
	}
}

func TestNodeRotateKeyCmd_RefusedOnWindows(t *testing.T) {
	deps := testNodeCLIDeps(t, nil)
	deps.GOOS = "windows"
	cmd := newNodeRotateKeyCmd(deps)
	cmd.SetArgs([]string{"whatever"})
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnsupported {
		t.Fatalf("got %v (ok=%v), want KindUnsupported", err, ok)
	}
}

// TestNodeRotateKeyCmd_SignErrorNodeNotInLocalKeystore isolates
// keystore.Sign's own error branch: the target device record exists, but
// this machine's local self-keystore has never stored a private key for
// it (never enrolled locally), so Sign refuses with KindNotFound.
func TestNodeRotateKeyCmd_SignErrorNodeNotInLocalKeystore(t *testing.T) {
	deps := testNodeCLIDeps(t, nil)
	deps.Gate = allowingGate(nil)
	id := seedRecord(t, deps, nodes.TierWorkerTrusted)

	cmd := newNodeRotateKeyCmd(deps)
	cmd.SetArgs([]string{id.NodeID})
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected a refusal signing with no locally-held key for this node id")
	}
}

func TestNodeRevokeCmd_RefusedOnWindows(t *testing.T) {
	deps := testNodeCLIDeps(t, nil)
	deps.GOOS = "windows"
	cmd := newNodeRevokeCmd(deps)
	cmd.SetArgs([]string{"whatever"})
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnsupported {
		t.Fatalf("got %v (ok=%v), want KindUnsupported", err, ok)
	}
}

// TestNodeRevokeCmd_UnknownNodeNotFound isolates store.Revoke's own
// error branch, past a successfully-opened record store.
func TestNodeRevokeCmd_UnknownNodeNotFound(t *testing.T) {
	deps := testNodeCLIDeps(t, nil)
	deps.Gate = allowingGate(nil)
	cmd := newNodeRevokeCmd(deps)
	cmd.SetArgs([]string{"does-not-exist"})
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindNotFound {
		t.Fatalf("got %v (ok=%v), want KindNotFound", err, ok)
	}
}
