// Purpose: fix18 round 3's composition-root proofs over a REAL enabled
//
//	bridge (fixtures: plugin_rpc_bridge_enabled_helpers_test.go), driven
//	through mountCascadePABridge and the daemon's real *rpc.Registry:
//	- bridge.send on a bound internal thread makes exactly ONE Bot API
//	  sendMessage call (goes RED if WireChatBridge is dropped here or the
//	  runtime's ChatBridge is not kept: both answer "skipped");
//	- the registered Stop runs in L1's order: while a Send is in flight the
//	  drain waits, a bridge.send arriving meanwhile is refused typed-unavailable
//	  with no transport call and no false bridge.refused event, and after
//	  Stop bridge.send stays unavailable.
//
// SPORT: cmd/cascade plugin_rpc_bridge_enabled/TEST (ADD) — fix18 round 3.
package main

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
)

// mountEnabledBridge mounts env's bridge and returns the registry plus a
// stop func that cancels the daemon context and waits for the drain.
func mountEnabledBridge(t *testing.T, env *enabledBridgeEnv) (*rpc.Registry, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	clock := runtime.NewSystemClock()
	registry := rpc.NewRegistry()
	manifest := daemon.NewManifest(nil, clock)
	mountCascadePABridge(ctx, registry, manifest, env.deps, clock)
	var once sync.Once
	stop := func() { once.Do(func() { cancel(); manifest.Wait() }) }
	t.Cleanup(stop)
	return registry, stop
}

// dispatchBridgeSend calls bridge.send through the registry, bounded so a
// call that wrongly reaches a held transport fails instead of hanging.
func dispatchBridgeSend(t *testing.T, registry *rpc.Registry, sensitivity string) (plugins.BridgeSendResult, *rpc.ErrorObject) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	params, err := json.Marshal(plugins.BridgeSendParams{
		ThreadID: enabledBridgeThread, Content: "hello owner", Sensitivity: sensitivity,
	})
	if err != nil {
		t.Errorf("marshal params: %v", err)
		return plugins.BridgeSendResult{}, nil
	}
	raw, errObj := registry.Dispatch(ctx, &rpc.Request{Method: plugins.MethodBridgeSend, Params: params})
	if errObj != nil {
		return plugins.BridgeSendResult{}, errObj
	}
	var res plugins.BridgeSendResult
	encoded, err := json.Marshal(raw)
	if err == nil {
		err = json.Unmarshal(encoded, &res)
	}
	if err != nil {
		t.Errorf("decode bridge.send result: %v", err)
	}
	return res, nil
}

// bridgeRefusedCount reads the bridge namespace back off the real bus.
func bridgeRefusedCount(t *testing.T, env *enabledBridgeEnv) int {
	t.Helper()
	evs, err := env.bus.Replay(context.Background(), "bridge", 0)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	n := 0
	for _, ev := range evs {
		if string(ev.Kind) == "bridge.refused" {
			n++
		}
	}
	return n
}

func TestMountCascadePABridge_BridgeSendReachesTheTransportOnce(t *testing.T) {
	skipWhenTheDaemonIsRefusedHere(t)
	env := newEnabledBridgeEnv(t)
	registry, _ := mountEnabledBridge(t, env)

	res, errObj := dispatchBridgeSend(t, registry, "internal")
	if errObj != nil || !res.Mirrored {
		t.Fatalf("bridge.send = %+v, %+v; want Mirrored", res, errObj)
	}
	if n := env.transport.sendCount(); n != 1 {
		t.Fatalf("sendMessage calls = %d, want exactly 1", n)
	}
	body := env.transport.sends[0]
	if !strings.Contains(body, `"chat_id":`+enabledBridgeChat) || !strings.Contains(body, `"text":"hello owner"`) {
		t.Fatalf("sendMessage body = %s, want the bound chat and the turn content", body)
	}
}

func TestMountCascadePABridge_StopUnwiresFirstAndDrainsInFlight(t *testing.T) {
	skipWhenTheDaemonIsRefusedHere(t)
	env := newEnabledBridgeEnv(t)
	env.transport.hold = make(chan struct{})
	registry, stop := mountEnabledBridge(t, env)

	first := make(chan plugins.BridgeSendResult, 1)
	go func() {
		res, _ := dispatchBridgeSend(t, registry, "internal")
		first <- res
	}()
	select {
	case <-env.transport.arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("the first bridge.send never reached the transport; the bridge is not armed")
	}

	stopped := make(chan struct{})
	go func() { stop(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("Stop finished while a Send was still in flight; it must drain first")
	case <-time.After(200 * time.Millisecond):
	}

	refusedBefore := bridgeRefusedCount(t, env)
	if _, errObj := dispatchBridgeSend(t, registry, "internal"); errObj == nil ||
		!strings.Contains(errObj.Message, "bridge.send is unavailable") {
		t.Fatalf("bridge.send during shutdown = %+v, want typed unavailable", errObj)
	}
	if n := env.transport.sendCount(); n != 1 {
		t.Fatalf("sendMessage calls = %d during shutdown, want still 1", n)
	}
	if n := bridgeRefusedCount(t, env); n != refusedBefore {
		t.Fatalf("bridge.refused events went %d -> %d during shutdown, want no false refusal", refusedBefore, n)
	}

	close(env.transport.hold)
	if res := <-first; !res.Mirrored {
		t.Fatalf("the in-flight bridge.send = %+v, want it to complete Mirrored", res)
	}
	<-stopped
	if _, errObj := dispatchBridgeSend(t, registry, "internal"); errObj == nil ||
		!strings.Contains(errObj.Message, "bridge.send is unavailable") {
		t.Fatalf("bridge.send after Stop = %+v, want typed unavailable", errObj)
	}
}
