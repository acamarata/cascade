// Package rpc implements the cascade daemon's JSON-RPC 2.0 IPC layer: the
// wire types and untrusted-input decoder (this file), the method registry
// and middleware chain (registry.go), the elevation middleware and its
// single-use nonce ledger and Ed25519 attestation verifier
// (elevation*.go), the version-skew envelope (version.go), and the
// http.Handler that mounts all of it on the daemon's unix socket
// (handler.go).
//
// Purpose: HTTP/1.1 POST /rpc over the unix socket D/S-06.T2 creates,
//
//	carrying JSON-RPC 2.0 request/response bodies (06-FORGE-SPEC §2).
//
// Inputs: raw HTTP request bodies from a peer already authenticated as the
//
//	socket owner by handler.go's UID check.
//
// Outputs: JSON-RPC 2.0 response bodies wrapped in ResponseEnvelope
//
//	(version.go).
//
// Constraints: Parse is a fuzz target (06-FORGE-SPEC §5 rule 7) — it must
//
//	never panic on malformed input; corpus lives at
//	internal/testdata/fuzz/FuzzParseRequest/. Wire error codes for taxonomy
//	application errors reuse pkg/cascade's existing JSON-RPC code table
//	verbatim (RPCCodeElevationRequired etc.) rather than a second mapping;
//	protocol-framing errors (parse error, invalid request, method not
//	found) use the JSON-RPC 2.0 spec's own reserved codes in the
//	-32768..-32600 band, which never overlaps pkg/cascade's -32000..-32013
//	application band. See codeMethodNotFound's doc comment for how -32601
//	reconciles with the taxonomy.
//
// SPORT: internal/rpc (ADD, per T-3 sport_updates).
package rpc

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// JSON-RPC 2.0 protocol-framing error codes (jsonrpc.org spec, reserved
// range -32768..-32600). These are distinct from pkg/cascade's application
// error codes (-32000..-32013): a framing error means the envelope itself
// could not be understood well enough to reach the taxonomy layer at all.
const (
	// codeParseError: the HTTP body was not valid JSON.
	codeParseError = -32700
	// codeInvalidRequest: the JSON was valid but not a well-formed
	// JSON-RPC 2.0 request object. This ticket also reuses this exact
	// code for the version-skew rejection (version.go's SkewCheck) — a
	// deliberate, documented divergence from strict spec semantics
	// (which reserves -32600 for malformed request *shape* only): both
	// conditions are pre-dispatch, envelope-level rejections, so sharing
	// the code is coherent, and the message text always names which
	// condition fired.
	codeInvalidRequest = -32600
	// codeMethodNotFound is JSON-RPC 2.0's own reserved code for an
	// unregistered method (jsonrpc.org spec). It is used LITERALLY here,
	// per this ticket's explicit contract text ("-32601 (A-T7 kind:
	// not-found)"), rather than pkg/cascade's KindNotFound.JSONRPCCode()
	// (-32001): method resolution is a protocol-framing concern (the
	// envelope names a method that does not exist on this server), not
	// an application-taxonomy error, so it stays in the spec-reserved
	// band and is never dispatched to a handler. The taxonomy Kind is
	// still surfaced for callers that want to recognize it structurally:
	// registry.go's Dispatch attaches Data: map[string]string{"kind":
	// cascade.KindNotFound.String()} to the error object, so an A-T7-aware
	// client can branch on the kind string without the wire code
	// colliding with the application band.
	codeMethodNotFound = -32601
)

// jsonrpcVersion is the only accepted "jsonrpc" field value.
const jsonrpcVersion = "2.0"

// ErrorObject is the JSON-RPC 2.0 error member: {"code", "message",
// "data"}. Shape matches pkg/cascade.RPCError deliberately (json tag names
// are identical) so a Response's Error field serializes to the same wire
// shape whether it originated from a taxonomy error (via NewErrorFromTaxonomy)
// or a protocol-framing error (via newFramingError).
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

func newFramingError(code int, message string) *ErrorObject {
	return &ErrorObject{Code: code, Message: message}
}

// Request is a decoded JSON-RPC 2.0 request object plus the one
// cascade-specific extension field, client_version (consumed by
// version.go's SkewCheck before dispatch). ID is the raw id bytes,
// preserved verbatim for round-trip in the response; a missing "id" key
// (a notification, per spec) is distinguished from a present-but-null id
// by the notification field, not by ID's nilness alone — see
// IsNotification.
type Request struct {
	JSONRPC       string          `json:"jsonrpc"`
	Method        string          `json:"method"`
	Params        json.RawMessage `json:"params,omitempty"`
	ID            json.RawMessage `json:"id,omitempty"`
	ClientVersion string          `json:"client_version,omitempty"`

	notification bool
}

// IsNotification reports whether the request omitted "id" entirely (a
// JSON-RPC 2.0 notification: no response is expected). A request carrying
// an explicit JSON null id is NOT a notification under this decoder — the
// key was present — matching the spec's guidance that omission, not a null
// value, is what makes a request a notification.
func (r *Request) IsNotification() bool {
	return r.notification
}

// ParamsHash returns the hex-encoded SHA-256 of the request's raw params
// bytes (empty params hashes the empty byte string). Used by elevation.go
// to bind an attestation to the exact params the pending elevated request
// carried.
func (r *Request) ParamsHash() string {
	return hashParams(r.Params)
}

func hashParams(params json.RawMessage) string {
	sum := sha256.Sum256(params)
	return hex.EncodeToString(sum[:])
}

// Parse decodes a JSON-RPC 2.0 request from an HTTP POST /rpc body. It is
// the fuzz target (FuzzParseRequest, fuzz_test.go) and MUST NEVER PANIC on
// any input, however malformed — a malformed body is always a returned
// *ErrorObject, never a crash. Parse rejects a top-level JSON array
// (batch requests) explicitly rather than silently misparsing one; see
// this package's doc comment / version.go's SPEC-COVERAGE note on batch
// support.
func Parse(body []byte) (*Request, *ErrorObject) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, newFramingError(codeParseError, "empty request body")
	}
	if trimmed[0] == '[' {
		return nil, newFramingError(codeInvalidRequest,
			"batch requests are not supported by this server")
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &raw); err != nil {
		return nil, newFramingError(codeParseError, "malformed JSON: "+err.Error())
	}

	req := &Request{}
	if v, ok := raw["jsonrpc"]; ok {
		_ = json.Unmarshal(v, &req.JSONRPC)
	}
	if req.JSONRPC != jsonrpcVersion {
		return nil, newFramingError(codeInvalidRequest,
			`invalid request: "jsonrpc" must be "2.0"`)
	}

	if v, ok := raw["method"]; ok {
		_ = json.Unmarshal(v, &req.Method)
	}
	if req.Method == "" {
		return nil, newFramingError(codeInvalidRequest,
			`invalid request: "method" is required and must be non-empty`)
	}

	if v, ok := raw["params"]; ok {
		req.Params = v
	}
	if v, ok := raw["client_version"]; ok {
		_ = json.Unmarshal(v, &req.ClientVersion)
	}
	if v, ok := raw["id"]; ok {
		req.ID = v
	} else {
		req.notification = true
	}

	return req, nil
}
