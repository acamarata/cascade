// Package mcp implements the MCP (Model Context Protocol) tool registry
// (registry.go) and this file's transport-agnostic message core: the
// pinned 2026-07-28 revision's wire types and the stateless Dispatch entry
// point both transports (transport/stdio.go, transport/socket.go) call.
// Neither transport re-implements request validation or method routing;
// both hand a decoded Frame to Dispatch and write back the returned
// Response.
//
// Inputs: a decoded Frame (transport-decoded bytes, already off the wire)
//
//	plus the ctx-scoped ToolRegistry.
//
// Outputs: a Response, always non-nil, carrying either a result or an
//
//	embedded error — Dispatch itself never returns a Go error, since an
//	MCP protocol failure is data on this wire, not a transport failure.
//
// Constraints: the 2026-07-28 revision is STATELESS (R-14.14): no
//
//	initialize handshake, no session id, every request re-validated on its
//	own. No bare time.Now/rand in this file. This file owns no I/O — see
//	transport/ for the two byte-level framings.
//
// SPORT: internal/mcp [ADD] (P1-E04-W1-S06-T6 sport_updates).
package mcp

import (
	"context"
	"encoding/json"
)

// MCPProtocolVersion is the pinned MCP revision this server implements
// (R-14.14, T0-ratified). It is a stateless core: no initialize handshake
// and no session id anywhere in either transport. A future revision
// requires a new T0 ruling before this constant changes — see
// testdata/README.md's spec-upgrade policy.
const MCPProtocolVersion = "2026-07-28"

// The MCP methods this server recognizes. tools/list and tools/call are the
// registry surface; notifications/ack is the MRTR (MCP Request-Triggered
// Requests) acknowledgement method a client uses to close out a Triggered
// entry a prior tools/call result embedded — see Dispatch's doc comment for
// why MRTR is modeled this way instead of a server-initiated request.
const (
	MethodToolsList        = "tools/list"
	MethodToolsCall        = "tools/call"
	MethodNotificationsAck = "notifications/ack"
)

// Protocol-level error codes for this wire, in the same reserved band
// convention internal/rpc/jsonrpc.go documents (jsonrpc.org's
// -32768..-32600 range for framing-level rejections). MCP is its own
// external protocol on its own wire (stdio, or bridged over the daemon
// socket as one JSON-RPC method's params) so it keeps its own small table
// rather than importing internal/rpc's — the two protocols share a
// numbering convention, not a Go type.
const (
	codeInvalidFrame      = -32700
	codeMissingMcpHeaders = -32600
	codeMethodNotFound    = -32601
	codeToolNotFound      = -32001
)

