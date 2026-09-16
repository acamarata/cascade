// Package mcp implements the MCP (Model Context Protocol) tool registry
// (registry.go) and this file's transport-agnostic message core: the wire
// types and the Dispatch entry point both transports
// (transport/stdio.go, transport/socket.go) call.
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
// THE WIRE IS WHAT A REAL CLIENT SENDS (R-14.246). Two rounds of this
//
//	package were built from a DESCRIPTION of a specification — first
//	R-14.14's, then R-14.238's correction of it — and both produced a
//	server that no real client could talk to. The protocol implemented here
//	is the one captured from the installed first-party client by
//	`cascade mcp serve --stdio --capture` (client and version named in
//	testdata/README.md): JSON-RPC 2.0, an `initialize`
//	handshake carrying protocolVersion/capabilities/clientInfo in `params`,
//	then notifications/initialized, tools/list and tools/call. There is no
//	`_meta` layer and no `server/discover`, because the real client sends
//	neither. The failing baseline (its frame, and the -32600 the old
//	dialect answered with) is committed under testdata/goldens/.
//
// Constraints: no bare time.Now/rand in this file. This file owns no I/O —
//
//	see transport/ for the two byte-level framings. A NOTIFICATION (a frame
//	with no id) gets NO response at all, per JSON-RPC 2.0; Dispatch returns
//	nil and the transport writes nothing.
//
// SPORT: internal/mcp [ADD] (P1-E04-W1-S06-T6); [CHANGE] P1-E04-W4-S86-T1.
package mcp

import (
	"context"
	"encoding/json"
)

// MCPProtocolVersion is the revision this server speaks: the one the real
// client offers (R-14.246). It is not a paraphrase of a published
// document — it is the value in the captured `initialize` frame.
const MCPProtocolVersion = "2025-11-25"

// The MCP request methods this server routes, all observed on the real
// wire. The NOTIFICATIONS a client sends — `notifications/initialized`
// after the handshake, `notifications/cancelled` to abandon a call — are
// deliberately not constants here: Dispatch answers every notification
// with silence by id, not by name, so naming them would create exported
// symbols with no production reader. The tests assert the literal strings
// a client sends, which is the stronger check anyway: renaming a constant
// cannot quietly change what they verify.
const (
	MethodInitialize = "initialize"
	MethodToolsList  = "tools/list"
	MethodToolsCall  = "tools/call"
)

// Protocol-level error codes for this wire, in the same reserved band
// convention internal/rpc/jsonrpc.go documents (jsonrpc.org's
// -32768..-32600 range for framing-level rejections). MCP is its own
// external protocol on its own wire (stdio, or bridged over the daemon
// socket as one JSON-RPC method's params) so it keeps its own small table
// rather than importing internal/rpc's — the two protocols share a
// numbering convention, not a Go type.
const (
	codeInvalidFrame   = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeToolNotFound   = -32001
)

// Frame is one decoded MCP request or notification.
//
// The mcp_method/mcp_name fields this struct used to carry were invented
// here, not read off any wire: no client sends them, and requiring them
// rejected every real frame with -32600. They are deleted (R-14.246).
type Frame struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	ID      json.RawMessage `json:"id,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// IsNotification reports whether f carries no id and so must receive no
// response at all (JSON-RPC 2.0). Answering a notification is a protocol
// violation a real client sees as an unsolicited message.
func (f *Frame) IsNotification() bool { return len(f.ID) == 0 || string(f.ID) == "null" }

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

// Server is the MCP dispatch core: it holds nothing but a *ToolRegistry
// (registry.go). Dispatch is safe for concurrent use — every call is
// independent.
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

// contentBlock is one item of an MCP tool result's content array. The
// protocol has no "raw JSON result" shape: a tool's output reaches the
// model as content blocks, and text is the block type every client
// renders.
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// toolCallResult is tools/call's result. IsError reports a TOOL failure —
// distinct from a protocol error, which travels in Response.Error. A tool
// that failed is a result the model can read and react to; a protocol
// error is not.
type toolCallResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError"`
}

// Dispatch routes one validated Frame to its handler and always returns a
// non-nil *Response — an MCP-level failure (unknown method, unknown tool,
// bad params) is reported IN the response's Error field, never as a Go
// error, matching how transport/socket.go bridges this into one
// always-succeeding JSON-RPC method (see that file's doc comment).
func (s *Server) Dispatch(ctx context.Context, f *Frame) *Response {
	// A notification is answered with silence, whatever it says. This runs
	// BEFORE method routing so an unknown notification cannot produce a
	// method-not-found response the client never asked for.
	if f.IsNotification() {
		return nil
	}
	switch f.Method {
	case MethodInitialize:
		return s.dispatchInitialize(f)
	case MethodToolsList:
		return &Response{JSONRPC: "2.0", ID: f.ID, Result: toolsListResult(s.Tools)}
	case MethodToolsCall:
		return s.dispatchToolsCall(ctx, f)
	default:
		return errResponse(f.ID, codeMethodNotFound, "method not found: "+f.Method)
	}
}

func (s *Server) dispatchToolsCall(ctx context.Context, f *Frame) *Response {
	var p toolCallParams
	if len(f.Params) > 0 {
		if err := json.Unmarshal(f.Params, &p); err != nil {
			return errResponse(f.ID, codeInvalidParams, "malformed tools/call params: "+err.Error())
		}
	}
	if p.Name == "" {
		return errResponse(f.ID, codeInvalidParams, `tools/call requires "name"`)
	}
	out, err := s.Tools.Call(ctx, p.Name, p.Arguments)
	if err != nil {
		return errResponse(f.ID, codeToolNotFound, err.Error())
	}
	return &Response{JSONRPC: "2.0", ID: f.ID, Result: toolCallResult{
		Content: []contentBlock{{Type: "text", Text: string(out)}},
	}}
}
