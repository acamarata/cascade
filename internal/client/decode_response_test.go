package client

// Purpose: decodeResponse's own unit tests (split from codec_test.go for
//   the 300-line file cap) — the response half of the JSON-RPC contract:
//   id correlation, the frozen-taxonomy mapping of every application
//   error code, and every malformed-body shape a peer on the socket can
//   send. No "net"/"net/http" import: this is the default no-network unit
//   lane.
// SPORT: internal/client (ADD).

import (
	"strconv"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

type statusResult struct {
	Health string `json:"health"`
	PID    int    `json:"pid"`
}

func TestDecodeResponse_DecodesResult(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":"req-1","result":{"health":"ok","pid":42}}`)
	var got statusResult
	if err := decodeResponse("status.get", "req-1", body, &got); err != nil {
		t.Fatalf("decodeResponse: %v", err)
	}
	if got.Health != "ok" || got.PID != 42 {
		t.Errorf("result = %+v, want {Health:ok PID:42}", got)
	}
}

// TestDecodeResponse_NilOutDiscardsResult proves a caller that passes no
// out pointer still succeeds on a response carrying a result, rather than
// erroring or panicking on the nil.
func TestDecodeResponse_NilOutDiscardsResult(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":"req-1","result":{"health":"ok"}}`)
	if err := decodeResponse("status.get", "req-1", body, nil); err != nil {
		t.Fatalf("decodeResponse with nil out: %v", err)
	}
}

// TestDecodeResponse_AbsentResultLeavesOutUntouched proves an envelope
// with no result member is success with nothing decoded, not a spurious
// error.
func TestDecodeResponse_AbsentResultLeavesOutUntouched(t *testing.T) {
	got := statusResult{Health: "untouched"}
	body := []byte(`{"jsonrpc":"2.0","id":"req-1"}`)
	if err := decodeResponse("status.get", "req-1", body, &got); err != nil {
		t.Fatalf("decodeResponse: %v", err)
	}
	if got.Health != "untouched" {
		t.Errorf("out was overwritten to %+v, want it left alone", got)
	}
}

// TestDecodeResponse_IDMismatch proves a response echoing some OTHER
// request's id is refused: an uncorrelated response must never be decoded
// into the caller's out as if it answered this call.
func TestDecodeResponse_IDMismatch(t *testing.T) {
	got := statusResult{}
	body := []byte(`{"jsonrpc":"2.0","id":"someone-elses-request","result":{"health":"ok"}}`)
	err := decodeResponse("status.get", "req-1", body, &got)
	if err == nil {
		t.Fatal("decodeResponse: expected an error for a mismatched response id")
	}
	if !cascade.HasKind(err, cascade.KindInternal) {
		t.Errorf("err = %v, want KindInternal", err)
	}
	if !strings.Contains(err.Error(), "someone-elses-request") {
		t.Errorf("err = %v, want it to name the mismatched id", err)
	}
	if got.Health != "" {
		t.Errorf("out was populated from an uncorrelated response: %+v", got)
	}
}

// TestDecodeResponse_AbsentIDAccepted proves an envelope with no id (or a
// non-string one, which decodes to the empty string) is treated as
// "nothing to correlate against" rather than a mismatch.
func TestDecodeResponse_AbsentIDAccepted(t *testing.T) {
	var got statusResult
	body := []byte(`{"jsonrpc":"2.0","result":{"health":"ok"}}`)
	if err := decodeResponse("status.get", "req-1", body, &got); err != nil {
		t.Fatalf("decodeResponse with an absent id: %v", err)
	}
	if got.Health != "ok" {
		t.Errorf("result = %+v, want Health=ok", got)
	}
}

// TestDecodeResponse_ErrorCodesMapToFrozenTaxonomy asserts each wire code
// against the frozen taxonomy's own constants (pkg/cascade), never
// against a table this test authored itself.
func TestDecodeResponse_ErrorCodesMapToFrozenTaxonomy(t *testing.T) {
	cases := []struct {
		code int
		want cascade.Kind
	}{
		{cascade.RPCCodeInternal, cascade.KindInternal},
		{cascade.RPCCodeNotFound, cascade.KindNotFound},
		{cascade.RPCCodeInvalidInput, cascade.KindInvalidInput},
		{cascade.RPCCodeConflict, cascade.KindConflict},
		{cascade.RPCCodeUnavailable, cascade.KindUnavailable},
		{cascade.RPCCodeTimeout, cascade.KindTimeout},
		{cascade.RPCCodeCanceled, cascade.KindCanceled},
		{cascade.RPCCodePermissionDenied, cascade.KindPermissionDenied},
		{cascade.RPCCodeElevationRequired, cascade.KindElevationRequired},
		{cascade.RPCCodePolicyDenied, cascade.KindPolicyDenied},
		{cascade.RPCCodeCapabilityDenied, cascade.KindCapabilityDenied},
		{cascade.RPCCodeQuotaExhausted, cascade.KindQuotaExhausted},
		{cascade.RPCCodeUnsupported, cascade.KindUnsupported},
		{cascade.RPCCodeIntegrity, cascade.KindIntegrity},
	}
	for _, tc := range cases {
		body := []byte(`{"jsonrpc":"2.0","id":"req-1","error":{"code":` +
			strconv.Itoa(tc.code) + `,"message":"boom"}}`)
		err := decodeResponse("status.get", "req-1", body, nil)
		if err == nil {
			t.Fatalf("decodeResponse(code=%d): expected an error", tc.code)
		}
		if !cascade.HasKind(err, tc.want) {
			t.Errorf("decodeResponse(code=%d) kind = %v, want %v", tc.code, err, tc.want)
		}
		if !strings.Contains(err.Error(), "boom") {
			t.Errorf("decodeResponse(code=%d) = %v, want the server message preserved", tc.code, err)
		}
	}
}

// TestDecodeResponse_ErrorWinsOverResult proves an envelope carrying both
// members is reported as the error, never decoded as a success.
func TestDecodeResponse_ErrorWinsOverResult(t *testing.T) {
	var got statusResult
	body := []byte(`{"jsonrpc":"2.0","id":"req-1","result":{"health":"ok"},` +
		`"error":{"code":-32004,"message":"unavailable"}}`)
	err := decodeResponse("status.get", "req-1", body, &got)
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	if got.Health != "" {
		t.Errorf("out was populated from an error response: %+v", got)
	}
}

// TestDecodeResponse_MalformedBodies covers every shape a peer on the far
// end of the socket can send that is not a decodable envelope. Each must
// surface KindInternal (the transport succeeded, the payload did not) and
// none may panic.
func TestDecodeResponse_MalformedBodies(t *testing.T) {
	cases := map[string]string{
		"empty":                "",
		"truncated object":     `{"jsonrpc":"2.0","id":"req-1","resu`,
		"not json":             `this is not a JSON-RPC envelope`,
		"json scalar":          `42`,
		"json array":           `[{"jsonrpc":"2.0"}]`,
		"wrong member types":   `{"jsonrpc":"2.0","id":"req-1","error":"not an object"}`,
		"trailing garbage":     `{"jsonrpc":"2.0","id":"req-1"} trailing`,
		"nul bytes":            "\x00\x00\x00",
		"unterminated string":  `{"jsonrpc":"2.0","id":"req`,
		"deeply nested opener": strings.Repeat("[", 512),
	}
	for name, body := range cases {
		err := decodeResponse("status.get", "req-1", []byte(body), nil)
		if err == nil {
			t.Errorf("decodeResponse(%s): expected an error", name)
			continue
		}
		if !cascade.HasKind(err, cascade.KindInternal) {
			t.Errorf("decodeResponse(%s) = %v, want KindInternal", name, err)
		}
	}
}

// TestDecodeResponse_ResultTypeMismatch proves a well-formed envelope
// whose result does not fit the caller's out type is a decode failure,
// not a silent zero value.
func TestDecodeResponse_ResultTypeMismatch(t *testing.T) {
	var got statusResult
	body := []byte(`{"jsonrpc":"2.0","id":"req-1","result":"a string, not an object"}`)
	err := decodeResponse("status.get", "req-1", body, &got)
	if err == nil {
		t.Fatal("decodeResponse: expected an error for a result of the wrong shape")
	}
	if !cascade.HasKind(err, cascade.KindInternal) {
		t.Errorf("err = %v, want KindInternal", err)
	}
}
