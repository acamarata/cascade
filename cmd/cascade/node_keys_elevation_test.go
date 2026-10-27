package main

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestNodeKeyVerbsRequireSignedProof: node.rotate_key and node.revoke take
// the same signed single-use proof as every other local elevated verb. Each
// refuses with no signature, with a valid signature over another method and
// with a replayed attestation, and is granted with a valid proof.
func TestNodeKeyVerbsRequireSignedProof(t *testing.T) {
	skipWithoutLocalElevation(t)
	for _, verb := range []string{"node.rotate_key", "node.revoke"} {
		f := newGateFixture(t, nil)
		refused := func(label, text string) {
			t.Helper()
			if err := requireElevation(t.Context(), f.gate, verb); !cascade.HasKind(err, cascade.KindElevationRequired) || !strings.Contains(err.Error(), text) {
				t.Fatalf("%s %s = %v, want ELEVATION_REQUIRED naming %q", verb, label, err, text)
			}
		}
		f.ks.sign = func([]byte) ([]byte, error) { return nil, nil }
		refused("with no signature", "returned no signature")
		f.ks.sign = nil
		f.gate.attest = func(ks elevation.ElevationKeystore, c runtime.Clock, _, h, n string) (rpc.Attestation, error) {
			return signAttestation(ks, c, "node.remove", h, n)
		}
		refused("signed over node.remove", "method does not match")
		var first rpc.Attestation
		f.gate.attest = func(ks elevation.ElevationKeystore, c runtime.Clock, m, h, n string) (rpc.Attestation, error) {
			att, err := signAttestation(ks, c, m, h, n)
			first = att
			return att, err
		}
		if err := requireElevation(t.Context(), f.gate, verb); err != nil {
			t.Fatalf("%s with a valid proof = %v", verb, err)
		}
		f.gate.attest = func(elevation.ElevationKeystore, runtime.Clock, string, string, string) (rpc.Attestation, error) {
			return first, nil
		}
		refused("with a replayed attestation", "nonce not found")
		// The replay is refused at the nonce claim; the gate still records
		// its deny after the replay's challenge attempt.
		if got := f.tap.kinds(); len(got) < 3 || got[len(got)-3] != "elevation.grant" || got[len(got)-2] != "elevation.attempt" || got[len(got)-1] != "elevation.deny" {
			t.Fatalf("%s audit rows %v, want grant, then the replay's attempt and deny", verb, got)
		}
	}
}

// TestNodeRevokeRefusedWithoutProofLeavesTheRecord drives the command: an
// unsigned challenge changes nothing, a valid proof revokes.
func TestNodeRevokeRefusedWithoutProofLeavesTheRecord(t *testing.T) {
	skipWithoutLocalElevation(t)
	deps := testNodeCLIDeps(t, nil)
	f := newGateFixture(t, nil)
	deps.Gate = f.gate
	id := seedRecord(t, deps, nodes.TierWorkerTrusted)
	run := func() error {
		cmd := newNodeRevokeCmd(deps)
		cmd.SetArgs([]string{id.NodeID})
		cmd.SetContext(context.Background())
		return cmd.Execute()
	}
	f.ks.sign = func([]byte) ([]byte, error) { return nil, nil }
	if err := run(); !cascade.HasKind(err, cascade.KindElevationRequired) {
		t.Fatalf("unsigned revoke = %v", err)
	}
	store, err := openRecordStore(deps)
	if err != nil {
		t.Fatal(err)
	}
	if rec, err := store.Get(id.NodeID); err != nil || len(rec.RevokedKeys) != 0 {
		t.Fatalf("an unsigned revoke changed the record: %+v %v", rec, err)
	}
	f.ks.sign = nil
	if err := run(); err != nil {
		t.Fatalf("signed revoke = %v", err)
	}
	if rec, err := store.Get(id.NodeID); err != nil || len(rec.RevokedKeys) == 0 {
		t.Fatalf("a signed revoke did not revoke: %+v %v", rec, err)
	}
}
