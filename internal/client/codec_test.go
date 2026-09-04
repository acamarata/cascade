package client

// Purpose: codec.go's unit tests — the whole request-framing and
//   response-contract surface of one JSON-RPC call, asserted without a
//   socket: what encodeRequest puts on the wire, what readCapped accepts
//   and refuses, and every branch of decodeResponse (id correlation,
//   taxonomy mapping, malformed and truncated bodies, result decoding).
//   Real-socket coverage of the same paths lives in
//   client_integration_test.go; this file deliberately imports neither
//   "net" nor "net/http" so it runs in the default no-network unit lane.
// SPORT: internal/client (ADD).

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestEncodeRequest_WireShape(t *testing.T) {
	body, err := encodeRequest("status.get", map[string]int{"n": 7}, "req-1")
	if err != nil {
		t.Fatalf("encodeRequest: %v", err)
	}
	var got map[string]any
	if unmarshalErr := json.Unmarshal(body, &got); unmarshalErr != nil {
		t.Fatalf("encodeRequest produced undecodable JSON %q: %v", body, unmarshalErr)
	}
	want := map[string]any{
		"jsonrpc":        "2.0",
		"method":         "status.get",
		"id":             "req-1",
		"client_version": protocolVersion,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("request[%q] = %v, want %v", k, got[k], v)
		}
	}
	params, ok := got["params"].(map[string]any)
	if !ok {
		t.Fatalf("request params = %#v, want an object", got["params"])
	}
	if params["n"] != float64(7) {
		t.Errorf("request params.n = %v, want 7", params["n"])
	}
}

// TestEncodeRequest_NilParamsOmitted proves a nil params argument is
// OMITTED from the wire object rather than sent as a JSON null: the
// server's Parse treats a present-but-null params differently from an
// absent one.
func TestEncodeRequest_NilParamsOmitted(t *testing.T) {
	body, err := encodeRequest("status.get", nil, "req-2")
	if err != nil {
		t.Fatalf("encodeRequest: %v", err)
	}
	if strings.Contains(string(body), "params") {
		t.Errorf("request body %s carries a params member, want it omitted", body)
	}
}

// TestEncodeRequest_UnmarshalableParams proves a params value
// encoding/json cannot marshal surfaces as KindInternal (a caller
// programming error), not as a silently empty request.
func TestEncodeRequest_UnmarshalableParams(t *testing.T) {
	_, err := encodeRequest("status.get", func() {}, "req-3")
	if err == nil {
		t.Fatal("encodeRequest: expected an error for an unmarshalable params value")
	}
	if !cascade.HasKind(err, cascade.KindInternal) {
		t.Errorf("err = %v, want KindInternal", err)
	}
}

func TestRequestSeq_NonEmptyAndMethodScoped(t *testing.T) {
	a, b := requestSeq("status.get"), requestSeq("other.method")
	if a == "" {
		t.Fatal("requestSeq returned an empty id; ids must be correlatable")
	}
	if a == b {
		t.Errorf("requestSeq(%q) == requestSeq(%q) = %q, want distinct ids", "status.get", "other.method", a)
	}
}

func TestReadCapped_ReadsWholeBody(t *testing.T) {
	got, err := readCapped(strings.NewReader(`{"jsonrpc":"2.0"}`), "status.get")
	if err != nil {
		t.Fatalf("readCapped: %v", err)
	}
	if string(got) != `{"jsonrpc":"2.0"}` {
		t.Errorf("readCapped = %q, want the whole body", got)
	}
}

// TestReadCapped_AtCapAccepted proves a body exactly at the cap is kept:
// the cap refuses the first byte OVER the limit, it does not truncate a
// legal maximum-size response.
func TestReadCapped_AtCapAccepted(t *testing.T) {
	got, err := readCapped(strings.NewReader(strings.Repeat("x", maxResponseBytes)), "status.get")
	if err != nil {
		t.Fatalf("readCapped at exactly the cap: unexpected error: %v", err)
	}
	if len(got) != maxResponseBytes {
		t.Errorf("readCapped returned %d bytes, want %d", len(got), maxResponseBytes)
	}
}

// TestReadCapped_OverCapRefused proves an oversized response is refused
// with KindInternal rather than being truncated into a confusing decode
// failure or read whole into memory.
func TestReadCapped_OverCapRefused(t *testing.T) {
	_, err := readCapped(strings.NewReader(strings.Repeat("x", maxResponseBytes+1)), "status.get")
	if err == nil {
		t.Fatal("readCapped: expected an error for a body over the cap")
	}
	if !cascade.HasKind(err, cascade.KindInternal) {
		t.Errorf("err = %v, want KindInternal", err)
	}
	if !strings.Contains(err.Error(), "maximum size") {
		t.Errorf("err = %v, want it to name the size cap", err)
	}
}

// failingReader returns some bytes, then fails — a peer that dies partway
// through writing its response.
type failingReader struct {
	prefix string
	sent   bool
}

var errPeerDied = errors.New("peer went away mid-response")

func (r *failingReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, r.prefix), nil
	}
	return 0, errPeerDied
}

// TestReadCapped_ReadError proves a mid-body read failure surfaces as
// KindInternal wrapping the underlying cause, not a partial body decoded
// as if it were complete.
func TestReadCapped_ReadError(t *testing.T) {
	_, err := readCapped(&failingReader{prefix: `{"jsonrpc":`}, "status.get")
	if err == nil {
		t.Fatal("readCapped: expected an error when the reader fails")
	}
	if !cascade.HasKind(err, cascade.KindInternal) {
		t.Errorf("err = %v, want KindInternal", err)
	}
	if !errors.Is(err, errPeerDied) {
		t.Errorf("err = %v, want it to wrap the underlying read failure", err)
	}
}
