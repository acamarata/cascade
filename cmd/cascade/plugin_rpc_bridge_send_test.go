// Purpose: fix18's composition-root proof that wireCascadePABridge
//
//	REGISTERS "bridge.send" on the daemon's real *rpc.Registry even when the
//	bridge is NOT enabled (no module manifest at all), and that a real
//	Dispatch then answers "skipped" rather than "method not found". Only the
//	socket is absent (Art.7.2: no "net" in an untagged test); the real
//	socket round trip is internal/integration's cpa_send_bridge_test.go.
//
// Falsifiable: drop plugins.RegisterBridgeSend(registry) from
// mountCascadePABridge and TestWireCascadePABridge_RegistersBridgeSend goes
// red (method not found). The enabled-bridge wiring proofs live in
// plugin_rpc_bridge_enabled_test.go.
//
// SPORT: cmd/cascade plugin_rpc_bridge_send/TEST (ADD) — fix18 rounds 2-3.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

// newBridgeSendTestRegistry builds the minimal real collaborators
// wireCascadePABridge needs, over a throwaway data directory with no
// module manifest written (the bridge is NOT enabled — proving
// registration is unconditional, this file's own header).
func newBridgeSendTestRegistry(t *testing.T) (*rpc.Registry, runtime.Clock) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/data", 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	clock := runtime.NewSystemClock()
	registry := rpc.NewRegistry()
	manifest := daemon.NewManifest(nil, clock)
	bus := events.New(storetest.NewMemStore(), clock)
	wireCascadePABridge(context.Background(), registry, manifest, fakeDaemonPaths{root: dir}, clock, bus, nil)
	return registry, clock
}

// TestWireCascadePABridge_RegistersBridgeSend proves bridge.send is a real,
// dispatchable method the moment wireCascadePABridge runs — even with no
// module manifest at all (the disabled-bridge path), matching pa.pair_code's
// own "always registered, answers by state" contract.
func TestWireCascadePABridge_RegistersBridgeSend(t *testing.T) {
	registry, _ := newBridgeSendTestRegistry(t)
	if !registry.Registered(plugins.MethodBridgeSend) {
		t.Fatal("bridge.send was never registered")
	}

	params, err := json.Marshal(plugins.BridgeSendParams{
		ThreadID: "bridge-telegram:1", Content: "x", Sensitivity: "public",
	})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	raw, errObj := registry.Dispatch(context.Background(), &rpc.Request{Method: plugins.MethodBridgeSend, Params: params})
	if errObj != nil {
		t.Fatalf("Dispatch bridge.send: %+v", errObj)
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var res plugins.BridgeSendResult
	if err := json.Unmarshal(encoded, &res); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if res.Mirrored || res.Refused || res.Reason == "" {
		t.Fatalf("result = %+v, want skipped with a reason when no bridge is enabled", res)
	}
}

// drainFailsBridge is a ChatBridge whose Drain fails; every other method is
// the embedded nil interface and must never be reached by the stop path.
type drainFailsBridge struct {
	cascadepa.ChatBridge
	err error
}

func (d drainFailsBridge) Drain(context.Context) error { return d.err }

// TestStopCascadePABridge_RunsEveryStepAndReturnsTheFirstError proves the
// shutdown still stops the module and closes its state when the drain step
// fails, and that the drain's error (the first failure, in order unwire,
// drain, stop) is the one returned, with no raw joined error.
func TestStopCascadePABridge_RunsEveryStepAndReturnsTheFirstError(t *testing.T) {
	drainErr, stopErr := errors.New("drain budget ran out"), errors.New("module stop failed")
	stopped := false
	rt := &plugins.BridgeRuntime{
		ChatBridge: drainFailsBridge{err: drainErr},
		Stop:       func(context.Context) error { stopped = true; return stopErr },
	}
	got := stopCascadePABridge(context.Background(), rt)
	if !stopped {
		t.Fatal("rt.Stop did not run after the drain failed; the durable state would stay open")
	}
	if !errors.Is(got, drainErr) || errors.Is(got, stopErr) {
		t.Fatalf("stopCascadePABridge = %v, want exactly the drain error", got)
	}
	if err := stopCascadePABridge(context.Background(), &plugins.BridgeRuntime{
		ChatBridge: drainFailsBridge{},
		Stop:       func(context.Context) error { return nil },
	}); err != nil {
		t.Fatalf("stopCascadePABridge with no failing step = %v, want nil", err)
	}
}
