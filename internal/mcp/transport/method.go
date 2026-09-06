// Purpose: the JSON-RPC method name the socket MCP transport is bridged
//
//	through, declared on EVERY platform.
//
// Inputs: none.
// Outputs: the MCPMethod constant.
// Constraints: this file carries no build tag on purpose. The constant
//
//	used to live in socket.go (//go:build !windows) while socket_test.go
//	asserts the windows behaviour of RegisterSocketMCP -- that the tier-2
//	refusal in socket_windows.go registers NOTHING and returns
//	KindUnsupported. That assertion needs the method name to check the
//	registry was left untouched, so on windows the test referred to a
//	symbol its own build did not have and the whole package failed to
//	compile there with `undefined: transport.MCPMethod`. The name is a
//	wire constant, identical on every platform; only the registration
//	behaviour is platform-specific, and that stays split across socket.go
//	and socket_windows.go.
//
// SPORT: internal/mcp/transport [ADD] (P1-E04-W1-S06-T6 sport_updates).

package transport

// MCPMethod is the JSON-RPC method name MCP requests are bridged through
// on the daemon socket. It is declared unconditionally: windows refuses to
// REGISTER it (socket_windows.go), which is a different thing from the
// name not existing.
const MCPMethod = "mcp.dispatch"
