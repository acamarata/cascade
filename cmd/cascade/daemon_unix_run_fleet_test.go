//go:build !windows

// Purpose: buildRPCServer's/buildDaemonRegistry's required tests for
//
//	P1-E12-W6-S121-T1 — fleet.sessions.list is actually registered by the
//	daemon composition root (never asserted against the handler function
//	called directly, which would prove nothing about buildDaemonRegistry
//	itself), and the topic-dispatching SSEMux is actually the handler
//	buildDaemonRegistry builds for GET rpc.EventsPath, wired with the
//	daemon-wide Default and a fleet.sessions Topics entry.
//
//	Neither test opens a real socket or dials real HTTP: an untagged
//	unit test may not import "net"/"net/http" (TestNoNetworkUnitTest_
//	RealTreeGreen), and rpc.ConnContext's socket-peer-owner check for
//	POST /rpc only ever fires for a real accepted unix connection anyway
//	(confirmed first in cmd/cascade/hooks_test.go's own header comment
//	for the identical prior case) — so TestBuildRPCServerRegistersFleetSessions
//	drives buildDaemonRegistry directly and dispatches in-process through
//	the real *rpc.Registry it returns.
//
//	TestBuildRPCServerMountsFleetSessionsSSE checks the events handler
//	buildDaemonRegistry returns is an *rpc.SSEMux with the daemon-wide
//	Default and exactly one fleet.sessions topic bound to a real
//	*sessions.SSEHandler, and that a request through that very handler
//	with no owner peer credentials is refused 403 (the mux's guard is in
//	front of every topic at the composition root). A request that passes
//	the guard needs a ConnContext-resolved owner credential, which only a
//	real accepted unix connection yields; TestEpicRDaemonModeSmoke and
//	TestHarnessWatchReceivesFleetSessions drive that real path, and
//	internal/rpc/sse_mux_test.go proves dispatch itself.
//
// SPORT: cmd/cascade/daemon (coverage-only addition for T-1's own
//
//	contract-named tests, no new production surface).
package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// TestBuildRPCServerRegistersFleetSessions proves buildDaemonRegistry
// (buildRPCServer's own composition-root helper) actually registers
// fleet.sessions.list, and that dispatching it returns the seeded
// session — reproducing exactly the pre-fix failure this ticket closes
// (P1-E18-W4-S40-T5's blocked journal: "sessions.RegisterHandlers( is
// called only from ... rpc_test.go — never from
// cmd/cascade/daemon_unix_run*.go", which would have left
// registry.Registered false here).
func TestBuildRPCServerRegistersFleetSessions(t *testing.T) {
	clock := runtime.NewSystemClock()
	store := storetest.NewMemStore()
	bus := events.New(store, clock)
	seed := sessions.New(store, clock, bus)
	if err := seed.Upsert(context.Background(), sessions.SessionRecord{SessionID: "registry-1", Harness: "claude", State: "running"}); err != nil {
		t.Fatalf("seed Upsert: %v", err)
	}

	registry, _, _, _, err := buildDaemonRegistry(bus, clock, nil, daemon.Settings{}, fakeDaemonPaths{root: t.TempDir()}, nil, store)
	if err != nil {
		t.Fatalf("buildDaemonRegistry: %v", err)
	}
	// Dispatch first, so an unregistered method fails with the real wire
	// error (JSON-RPC -32601 method-not-found), the pre-fix reproduction.
	req := &rpc.Request{JSONRPC: "2.0", Method: sessions.MethodList}
	result, errObj := registry.Dispatch(context.Background(), req)
	if errObj != nil {
		t.Fatalf("Dispatch(%s) through the daemon registry: code %d, message %q", sessions.MethodList, errObj.Code, errObj.Message)
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
	if len(decoded.Sessions) != 1 || decoded.Sessions[0].SessionID != "registry-1" {
		t.Fatalf("fleet.sessions.list() through the daemon's own registry = %+v, want [registry-1]", decoded.Sessions)
	}
}

// TestBuildRPCServerMountsFleetSessionsSSE proves the events handler
// buildDaemonRegistry assembles (the same value buildRPCServer mounts at
// rpc.EventsPath) is actually a *rpc.SSEMux wired with the daemon-wide
// Default and a fleet.sessions Topics entry bound to a real
// *sessions.SSEHandler — the composition-root wiring this ticket's own
// contract names ("buildRPCServer mounts SSEMux{Default: sse, Topics:
// {sessions.Topic: sessions.NewSSEHandler(bus, clock)}} at
// rpc.EventsPath"), and that the guard refuses before any topic dispatch.
// See this file's header comment for where the guarded dispatch is proven.
func TestBuildRPCServerMountsFleetSessionsSSE(t *testing.T) {
	clock := runtime.NewSystemClock()
	store := storetest.NewMemStore()
	bus := events.New(store, clock)
	_, _, _, eventsHandler, err := buildDaemonRegistry(bus, clock, nil, daemon.Settings{}, fakeDaemonPaths{root: t.TempDir()}, nil, store)
	if err != nil {
		t.Fatalf("buildDaemonRegistry: %v", err)
	}

	mux, ok := eventsHandler.(*rpc.SSEMux)
	if !ok {
		t.Fatalf("eventsHandler = %T, want *rpc.SSEMux", eventsHandler)
	}
	if mux.Default == nil {
		t.Fatal("SSEMux.Default is nil, want the daemon-wide SSEHandler bound")
	}
	if _, ok := mux.Default.(*rpc.SSEHandler); !ok {
		t.Fatalf("SSEMux.Default = %T, want *rpc.SSEHandler", mux.Default)
	}
	topicHandler, ok := mux.Topics[sessions.Topic]
	if !ok {
		t.Fatalf("SSEMux.Topics[%q] missing, want the fleet.sessions handler mounted", sessions.Topic)
	}
	if _, ok := topicHandler.(*sessions.SSEHandler); !ok {
		t.Fatalf("SSEMux.Topics[%q] = %T, want *sessions.SSEHandler", sessions.Topic, topicHandler)
	}
	if len(mux.Topics) != 1 {
		t.Fatalf("SSEMux.Topics has %d entries, want exactly 1 (fleet.sessions)", len(mux.Topics))
	}

	req := httptest.NewRequest("GET", rpc.EventsPath+"?topic="+sessions.Topic, nil)
	req.Host = "unix"
	rec := httptest.NewRecorder()
	eventsHandler.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("topic=%s without owner credentials: status = %d, want 403 from the mux's guard", sessions.Topic, rec.Code)
	}
}
