// Purpose: the MCP `initialize` handshake and `tools/list` result shape —
//
//	the two message bodies whose exact form is dictated by what a real
//	client sends and expects, not by prose.
//
// WHY A SEPARATE FILE: server.go owns framing and routing; this owns the
//
//	two negotiated payloads. They change for different reasons — a new
//	protocol revision moves this file, a new method moves that one.
//
// Inputs: the client's initialize params, and the tool registry.
// Outputs: InitializeResult and the tools/list body.
// Constraints: version negotiation NEVER fails the handshake. MCP's rule is
//
//	that a server which cannot speak the client's revision answers with the
//	one it DOES speak and lets the client decide whether to continue.
//	Refusing here (the -32022 an earlier ruling described) would reject the
//	only client that exists — see R-14.246.
//
// SPORT: internal/mcp:initialize (ADD) — P1-E04-W4-S86-T1.

package mcp

import (
	"encoding/json"

	"github.com/acamarata/cascade/internal/buildinfo"
)

// serverName is how cascade identifies itself in the handshake.
const serverName = "cascade"

// initializeParams is the client's half of the handshake, in the shape the
// captured frame actually uses: everything in `params`, no `_meta` layer
// anywhere. See testdata/README.md for which client sent it.
type initializeParams struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Capabilities    json.RawMessage `json:"capabilities,omitempty"`
	ClientInfo      clientInfo      `json:"clientInfo"`
}

// clientInfo is the calling client's self-description. Its Name is what the
// policy filter uses as the client identity the old wire took from the
// invented `mcp_name` field.
type clientInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version,omitempty"`
}

// serverInfo is cascade's half. Version is the BUILD version, not the
// protocol revision: a client reporting which cascade it is talking to
// needs the artifact tag, and the protocol version is already its own
// field one level up. The first capture had the protocol version here,
// which would have told an operator nothing they did not already know.
type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// toolsCapability declares what this server offers under `tools`.
// listChanged is false and honest: nothing here pushes a change
// notification, and claiming otherwise would have a client wait for one.
type toolsCapability struct {
	ListChanged bool `json:"listChanged"`
}

// serverCapabilities is the capabilities object. Only `tools` is declared,
// because tools are the only surface this server has: declaring an empty
// `resources` or `prompts` would advertise a capability whose every call
// would then be method-not-found.
type serverCapabilities struct {
	Tools toolsCapability `json:"tools"`
}

// initializeResult is the server's handshake reply.
type initializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    serverCapabilities `json:"capabilities"`
	ServerInfo      serverInfo         `json:"serverInfo"`
}

// dispatchInitialize answers the handshake.
//
// Malformed params are NOT fatal: the handshake's only required field is
// the version, and a client that sent something this decoder cannot read
// still learns which revision cascade speaks, which is the whole point of
// the exchange. Refusing here would be refusing to tell a client why they
// disagree.
func (s *Server) dispatchInitialize(f *Frame) *Response {
	var p initializeParams
	if len(f.Params) > 0 {
		_ = json.Unmarshal(f.Params, &p)
	}
	return &Response{JSONRPC: "2.0", ID: f.ID, Result: initializeResult{
		ProtocolVersion: negotiateVersion(p.ProtocolVersion),
		Capabilities:    serverCapabilities{Tools: toolsCapability{ListChanged: false}},
		ServerInfo:      serverInfo{Name: serverName, Version: buildinfo.Version},
	}}
}

// negotiateVersion returns the revision this exchange will use.
//
// When the client offers the revision cascade speaks, that is the answer.
// When it offers anything else — including nothing — the answer is
// cascade's own, and the CLIENT decides whether to proceed. That is MCP's
// rule and it is also the only behaviour that degrades sensibly: a server
// that hung up on an unfamiliar version string would break on the next
// revision every client ships before this one does.
func negotiateVersion(offered string) string {
	if offered == MCPProtocolVersion {
		return offered
	}
	return MCPProtocolVersion
}

// toolDescriptor is one entry of tools/list, in MCP's own shape.
//
// InputSchema is REQUIRED by the protocol and is emitted as a permissive
// object schema, because the plugin manifest declares no per-tool schema
// today. That is a real gap, stated rather than papered over: a tool whose
// arguments are undescribed is one the model must guess at. The manifest
// gaining a schema field is the fix; inventing one here would be worse,
// since it would describe arguments no handler actually reads.
type toolDescriptor struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// toolsListBody is tools/list's result.
type toolsListBody struct {
	Tools []toolDescriptor `json:"tools"`
}

// toolsListResult renders the registry for the wire.
//
// The registry already returns its tools sorted by name, so the order here
// is deterministic without re-sorting — a client caching this list must see
// the same bytes for the same set.
func toolsListResult(tools *ToolRegistry) toolsListBody {
	listed := tools.List()
	out := make([]toolDescriptor, 0, len(listed))
	for _, t := range listed {
		out = append(out, toolDescriptor{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		})
	}
	return toolsListBody{Tools: out}
}
