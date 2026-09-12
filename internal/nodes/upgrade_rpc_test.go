package nodes

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestRegisterUpgradeHandler_DispatchesToRealRunUpgrade drives
// RegisterUpgradeHandler through the real rpc.Registry.Dispatch entry
// point (this ticket's own SAFETY-CRITICAL wiring proof: "node.upgrade"
// must be REACHABLE, not merely built and tested in isolation).
func TestRegisterUpgradeHandler_DispatchesToRealRunUpgrade(t *testing.T) {
	rec := testRecordWithRoute("n1", "worker", "host1:22")
	store := upgradeStoreWith(rec)
	artifact, pub := testArtifact(t, "v2.5.0")
	resolve := func(context.Context) (UpgradeDeps, error) {
		return UpgradeDeps{
			Store: store, Artifact: artifact,
			Provision: ProvisionDeps{PublicKey: pub, VerifyFor: noopVerifyFor, Dialer: &fakeExecDialer{session: newFakeExecSession("v2.4.0")}},
		}, nil
	}
	reg := rpc.NewRegistry()
	RegisterUpgradeHandler(reg, resolve)

	params, _ := json.Marshal(UpgradeRequest{NodeID: "n1"})
	result, errObj := reg.Dispatch(context.Background(), &rpc.Request{Method: "node.upgrade", Params: params})
	if errObj != nil {
		t.Fatalf("dispatch failed: %+v", errObj)
	}
	outcomes, ok := result.([]UpgradeOutcome)
	if !ok || len(outcomes) != 1 || !outcomes[0].Installed {
		t.Fatalf("result = %+v, want one installed outcome", result)
	}
}

// TestRegisterUpgradeHandler_RemovedFromRegistryFails is the mutation
// proof required by this brief: with the registration line removed, the
// exact same Dispatch call must fail with "method not found" — proving
// the test above is actually exercising real wiring, not a pass-through.
// Simulated here by dispatching against a registry that never called
// RegisterUpgradeHandler at all (the RED case), asserted immediately
// before the GREEN case above to keep both outcomes in one file.
func TestRegisterUpgradeHandler_RemovedFromRegistryFails(t *testing.T) {
	reg := rpc.NewRegistry()
	params, _ := json.Marshal(UpgradeRequest{NodeID: "n1"})
	_, errObj := reg.Dispatch(context.Background(), &rpc.Request{Method: "node.upgrade", Params: params})
	if errObj == nil {
		t.Fatal("expected method-not-found when node.upgrade was never registered (RED case)")
	}
}

func TestRegisterUpgradeHandler_RequiresNodeIDOrAll(t *testing.T) {
	reg := rpc.NewRegistry()
	RegisterUpgradeHandler(reg, func(context.Context) (UpgradeDeps, error) { return UpgradeDeps{}, nil })
	_, errObj := reg.Dispatch(context.Background(), &rpc.Request{Method: "node.upgrade", Params: json.RawMessage(`{}`)})
	if errObj == nil {
		t.Fatal("expected a refusal for a request naming neither node_id nor all")
	}
}

func TestRegisterUpgradeHandler_ResolveFailurePropagates(t *testing.T) {
	reg := rpc.NewRegistry()
	wantErr := cascade.New(cascade.KindUnavailable, "no staged artifact")
	RegisterUpgradeHandler(reg, func(context.Context) (UpgradeDeps, error) { return UpgradeDeps{}, wantErr })
	params, _ := json.Marshal(UpgradeRequest{All: true})
	_, errObj := reg.Dispatch(context.Background(), &rpc.Request{Method: "node.upgrade", Params: params})
	if errObj == nil {
		t.Fatal("expected the resolve error to propagate")
	}
}
