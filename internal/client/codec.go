package client

// Purpose: the socket-free half of one JSON-RPC 2.0 call — request framing
//   (encodeRequest), the bounded response read (readCapped), and the whole
//   response contract (decodeResponse: id correlation, taxonomy mapping,
//   result unmarshaling). Client.Do (client.go) is the only production
//   caller of each; splitting them out is what lets every framing and
//   malformed-response rule be asserted in the default no-network unit
//   lane, leaving only the irreducible transport step behind a real
//   socket.
// Constraints: decodeResponse's input is untrusted bytes from the far end
//   of the socket — it must never panic and never allocate unboundedly
//   (readCapped enforces the size bound before it is ever reached).

import (
	"encoding/json"
	"io"

	"github.com/acamarata/cascade/pkg/cascade"
)

// maxResponseBytes caps how much of a daemon response body readCapped will
// keep, mirroring internal/rpc/handler.go's maxBodyBytes cap on the
// server's own inbound side: a malicious or buggy peer on the socket must
// not be able to exhaust client memory with an unbounded response.
const maxResponseBytes = 4 << 20

// wireRequest is the JSON-RPC 2.0 request object this SDK sends. Field
// names/shape mirror internal/rpc.Request's wire contract exactly
// (jsonrpc.go), duplicated for the same reason as client.go's rpcPath.
type wireRequest struct {
	JSONRPC       string `json:"jsonrpc"`
	Method        string `json:"method"`
	Params        any    `json:"params,omitempty"`
	ID            string `json:"id"`
	ClientVersion string `json:"client_version,omitempty"`
}

// requestSeq gives each Do call a distinct, non-empty id so decodeResponse
// can detect a response whose id does not match the request that
// solicited it. Not a rand/atomic counter: a fixed per-process string plus
// the method name is sufficient correlation for a single in-flight
// request/response pair over one HTTP round trip, and avoids a forbidden
// bare time.Now()/unseeded rand call in domain logic.
func requestSeq(method string) string {
	return "cascade-client:" + method
}

// encodeRequest marshals one JSON-RPC 2.0 request for method with params
// under the given id. A params value encoding/json cannot marshal (a
// channel, a func, a cyclic structure) is a caller programming error, not
// a daemon condition, so it surfaces as KindInternal rather than
// KindInvalidInput.
func encodeRequest(method string, params any, id string) ([]byte, error) {
	body, err := json.Marshal(wireRequest{
		JSONRPC:       "2.0",
		Method:        method,
		Params:        params,
		ID:            id,
		ClientVersion: protocolVersion,
	})
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "client: marshal request "+method)
	}
	return body, nil
}

// readCapped reads at most maxResponseBytes from r, reporting KindInternal
// if the peer sends more than that or if the read itself fails partway.
// It reads one byte past the cap deliberately, so an exactly-at-the-cap
// body is accepted and the first byte over it is detected rather than
// silently truncated into a decode failure.
func readCapped(r io.Reader, method string) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxResponseBytes+1))
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "client: read response for "+method)
	}
	if len(body) > maxResponseBytes {
		return nil, cascade.Newf(cascade.KindInternal, "client: response for %s exceeds maximum size", method)
	}
	return body, nil
}

// decodeResponse turns one raw response body into either a decoded result
// in out or a taxonomy error. The rules, in order:
//
//   - a body that is not a decodable JSON-RPC envelope (empty, truncated,
//     garbage, a JSON scalar) is KindInternal: the transport succeeded,
//     only the payload is unusable;
//   - a non-empty id that does not echo wantID is a correlation failure,
//     also KindInternal — a client that ignores id correlation is not a
//     faithful JSON-RPC 2.0 implementation. An absent/non-string id
//     decodes to "" and is treated as "nothing to correlate against";
//   - an error member wins over any result and maps through
//     kindForRPCError (decode.go) to the frozen taxonomy;
//   - out == nil or an absent/empty result member is success with nothing
//     to decode, so a notification-shaped call is not an error.
func decodeResponse(method, wantID string, body []byte, out any) error {
	env, err := decodeEnvelope(body)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "client: decode response for "+method)
	}
	if env.ID != "" && env.ID != wantID {
		return cascade.Newf(cascade.KindInternal,
			"client: response id %q does not match request id %q for %s", env.ID, wantID, method)
	}
	if env.Error != nil {
		return cascade.New(kindForRPCError(env.Error), method+": "+env.Error.Message)
	}
	if out == nil || len(env.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "client: decode result for "+method)
	}
	return nil
}
