package nodes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

func newHeartbeatHarness(t *testing.T) (HeartbeatDeps, DeviceRecord, Identity, func(seq uint64) HeartbeatFrame) {
	t.Helper()
	clock := testkit.NewFrozenClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	store := NewRecordStore(newMemRecordBackend(), clock)
	id := testIdentity(t, "n")
	rec, err := store.Enroll(id, TierWorkerTrusted)
	if err != nil {
		t.Fatal(err)
	}
	_, priv, err := GenerateIdentity(fixedReader(t, "n"))
	if err != nil {
		t.Fatal(err)
	}
	deps := HeartbeatDeps{Records: store, Sequences: NewSequenceStore(), Clock: clock}
	build := func(seq uint64) HeartbeatFrame {
		return signedFrame(t, priv, id.NodeID, DeriveEnrollmentID(rec), seq)
	}
	return deps, rec, id, build
}

func fixedReader(t *testing.T, seed string) *repeatReader {
	t.Helper()
	return &repeatReader{b: []byte(seed)}
}

// repeatReader deterministically repeats a seed byte string.
type repeatReader struct {
	b   []byte
	pos int
}

func (r *repeatReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.b[r.pos%len(r.b)]
		r.pos++
	}
	return len(p), nil
}

func TestProcessHeartbeatAccepted(t *testing.T) {
	deps, rec, _, build := newHeartbeatHarness(t)
	res, err := ProcessHeartbeat(context.Background(), deps, build(1))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Liveness != LivenessReachable {
		t.Fatalf("got liveness %q, want reachable", res.Liveness)
	}
	got, err := deps.Records.Get(rec.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastSeen.IsZero() {
		t.Fatal("LastSeen was not updated")
	}
}

// TestProcessHeartbeatUnenrolledNodeRefused: unknown node id is refused.
func TestProcessHeartbeatUnenrolledNodeRefused(t *testing.T) {
	deps, _, _, build := newHeartbeatHarness(t)
	f := build(1)
	f.NodeID = "never-enrolled"
	_, err := ProcessHeartbeat(context.Background(), deps, f)
	if err == nil {
		t.Fatal("expected refusal for unenrolled node id")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindNotFound {
		t.Fatalf("got kind %v (ok=%v), want KindNotFound", kind, ok)
	}
}

func TestProcessHeartbeatRevokedKeyRefused(t *testing.T) {
	deps, rec, id, build := newHeartbeatHarness(t)
	rec.RevokedKeys = append(rec.RevokedKeys, fingerprintOfKey(id.PubKey))
	if err := deps.Records.put(rec); err != nil {
		t.Fatal(err)
	}
	if _, err := ProcessHeartbeat(context.Background(), deps, build(1)); err == nil {
		t.Fatal("expected refusal for revoked key")
	}
}

func TestDecodeHeartbeatFrameRejectsMalformed(t *testing.T) {
	cases := map[string][]byte{
		"empty":            nil,
		"not json":         []byte("{{{"),
		"trailing data":    []byte(`{"node_id":"a","enrollment_id":"b","signature_b64":"c","report":{"capabilities":[],"k12":{"class":"minimal"}}}{}`),
		"unknown field":    []byte(`{"node_id":"a","enrollment_id":"b","signature_b64":"c","report":{"capabilities":[],"k12":{"class":"minimal"}},"bogus":1}`),
		"missing node_id":  []byte(`{"enrollment_id":"b","signature_b64":"c","report":{"capabilities":[],"k12":{"class":"minimal"}}}`),
		"missing sig":      []byte(`{"node_id":"a","enrollment_id":"b","report":{"capabilities":[],"k12":{"class":"minimal"}}}`),
		"bad k12 class":    []byte(`{"node_id":"a","enrollment_id":"b","signature_b64":"c","report":{"capabilities":[],"k12":{"class":"huge"}}}`),
		"oversized report": []byte(`{"node_id":"a","enrollment_id":"b","signature_b64":"c","report":{"capabilities":["` + oversizedCapString() + `"],"k12":{"class":"minimal"}}}`),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeHeartbeatFrame(raw); err == nil {
				t.Fatalf("%s: expected decode error", name)
			}
		})
	}
}

func oversizedCapString() string { return string(bytes.Repeat([]byte("a"), maxCapabilityLen+1)) }

func TestDecodeHeartbeatFrameOversizedPayload(t *testing.T) {
	huge := bytes.Repeat([]byte(" "), maxHeartbeatPayloadBytes+1)
	if _, err := DecodeHeartbeatFrame(huge); err == nil {
		t.Fatal("expected refusal for oversized payload")
	}
}

