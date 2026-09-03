//go:build !windows

// Package transport implements the MCP transports: this file, the
// unix-socket transport — MCP registered as one method namespace
// ("mcp.dispatch") on the existing D/S-06.T3 JSON-RPC frame, rather than a
// second listener of its own, per 06-FORGE-SPEC §2 (IPC = HTTP/1.1 unix
// socket, JSON-RPC 2.0).
//
// Inputs: an *rpc.Registry (already constructed and about to be served by
//
//	the daemon composition root) and a Dispatcher.
//
// Outputs: RegisterSocketMCP mutates registry in place; it returns an
//
//	error only on this platform's windows sibling (socket_windows.go).
//
// Constraints: the bridged handler always succeeds at the JSON-RPC layer —
//
//	an MCP-level failure (unknown method, unknown tool, bad params) is
//	reported INSIDE the returned mcp.Response's Error field, never surfaced
//	as a HandlerFunc error, because it is not a transport-layer failure:
//	the JSON-RPC call to mcp.dispatch genuinely succeeded, it is the MCP
//	request it carried that the pinned revision's own error shape reports
//	on. Stateless per request: this handler holds no state across calls.
//
// SPORT: internal/mcp/transport [ADD] (P1-E04-W1-S06-T6 sport_updates).
package transport

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/rpc"
)

// MCPMethod is the JSON-RPC method name MCP requests are bridged through
// on the daemon socket.
const MCPMethod = "mcp.dispatch"

// RegisterSocketMCP registers dispatcher as registry's mcp.dispatch
// handler. On non-windows platforms this always succeeds; see
// socket_windows.go for the tier-2 refusal this same function name
// performs on windows instead of registering anything.
func RegisterSocketMCP(registry *rpc.Registry, dispatcher Dispatcher) error {
	registry.Register(MCPMethod, func(ctx context.Context, params json.RawMessage) (any, error) {
		f, perr := mcp.ParseFrame(params)
		if perr != nil {
			return &mcp.Response{JSONRPC: "2.0", Error: perr}, nil
		}
		return dispatcher.Dispatch(ctx, f), nil
	})
	return nil
}
