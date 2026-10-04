//go:build !windows

// Purpose: registers the socket-mcp daemon wiring: the MCP dispatcher on the daemon's own socket, last so coretools sees the finished method table.
// SPORT: cmd/cascade daemon registrations (P1-CORE-01).
package main

import (
	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/mcp/transport"
)

var _ = registerDaemonWiring(daemonRegistration{
	Name: "socket-mcp", Phase: phaseMCPLast, Order: 10,
	Wire: func(w *daemonWiring) error {
		return transport.RegisterSocketMCP(w.Registry,
			mcp.NewServer(daemonMCPToolRegistry(w.Registry, mcpFilterFromOptions(w.Opts))))
	},
})