// TestRegisterHeartbeatHandler_EndToEndThroughDispatch drives the real handler.
func TestRegisterHeartbeatHandler_EndToEndThroughDispatch(t *testing.T) {
	deps, _, _, build := newHeartbeatHarness(t)
	f := build(1)
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	reg := rpc.NewRegistry()
	RegisterHeartbeatHandler(reg, deps)

	result, errObj := reg.Dispatch(context.Background(), &rpc.Request{Method: "node.heartbeat", Params: raw})
	if errObj != nil {
		t.Fatalf("dispatch failed: %+v", errObj)
	}
	res, ok := result.(HeartbeatResult)
	if !ok {
		t.Fatalf("result is %T, want HeartbeatResult", result)
	}
	if res.Liveness != LivenessReachable {
		t.Fatalf("got liveness %q, want reachable", res.Liveness)
	}
}

// TestRegisterHeartbeatHandler_WithoutWiring_MethodNotFound: unwired refuses.
func TestRegisterHeartbeatHandler_WithoutWiring_MethodNotFound(t *testing.T) {
	_, _, _, build := newHeartbeatHarness(t)
	f := build(1)
	raw, _ := json.Marshal(f)

	reg := rpc.NewRegistry() // RegisterHeartbeatHandler deliberately NOT called
	_, errObj := reg.Dispatch(context.Background(), &rpc.Request{Method: "node.heartbeat", Params: raw})
	if errObj == nil {
		t.Fatal("expected method-not-found with no handler registered")
	}
	if errObj.Code != -32601 {
		t.Fatalf("errObj.Code = %d, want -32601 (method not found)", errObj.Code)
	}
}

// storedTestKeystore builds a NodeKeystore for testID with a fresh key.
func storedTestKeystore(t *testing.T, seed, nodeID string) *NodeKeystore {
	t.Helper()
	ks, err := NewNodeKeystoreForTest(t)
	if err != nil {
		t.Fatal(err)
	}
	_, priv, err := GenerateIdentity(fixedReader(t, seed))
	if err != nil {
		t.Fatal(err)
	}
	if err := ks.Store(context.Background(), nodeID, priv); err != nil {
		t.Fatal(err)
	}
	return ks
}

// bufConn is a Conn (io.ReadWriteCloser) backed by an in-memory write
// buffer and a canned read response — no goroutine, no "net" import
// needed to exercise the sender's real req.Write/http.ReadResponse path.
type bufConn struct {
	written bytes.Buffer
	resp    *bytes.Reader
}

func (c *bufConn) Read(p []byte) (int, error)  { return c.resp.Read(p) }
func (c *bufConn) Write(p []byte) (int, error) { return c.written.Write(p) }
func (c *bufConn) Close() error                { return nil }

// TestNewTunnelHeartbeatSender_RoundTrip proves the sink writes a real
// HTTP/JSON-RPC request and decodes a real canned response.
func TestNewTunnelHeartbeatSender_RoundTrip(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"result":{"node_id":"n"}}`
	resp := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
	conn := &bufConn{resp: bytes.NewReader([]byte(resp))}
	sender := NewTunnelHeartbeatSender(func(context.Context) (Conn, error) { return conn, nil })
	if err := sender(context.Background(), HeartbeatFrame{NodeID: "n", EnrollmentID: "e", Sequence: 1}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if conn.written.Len() == 0 {
		t.Fatal("expected a request to be written to the tunnel conn")
	}
}

// TestNewTunnelHeartbeatSender_SetsJSONContentType proves the sink sets
// Content-Type: application/json on the wire (P1-E04-W6-S146-T1) —
// internal/rpc's local request guard refuses a POST whose Content-Type
// does not parse to application/json as browser-shaped, and this sender
// was the one first-party caller that sent none. A raw byte check on the
// written request (no net/http import needed) proves the header line
// actually reached the wire, not just some in-memory request value.
func TestNewTunnelHeartbeatSender_SetsJSONContentType(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"result":{"node_id":"n"}}`
	resp := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
	conn := &bufConn{resp: bytes.NewReader([]byte(resp))}
	sender := NewTunnelHeartbeatSender(func(context.Context) (Conn, error) { return conn, nil })
	if err := sender(context.Background(), HeartbeatFrame{NodeID: "n", EnrollmentID: "e", Sequence: 1}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if !bytes.Contains(conn.written.Bytes(), []byte("Content-Type: application/json\r\n")) {
		t.Fatalf("written request must carry the Content-Type header line, got:\n%s", conn.written.String())
	}
}
