//go:build windows

// Purpose: the windows tier-2 sibling of socket.go — the socket MCP
//
//	transport is NOT supported on windows (06-FORGE-SPEC §2's tier-2
//	definition) and must refuse clearly rather than silently succeed.
//
// Constraints: RegisterSocketMCP on this platform registers nothing and
//
//	always returns a documented, typed error — fail loud at startup,
//	before any client ever attempts a connection, rather than accepting
//	registration and failing confusingly per-request later. stdio remains
//	fully available on windows; only this transport is refused.
//
// SPORT: internal/mcp/transport [ADD] (P1-E04-W1-S06-T6 sport_updates).
package transport

import (
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// RegisterSocketMCP refuses on windows: the socket MCP transport is
// tier-2-unsupported there. registry is left untouched — mcp.dispatch is
// never registered — and the returned error carries KindUnsupported so
// callers (cmd/cascade/mcp.go's `cascade mcp serve --socket`) surface the
// documented refusal message and correct exit status rather than a bare
// internal error.
func RegisterSocketMCP(registry *rpc.Registry, dispatcher Dispatcher) error {
	_ = registry
	_ = dispatcher
	return cascade.New(cascade.KindUnsupported,
		"the MCP socket transport is not supported on windows; use --stdio instead")
}
