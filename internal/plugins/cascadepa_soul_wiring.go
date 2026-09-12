package plugins

import (
	"context"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

// Purpose (this file): the composition-root wiring for cascade-pa's
//
//	`/soul edit` in-chat command (P1-E22-W5-S47-T3): injects a real
//	internal/client-backed implementation of
//	plugins/cascade-pa.SoulChatClient, matching
//	cascadepa_wiring.go's identical pattern for the ordinary chat turn's
//	Client seam. Declared here, not in soul_chat.go's own ticket
//	files_scope, because this package (internal/plugins) is the one
//	place allowed to import both internal/** and plugins/** (Art.10.2) —
//	the same reasoning cascadepa_wiring.go's own doc comment already
//	states for the identical class of call site, and the same recorded,
//	disclosed deviation FIX-cascade-chat-client-wiring.md made necessary
//	for T/S-43.T3's Client seam.
//
// Inputs: none at import time; each call resolves the daemon socket path
//
//	lazily, matching cascadepa_wiring.go's own deferred-resolution
//	pattern.
//
// Outputs: registers a *cascadePASoulClient with cascade-pa via
//
//	SetSoulChatClient. Show/Edit perform real memory.soul.show /
//	memory.soul.edit round trips over the unix socket.
//
// Constraints: this file mirrors cascadepa_wiring.go's wire-shape
//
//	duplication practice — internal/memory's own SoulShowResult/
//	SoulEditParams/SoulEditResult are imported directly here (this file
//	IS internal/, so no boundary forbids it) for the method-name
//	constants (MethodSoulShow, MethodSoulEdit), while the plugin-side
//	SoulChatView/SoulChatDocument/SoulChatEditResult types stay
//	independent of internal/memory, per Art.10.2.
//
// SPORT: internal/plugins:cascadepa-soul-wiring (ADD) — P1-E22-W5-S47-T3.
func init() {
	cascadepa.SetSoulChatClient(newCascadePASoulClient(client.UnixDialer, cascadePAClientTimeout, runtime.NewDefaultPathProvider))
}

// cascadePASoulClient adapts internal/client's transport to
// plugins/cascade-pa.SoulChatClient.
type cascadePASoulClient struct {
	dial         client.DialFunc
	timeout      time.Duration
	resolvePaths pathResolver
}

func newCascadePASoulClient(dial client.DialFunc, timeout time.Duration, resolvePaths pathResolver) *cascadePASoulClient {
	return &cascadePASoulClient{dial: dial, timeout: timeout, resolvePaths: resolvePaths}
}

func (c *cascadePASoulClient) rpcClient() (*client.Client, error) {
	paths, err := c.resolvePaths()
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "cascade chat: /soul edit: resolve daemon socket path")
	}
	return client.New(paths.SocketPath(), c.dial, c.timeout), nil
}

// Show performs a real memory.soul.show round trip.
func (c *cascadePASoulClient) Show(ctx context.Context) (cascadepa.SoulChatView, error) {
	rpcClient, err := c.rpcClient()
	if err != nil {
		return cascadepa.SoulChatView{}, err
	}
	var res memory.SoulShowResult
	if err := rpcClient.Do(ctx, memory.MethodSoulShow, memory.SoulShowParams{}, &res); err != nil {
		return cascadepa.SoulChatView{}, err
	}
	return cascadepa.SoulChatView{
		Body: res.Body, Schema: res.Schema, Version: res.Version, Diverged: res.Diverged,
	}, nil
}

// Edit performs a real memory.soul.edit round trip through route (a)'s own
// RPC method — the same one route (c) uses, per this ticket's "no fourth
// write path" constraint.
func (c *cascadePASoulClient) Edit(ctx context.Context, doc cascadepa.SoulChatDocument) (cascadepa.SoulChatEditResult, error) {
	rpcClient, err := c.rpcClient()
	if err != nil {
		return cascadepa.SoulChatEditResult{}, err
	}
	var res memory.SoulEditResult
	params := memory.SoulEditParams{Body: doc.Body, Schema: doc.Schema}
	if err := rpcClient.Do(ctx, memory.MethodSoulEdit, params, &res); err != nil {
		return cascadepa.SoulChatEditResult{}, err
	}
	return cascadepa.SoulChatEditResult{Version: res.Version}, nil
}
