package plugins

// Purpose (this file): proves the owner-DM guard's refusals reach the bridge
//   journal in production wiring, not only a recording sink in package
//   telegram's own tests.

import (
	"context"
	"testing"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
	"github.com/acamarata/cascade/plugins/cascade-pa/telegram"
)

// TestNewCascadePABridgeChatHandler_AuditsOwnerGuardRefusals sends to a chat
// that is not the paired owner's through the module's own client: the guard
// refuses, and one bridge.refused is published over the wired journal.
func TestNewCascadePABridgeChatHandler_AuditsOwnerGuardRefusals(t *testing.T) {
	deps, bus, dataDir := enabledBridgeDeps(t)
	ctx := context.Background()
	state, err := openBridgeState(ctx, dataDir)
	if err != nil {
		t.Fatalf("openBridgeState: %v", err)
	}
	closeBridgeState(t, state)
	stateDB, ok := bridgeStateSQLDB(state)
	if !ok {
		t.Fatal("bridgeStateSQLDB: state does not expose a reusable sqlite connection")
	}
	pairKey, err := cascadepa.DerivePairCodeKey(syntheticBotToken)
	if err != nil {
		t.Fatalf("DerivePairCodeKey: %v", err)
	}
	stores := cascadepa.NewStores(deps.Clock, newBridgeDeviceRegistrar(dataDir), state, pairKey)
	module := telegram.NewModule(syntheticBotToken, nil, nil, bridgeElevationPolicy{}, stores, nil, nil, nil)
	if _, err := NewCascadePABridgeChatHandler(ctx,
		ChatWiringDeps{DataDir: dataDir, DB: stateDB, Events: bus}, module, stores.Binding, "tg-test", deps.Clock); err != nil {
		t.Fatalf("NewCascadePABridgeChatHandler: %v", err)
	}

	if err := module.Client().SendMessage(ctx, 222, cascadepa.TierInternal, "hello"); err == nil {
		t.Fatal("a send to a chat that is not the paired owner chat succeeded")
	}
	got := bus.events()
	if len(got) != 1 || got[0].kind != EventKindBridgeRefused || !containsAll(got[0].payload, "chat-not-paired") {
		t.Fatalf("published %+v, want one bridge.refused with reason chat-not-paired", got)
	}
}
