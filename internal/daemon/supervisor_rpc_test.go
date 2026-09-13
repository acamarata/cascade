package daemon

// Purpose: unit coverage for RegisterSupervisorHandler, driven through a
//   real *rpc.Registry.Dispatch (attention_rpc_test.go's own precedent):
//   proves the nil-store no-op degradation, that a real store wires
//   supervisor.snapshot/events_schema to a reachable, real handler rather
//   than leaving it unregistered, and that a fully-populated real
//   Store+Store pairing returns a non-zero snapshot end to end.
// SPORT: internal/daemon (ADD, P1-E18-W4-S40-T4).

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// TestRegisterSupervisorHandler_NilStore_RegistersNothing proves the
// documented nil-store degradation: a nil provider.Store registers no
// methods at all, matching every sibling registerXHandler's own contract.
func TestRegisterSupervisorHandler_NilStore_RegistersNothing(t *testing.T) {
	registry := rpc.NewRegistry()
	RegisterSupervisorHandler(registry, nil, runtime.NewSystemClock(), nil, nil, nil)
	if registry.Registered(rpc.MethodSupervisorSnapshot) {
		t.Error("supervisor.snapshot registered despite a nil store")
	}
	if registry.Registered(rpc.MethodSupervisorEventsSchema) {
		t.Error("supervisor.events_schema registered despite a nil store")
	}
}

// TestRegisterSupervisorHandler_RealStore_ReachableViaDispatch is the
// wiring mutation proof: a real store wires both methods to a real,
// reachable handler over a real *rpc.Registry.Dispatch call, rather than
// leaving them unregistered (method-not-found).
func TestRegisterSupervisorHandler_RealStore_ReachableViaDispatch(t *testing.T) {
	registry := rpc.NewRegistry()
	store := storetest.NewMemStore()
	RegisterSupervisorHandler(registry, store, runtime.NewSystemClock(), nil, nil, nil)

	for _, method := range []string{rpc.MethodSupervisorSnapshot, rpc.MethodSupervisorEventsSchema} {
		if !registry.Registered(method) {
			t.Errorf("%s never reached the registry", method)
		}
	}

	const methodNotFound = -32601
	for _, method := range []string{rpc.MethodSupervisorSnapshot, rpc.MethodSupervisorEventsSchema} {
		_, errObj := registry.Dispatch(context.Background(), &rpc.Request{Method: method, Params: json.RawMessage(`{}`)})
		if errObj != nil && errObj.Code == methodNotFound {
			t.Errorf("%s returned method-not-found: %+v", method, errObj)
		}
	}
}

// TestSupervisorSource_Snapshot_RealStores_NonZero proves the composed
// supervisorSource reads real, non-zero data end to end: a real
// sessions.Store row plus a real supervision.Store attention item over the
// SAME session, both over one real in-memory provider.Store.
func TestSupervisorSource_Snapshot_RealStores_NonZero(t *testing.T) {
	kv := storetest.NewMemStore()
	clock := runtime.NewSystemClock()

	sessStore := sessions.New(kv, clock, nil)
	if err := sessStore.Upsert(context.Background(), sessions.SessionRecord{
		SessionID: "sess-alpha", Harness: "claude", State: "running", ToolCount: 7,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	attnStore := supervision.NewStore(kv, clock, nil, supervision.NewSystemIDGenerator(), 0)
	if _, err := attnStore.Push(context.Background(), supervision.AttentionItem{
		Kind:      supervision.KindStall,
		SourceRef: "sess-alpha",
		ScopeRef:  scope.Ref{Kind: scope.ScopeKindSession, ID: "sess-alpha"},
	}); err != nil {
		t.Fatalf("Push: %v", err)
	}

	src := &supervisorSource{attention: attnStore, sessions: sessStore}
	snap, err := src.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.AttentionQueueDepth != 1 {
		t.Errorf("AttentionQueueDepth = %d, want 1", snap.AttentionQueueDepth)
	}
	if snap.StallCount != 1 {
		t.Errorf("StallCount = %d, want 1", snap.StallCount)
	}
	if len(snap.Sessions) != 1 || snap.Sessions[0].SessionID != "sess-alpha" {
		t.Fatalf("Sessions = %+v, want one sess-alpha row", snap.Sessions)
	}
	if snap.Sessions[0].ActionCount != 7 {
		t.Errorf("ActionCount = %d, want 7", snap.Sessions[0].ActionCount)
	}
	if snap.Sessions[0].InterruptCount != 1 {
		t.Errorf("InterruptCount = %d, want 1", snap.Sessions[0].InterruptCount)
	}
	if snap.AutonomyProfile != "locked" {
		t.Errorf("AutonomyProfile = %q, want %q (nil controller's documented default)", snap.AutonomyProfile, "locked")
	}
	if snap.AutoAdvanceTier != "none" {
		t.Errorf("AutoAdvanceTier = %q, want %q (nil controller allows no rung)", snap.AutoAdvanceTier, "none")
	}
	if snap.HeadroomCeiling != nil {
		t.Errorf("HeadroomCeiling = %v, want nil (no metrics registry wired)", *snap.HeadroomCeiling)
	}
}
