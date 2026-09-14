package remote

// Purpose: the JSON-RPC 2.0 handshake envelope R-14.49 names ("an exact
//   mirror of the §2 unix-socket IPC shape, with NO custom framing") and
//   decodeHandshakeResponse, the network-facing decoder
//   FuzzRemoteHandshakeResponse (fuzz_test.go) exercises directly (§5.7:
//   every decoder reading bytes from an untrusted remote host carries a
//   fuzz target). Split out of remote.go to isolate the fuzzed surface
//   in its own small file and keep remote.go under the 300-line cap.
// Inputs: a RemoteRuntimeConfig (encodeHandshakeRequest) or raw response
//   bytes from the wire (decodeHandshakeResponse).
// Outputs: the request body to send, or a *handshakeResult / typed
//   *cascade.Error.
// Constraints: decodeHandshakeResponse MUST NOT panic on any input,
//   including malformed, truncated, or adversarial JSON — that is the
//   fuzz target's entire premise. encoding/json.Unmarshal already never
//   panics on malformed input; this function adds no unsafe operation on
//   top of it (no unchecked type assertion, no indexing without a length
//   check).
// SPORT: internal/plugins/remote (ADD) — P1-E15-W4-S33-T4.

import (
	"encoding/json"

	"github.com/acamarata/cascade/pkg/cascade"
)

// jsonrpcVersion is the only accepted "jsonrpc" field value, matching
// internal/rpc/jsonrpc.go's own constant (this package cannot import
// that one directly — BOUNDARY NOTE, remote.go's package doc — so it
// restates the literal rather than sharing the symbol).
const jsonrpcVersion = "2.0"

// handshakeMethod is the JSON-RPC method name the handshake negotiates
// over.
const handshakeMethod = "handshake"

// maxHandshakeResponseBytes caps the response body a remote host may
// send, so a hostile or broken remote cannot stream unbounded bytes into
// this process's memory during a handshake it never even agreed to run.
const maxHandshakeResponseBytes = 64 << 10 // 64 KiB

// handshakeRequest is the outbound JSON-RPC 2.0 request object.
type handshakeRequest struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      int              `json:"id"`
	Method  string           `json:"method"`
	Params  handshakeReqBody `json:"params"`
}

// handshakeReqBody is the handshake method's params: this host's ABI
// version, the one fact negotiation needs from the caller's side.
type handshakeReqBody struct {
	ABIVersion int `json:"abi_version"`
}

// handshakeResponse is the inbound JSON-RPC 2.0 response envelope: either
// Result or Error is populated, never both (spec-standard; this decoder
// does not enforce mutual exclusion, it just prefers Error when present,
// matching every other JSON-RPC client in this tree).
type handshakeResponse struct {
	JSONRPC string             `json:"jsonrpc"`
	ID      int                `json:"id"`
	Result  *handshakeResult   `json:"result"`
	Error   *handshakeErrorObj `json:"error"`
}

// handshakeResult is the remote's successful handshake answer: its own
// ABI version, for dialRemote to compare against the config it dialed
// with.
type handshakeResult struct {
	ABIVersion int `json:"abi_version"`
}

// handshakeErrorObj mirrors the JSON-RPC 2.0 error member shape
// (internal/rpc/jsonrpc.go's ErrorObject; restated here for the same
// BOUNDARY NOTE reason handshakeMethod's constant is restated).
type handshakeErrorObj struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// encodeHandshakeRequest builds the outbound JSON-RPC 2.0 request body.
func encodeHandshakeRequest(cfg RemoteRuntimeConfig) ([]byte, error) {
	return json.Marshal(handshakeRequest{
		JSONRPC: jsonrpcVersion,
		ID:      1,
		Method:  handshakeMethod,
		Params:  handshakeReqBody{ABIVersion: cfg.ABIVersion},
	})
}

// decodeHandshakeResponse parses data as a handshakeResponse. It is the
// network-facing decoder FuzzRemoteHandshakeResponse fuzzes directly:
// every return path is an error, never a panic, for any []byte input.
func decodeHandshakeResponse(data []byte) (*handshakeResult, error) {
	var resp handshakeResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "plugins/remote: decode handshake response")
	}
	if resp.Error != nil {
		return nil, cascade.Newf(cascade.KindUnavailable,
			"plugins/remote: remote refused handshake: %s (code %d)", resp.Error.Message, resp.Error.Code)
	}
	if resp.JSONRPC != jsonrpcVersion {
		return nil, cascade.Newf(cascade.KindInvalidInput,
			"plugins/remote: handshake response jsonrpc field = %q, want %q", resp.JSONRPC, jsonrpcVersion)
	}
	if resp.Result == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "plugins/remote: handshake response has neither result nor error")
	}
	return resp.Result, nil
}
