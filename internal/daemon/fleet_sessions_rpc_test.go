package daemon

// Purpose: TestRegisterFleetSessionsHandlerServesList (P1-E12-W6-S121-T1's
//   own contract-named test), plus the nil-store degradation companion
//   test mirroring TestRegisterFleetJournalHandler_NilStoreRegistersNothing.
//
// SPORT: internal.daemon.RegisterFleetSessionsHandler/ADDED
//   (P1-E12-W6-S121-T1).

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
)

// TestRegisterFleetSessionsHandlerServesList proves
// RegisterFleetSessionsHandler actually mounts fleet.sessions.list on the
// registry it is given AND that the mounted handler serves real data:
// seeds one record through a second sessions.Store over the SAME
// underlying provider.Store RegisterFleetSessionsHandler built its own
// Store from, then dispatches fleet.sessions.list through the real
// registry.Dispatch (never the handler function in-process) and asserts
// the seeded record comes back.
func TestRegisterFleetSessionsHandlerServesList(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	store := storetest.NewMemStore()
	bus := events.New(store, clock)

	registry := rpc.NewRegistry()
	RegisterFleetSessionsHandler(registry, store, clock, bus)

	if !registry.Registered(sessions.MethodList) {
		t.Fatalf("registry.Registered(%q) = false, want true", sessions.MethodList)
	}

	seed := sessions.New(store, clock, bus)
	if err := seed.Upsert(ctx, sessions.SessionRecord{SessionID: "s1", Harness: "claude", State: "running"}); err != nil {
		t.Fatalf("seed Upsert: %v", err)
	}

	req := &rpc.Request{JSONRPC: "2.0", Method: sessions.MethodList}
	result, errObj := registry.Dispatch(ctx, req)
	if errObj != nil {
		t.Fatalf("Dispatch(%s): %+v", sessions.MethodList, errObj)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var decoded struct {
		Sessions []sessions.SessionRecord `json:"sessions"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(decoded.Sessions) != 1 || decoded.Sessions[0].SessionID != "s1" {
		t.Fatalf("fleet.sessions.list() = %+v, want [s1]", decoded.Sessions)
	}
}

// TestRegisterFleetSessionsHandler_NilStoreRegistersNothing proves the
// documented nil-store degradation: a nil store leaves fleet.sessions.list
// unregistered rather than constructing a sessions.Store over a store that
// does not exist.
func TestRegisterFleetSessionsHandler_NilStoreRegistersNothing(t *testing.T) {
	registry := rpc.NewRegistry()
	RegisterFleetSessionsHandler(registry, nil, testkit.NewFrozenClock(time.Unix(1_700_000_000, 0)), nil)

	if registry.Registered(sessions.MethodList) {
		t.Errorf("registry.Registered(%q) = true with a nil store, want false", sessions.MethodList)
	}
}

// TestRegisterFleetSessionsHandler_NilBusStillRegisters proves a nil
// *events.Bus does not panic RegisterFleetSessionsHandler or its
// registered handler: emission is best-effort (Store.emit's own
// documented nil-bus degradation), so a nil bus must still leave
// fleet.sessions.list fully functional, matching the fleet.sessions.list
// handler used before an *events.Bus exists at daemon startup.
func TestRegisterFleetSessionsHandler_NilBusStillRegisters(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	store := storetest.NewMemStore()

	registry := rpc.NewRegistry()
	RegisterFleetSessionsHandler(registry, store, clock, nil)

	seed := sessions.New(store, clock, nil)
	if err := seed.Upsert(ctx, sessions.SessionRecord{SessionID: "s1"}); err != nil {
		t.Fatalf("seed Upsert with a nil bus: %v", err)
	}

	req := &rpc.Request{JSONRPC: "2.0", Method: sessions.MethodList}
	if _, errObj := registry.Dispatch(ctx, req); errObj != nil {
		t.Fatalf("Dispatch(%s) with a nil bus: %+v", sessions.MethodList, errObj)
	}
}