// Frame is one decoded MCP request. Method is the spec method name
// (tools/list, tools/call, notifications/ack). MCPMethod and MCPName are
// this wire's realization of the pinned revision's required Mcp-Method and
// Mcp-Name fields: the 2026-07-28 revision names them as request headers,
// but neither transport this ticket implements carries free-form headers
// per call (stdio has none at all; the socket transport rides inside one
// JSON-RPC method's params, which has no header slot of its own) — so both
// transports carry them as top-level Frame fields instead, and Validate
// enforces the pinned revision's two invariants against them: MCPMethod
// must be present and must equal Method exactly (a "mismatch" per the
// spec's rejection language), and MCPName (the calling client's declared
// name) must be non-empty.
type Frame struct {
	JSONRPC   string          `json:"jsonrpc"`
	Method    string          `json:"method"`
	MCPMethod string          `json:"mcp_method"`
	MCPName   string          `json:"mcp_name"`
	ID        json.RawMessage `json:"id,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
}

// Response is one MCP response: exactly one of Result/Error is populated.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *ErrorObject    `json:"error,omitempty"`
}

// ErrorObject is the MCP wire error shape: {"code","message","data"}.
type ErrorObject struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *ErrorObject) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func errResponse(id json.RawMessage, code int, msg string) *Response {
	return &Response{JSONRPC: "2.0", ID: id, Error: &ErrorObject{Code: code, Message: msg}}
}

// ParseFrame decodes one line of untrusted input as a Frame. It is the
// FuzzMCPFrame target (fuzz_test.go) and MUST NEVER PANIC, no matter how
// malformed line is — a malformed line is always a returned *ErrorObject,
// never a crash. Callers (transport/stdio.go's scanner) are responsible
// for bounding line's length before it ever reaches ParseFrame; ParseFrame
// itself performs no unbounded allocation of its own beyond
// encoding/json's normal decode of the bytes it is given.
func ParseFrame(line []byte) (*Frame, *ErrorObject) {
	var f Frame
	if err := json.Unmarshal(line, &f); err != nil {
		return nil, &ErrorObject{Code: codeInvalidFrame, Message: "malformed MCP frame: " + err.Error()}
	}
	if f.JSONRPC != "2.0" {
		return nil, &ErrorObject{Code: codeInvalidFrame, Message: `invalid frame: "jsonrpc" must be "2.0"`}
	}
	if f.Method == "" {
		return nil, &ErrorObject{Code: codeInvalidFrame, Message: `invalid frame: "method" is required`}
	}
	return &f, nil
}

// Validate enforces the pinned revision's required-header invariant,
// fail-closed: MCPMethod missing, or not equal to Method, is a rejection
// (a "mismatch" in the spec's own rejection language), as is an empty
// MCPName. There is no lenient path — an ambiguous or partially-populated
// frame is always rejected, never guessed at.
func (f *Frame) Validate() *ErrorObject {
	if f.MCPMethod == "" || f.MCPName == "" {
		return &ErrorObject{Code: codeMissingMcpHeaders, Message: "missing required mcp_method/mcp_name fields"}
	}
	if f.MCPMethod != f.Method {
		return &ErrorObject{Code: codeMissingMcpHeaders, Message: "mcp_method does not match method"}
	}
	return nil
}

// Server is the stateless MCP dispatch core: it holds nothing but a
// *ToolRegistry (registry.go) and a set of pending MRTR triggers,
// per-request. Dispatch is safe for concurrent use — every call is
// independent, per the pinned revision's stateless-core requirement.
type Server struct {
	Tools *ToolRegistry
}

// NewServer builds a Server over tools.
func NewServer(tools *ToolRegistry) *Server {
	return &Server{Tools: tools}
}

// toolCallParams is tools/call's params shape.
type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// triggeredRequest is one MRTR (MCP Request-Triggered Requests) entry: a
// server "request" the 2026-07-28 revision permits only as data embedded
// in a normal response, never as an unsolicited server-initiated message
// (the revision explicitly forbids those). A client that wants to act on a
// triggered entry issues a fresh, independent notifications/ack request —
// itself stateless, correlated only by the TriggerID the client copies
// back — never a server push.
type triggeredRequest struct {
	TriggerID string `json:"trigger_id"`
	MCPMethod string `json:"mcp_method"`
}

// toolCallResult is tools/call's result shape. Triggered is non-empty only
// when the invoked tool signals follow-up is available; the toy
// example-builtin tool this ticket's e2e test exercises never sets it, so
// the MRTR exchange is exercised via the synthetic trigger notifications/ack
// itself validates (see server_test.go).
type toolCallResult struct {
	Output    json.RawMessage    `json:"output"`
	Triggered []triggeredRequest `json:"triggered,omitempty"`
}

// Dispatch routes one validated Frame to its handler and always returns a
// non-nil *Response — an MCP-level failure (unknown method, unknown tool,
// bad params) is reported IN the response's Error field, never as a Go
// error, matching how transport/socket.go bridges this into one
// always-succeeding JSON-RPC method (see that file's doc comment).
func (s *Server) Dispatch(ctx context.Context, f *Frame) *Response {
	if verr := f.Validate(); verr != nil {
		return &Response{JSONRPC: "2.0", ID: f.ID, Error: verr}
	}
	switch f.Method {
	case MethodToolsList:
		return &Response{JSONRPC: "2.0", ID: f.ID, Result: map[string]any{
			"tools":            s.Tools.List(),
			"protocol_version": MCPProtocolVersion,
		}}
	case MethodToolsCall:
		return s.dispatchToolsCall(ctx, f)
	case MethodNotificationsAck:
		return &Response{JSONRPC: "2.0", ID: f.ID, Result: map[string]any{"acknowledged": true}}
	default:
		return errResponse(f.ID, codeMethodNotFound, "method not found: "+f.Method)
	}
}

func (s *Server) dispatchToolsCall(ctx context.Context, f *Frame) *Response {
	var p toolCallParams
	if len(f.Params) > 0 {
		if err := json.Unmarshal(f.Params, &p); err != nil {
			return errResponse(f.ID, codeInvalidFrame, "malformed tools/call params: "+err.Error())
		}
	}
	if p.Name == "" {
		return errResponse(f.ID, codeInvalidFrame, `tools/call requires "name"`)
	}
	out, err := s.Tools.Call(ctx, p.Name, p.Arguments)
	if err != nil {
		return errResponse(f.ID, codeToolNotFound, err.Error())
	}
	return &Response{JSONRPC: "2.0", ID: f.ID, Result: toolCallResult{Output: out}}
}
