package client

// Purpose: decodeEnvelope is FuzzRPCResponseDecode's target
//   (fuzz_test.go) — the untrusted-input decoder for bytes the daemon (or
//   anything on the far end of the unix socket) sends back. It MUST NEVER
//   PANIC, however truncated, oversized, or adversarial the input
//   (06-FORGE-SPEC §5 rule 7). kindForRPCError maps a decoded JSON-RPC
//   error object to the frozen pkg/cascade taxonomy (hard requirement 4).
// Constraints: seed corpus at
//   internal/testdata/fuzz/FuzzRPCResponseDecode/, with a provenance
//   README (mirrors internal/rpc/fuzz_test.go's own convention for
//   Parse).

import (
	"encoding/json"
	"errors"

	"github.com/acamarata/cascade/pkg/cascade"
)

// rpcErrorObject is the JSON-RPC 2.0 error member's wire shape, matching
// internal/rpc.ErrorObject field-for-field (json tag names identical, per
// that type's own doc comment on why every taxonomy/framing error shares
// one shape).
type rpcErrorObject struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// wireEnvelope is the decoded shape of a daemon response body: the
// JSON-RPC 2.0 response members plus internal/rpc's ResponseEnvelope
// version fields. ID is captured as a string for id-correlation
// (requestSeq/Do always sends a string id) — a daemon that echoes some
// other JSON id type decodes to the empty string here, which Do treats as
// "no id to correlate against" rather than panicking on a type mismatch.
type wireEnvelope struct {
	JSONRPC         string          `json:"jsonrpc"`
	ID              string          `json:"id"`
	Result          json.RawMessage `json:"result,omitempty"`
	Error           *rpcErrorObject `json:"error,omitempty"`
	ProtocolVersion string          `json:"protocol_version"`
	ServerVersion   string          `json:"server_version"`
}

// errEmptyBody is decodeEnvelope's sentinel for a zero-length response
// body — the shortest possible truncation a daemon (or a malicious peer on
// the socket) could send.
var errEmptyBody = errors.New("client: empty response body")

// decodeEnvelope decodes body as a JSON-RPC 2.0 response envelope. It is
// the fuzz target FuzzRPCResponseDecode exercises directly and must never
// panic on any input: truncated, oversized, garbage, or well-formed JSON
// that is not an object all decode to a non-nil error here, never a
// crash. encoding/json.Unmarshal already never panics on malformed input
// (it returns a *SyntaxError/*UnmarshalTypeError instead), matching
// internal/rpc.Parse's own approach; this function's only addition is the
// explicit empty-body case, which json.Unmarshal alone reports as a less
// specific "unexpected end of JSON input".
func decodeEnvelope(body []byte) (*wireEnvelope, error) {
	if len(body) == 0 {
		return nil, errEmptyBody
	}
	var env wireEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, err
	}
	return &env, nil
}

// kindForRPCError maps a decoded JSON-RPC error object's wire code to a
// taxonomy Kind, per hard requirement 4: application error codes
// (RPCCodeInternal..RPCCodeIntegrity, -32000..-32013) reverse through
// pkg/cascade's own frozen code table (the "existing table" the
// requirement names — codes.go's rpcCodeByKind/KindFromJSONRPCCode).
// -32601 (method not found) is JSON-RPC's own spec-reserved code, never
// present in that application-band table (see internal/rpc/jsonrpc.go's
// codeMethodNotFound doc comment for why the server keeps it there
// literally); this SDK still recognizes it structurally and reports
// KindNotFound, matching the "kind" the server independently attaches to
// that error's Data member (registry.go's methodNotFoundError). Any other
// framing-band code (parse error, invalid request, invalid params) falls
// back to KindInternal — the same fallback pkg/cascade.NewRPCError uses
// for any error that does not carry a taxonomy Kind.
func kindForRPCError(errObj *rpcErrorObject) cascade.Kind {
	if k, ok := cascade.KindFromJSONRPCCode(errObj.Code); ok {
		return k
	}
	if errObj.Code == rpcCodeMethodNotFound {
		return cascade.KindNotFound
	}
	return cascade.KindInternal
}

// rpcCodeMethodNotFound mirrors internal/rpc.codeMethodNotFound (-32601,
// the JSON-RPC 2.0 spec's own reserved code). Duplicated as a literal for
// the same reason as client.go's rpcPath/protocolVersion: this package
// never imports internal/rpc.
const rpcCodeMethodNotFound = -32601
