package daemon

// Purpose: unit coverage for RegisterFleetAttentionHandler, driven through
//   a real *rpc.Registry.Dispatch (the fleet.rpc_test.go precedent):
//   proves the nil-store no-op degradation, and that a real store wires
//   fleet.attention.list to a reachable, real handler rather than leaving
//   it unregistered.
// SPORT: internal/daemon (ADD, coverage-floor fix).

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// TestRegisterFleetAttentionHandler_NilStore_RegistersNothing proves the
// documented nil-store degradation: a nil provider.Store registers no
// methods at all, matching every sibling registerXHandler's own contract.
func TestRegisterFleetAttentionHandler_NilStore_RegistersNothing(t *testing.T) {
	registry := rpc.NewRegistry()
	RegisterFleetAttentionHandler(registry, nil, runtime.NewSystemClock(), nil)
	if registry.Registered(supervision.MethodList) {
		t.Error("fleet.attention.list registered despite a nil store")
	}
	if registry.Registered(supervision.MethodGet) {
		t.Error("fleet.attention.get registered despite a nil store")
	}
	if registry.Registered(supervision.MethodAck) {
		t.Error("fleet.attention.ack registered despite a nil store")
	}
}

// TestRegisterFleetAttentionHandler_RealStore_ReachableViaDispatch proves
// a real store wires all three methods to a real, reachable handler: the
// registry answers with the handler's own real domain error (own_scope is
// required) rather than JSON-RPC's method-not-found, over a real
// *rpc.Registry.Dispatch call.
func TestRegisterFleetAttentionHandler_RealStore_ReachableViaDispatch(t *testing.T) {
	registry := rpc.NewRegistry()
	store := storetest.NewMemStore()
	RegisterFleetAttentionHandler(registry, store, runtime.NewSystemClock(), nil)

	for _, method := range []string{supervision.MethodList, supervision.MethodGet, supervision.MethodAck} {
		if !registry.Registered(method) {
			t.Errorf("%s never reached the registry", method)
		}
	}

	result, errObj := registry.Dispatch(context.Background(), &rpc.Request{Method: supervision.MethodList, Params: json.RawMessage(`{}`)})
	if errObj == nil {
		t.Fatalf("Dispatch(fleet.attention.list) = %v, nil, want the real own_scope-required refusal", result)
	}
	const methodNotFound = -32601
	if errObj.Code == methodNotFound {
		t.Fatalf("fleet.attention.list returned method-not-found: %+v", errObj)
	}
}
