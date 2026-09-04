// Purpose: unit tests for this SDK's pure logic — envelope decoding and
//
//	RPC error-to-Kind mapping — that need no real socket or HTTP client.
//	The SSE accumulator's tests live in stream_test.go, the
//	cmd-rpc-server-boundary depguard proof lives in boundary_test.go (both
//	split out to keep each file under the 300-line cap), and the real-dial
//	cases live in client_integration_test.go and
//	stream_integration_test.go, build-tagged "integration": this file
//	deliberately imports neither "net" nor "net/http" so it runs in the
//	fast, no-network unit lane (internal/build's no-network-unit-lane
//	gate, Art.7.2).
//
// SPORT: internal/client (ADD, per T-3 sport_updates).
package client

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestDecodeEnvelope_Success(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":"1","result":{"a":1}}`)
	env, err := decodeEnvelope(body)
	if err != nil {
		t.Fatalf("decodeEnvelope: unexpected error: %v", err)
	}
	if env.ID != "1" {
		t.Errorf("ID = %q, want %q", env.ID, "1")
	}
	if string(env.Result) != `{"a":1}` {
		t.Errorf("Result = %s, want %s", env.Result, `{"a":1}`)
	}
}

func TestDecodeEnvelope_EmptyBody(t *testing.T) {
	if _, err := decodeEnvelope(nil); err == nil {
		t.Fatal("decodeEnvelope(nil): expected an error")
	}
	if _, err := decodeEnvelope([]byte{}); err == nil {
		t.Fatal("decodeEnvelope([]byte{}): expected an error")
	}
}

func TestDecodeEnvelope_MalformedJSON(t *testing.T) {
	if _, err := decodeEnvelope([]byte("not json")); err == nil {
		t.Fatal("decodeEnvelope: expected an error for malformed JSON")
	}
}

func TestDecodeEnvelope_ErrorObject(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":"1","error":{"code":-32601,"message":"method not found: bogus"}}`)
	env, err := decodeEnvelope(body)
	if err != nil {
		t.Fatalf("decodeEnvelope: unexpected error: %v", err)
	}
	if env.Error == nil || env.Error.Code != -32601 {
		t.Fatalf("Error = %+v, want code -32601", env.Error)
	}
}

func TestKindForRPCError_ApplicationCodes(t *testing.T) {
	cases := []struct {
		code int
		want cascade.Kind
	}{
		{-32000, cascade.KindInternal},
		{-32001, cascade.KindNotFound},
		{-32002, cascade.KindInvalidInput},
		{-32005, cascade.KindTimeout},
		{-32013, cascade.KindIntegrity},
	}
	for _, tc := range cases {
		got := kindForRPCError(&rpcErrorObject{Code: tc.code})
		if got != tc.want {
			t.Errorf("kindForRPCError(code=%d) = %v, want %v", tc.code, got, tc.want)
		}
	}
}

func TestKindForRPCError_MethodNotFound(t *testing.T) {
	got := kindForRPCError(&rpcErrorObject{Code: -32601, Message: "method not found: x"})
	if got != cascade.KindNotFound {
		t.Errorf("kindForRPCError(-32601) = %v, want KindNotFound", got)
	}
}

func TestKindForRPCError_UnrecognizedFallsBackToInternal(t *testing.T) {
	got := kindForRPCError(&rpcErrorObject{Code: -32700})
	if got != cascade.KindInternal {
		t.Errorf("kindForRPCError(-32700) = %v, want KindInternal", got)
	}
	got = kindForRPCError(&rpcErrorObject{Code: 999999})
	if got != cascade.KindInternal {
		t.Errorf("kindForRPCError(999999) = %v, want KindInternal", got)
	}
}
