package nodes

import (
	"context"
	"encoding/json"
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

// repeatReader deterministically repeats a seed byte string, matching
// testIdentity's strings.Repeat(seed, 64) shape without re-importing
// strings here.
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
	f := build(1)
	res, err := ProcessHeartbeat(context.Background(), deps, f)
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

// TestProcessHeartbeatUnenrolledNodeRefused proves the authenticated-
// channel requirement: a heartbeat for a node id with no device record at
// all is refused, never silently accepted as "unknown means allow".
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
	f := build(1)
	_, err := ProcessHeartbeat(context.Background(), deps, f)
	if err == nil {
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

func oversizedCapString() string {
	b := make([]byte, maxCapabilityLen+1)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}

func TestDecodeHeartbeatFrameOversizedPayload(t *testing.T) {
	huge := make([]byte, maxHeartbeatPayloadBytes+1)
	for i := range huge {
		huge[i] = ' '
	}
	if _, err := DecodeHeartbeatFrame(huge); err == nil {
		t.Fatal("expected refusal for oversized payload")
	}
}

// TestRegisterHeartbeatHandler_EndToEndThroughDispatch drives the REAL
// production entry point: rpc.Registry.Dispatch through the handler
// RegisterHeartbeatHandler mounts, mirroring
// TestRegisterHandlers_EndToEndThroughDispatch's proof shape for
// node.enroll.
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

// TestRegisterHeartbeatHandler_WithoutWiring_MethodNotFound proves the
// wiring above is load-bearing: an otherwise-identical registry that
// never calls RegisterHeartbeatHandler refuses node.heartbeat with
// method-not-found, never reaching ProcessHeartbeat at all.
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

// fakeTicker is a manually-fired Ticker for deterministic loop tests
// (Art.7.3 — no real sleeps).
type fakeTicker struct {
	ch      chan struct{}
	stopped bool
}

func newFakeTicker() *fakeTicker         { return &fakeTicker{ch: make(chan struct{}, 1)} }
func (f *fakeTicker) C() <-chan struct{} { return f.ch }
func (f *fakeTicker) Stop()              { f.stopped = true }
func (f *fakeTicker) fire()              { f.ch <- struct{}{} }

// storedTestKeystore builds a NodeKeystore for testID with a freshly
// generated key already stored under it, shared by the RunHeartbeatLoop
// tests to keep each test under the function-length cap.
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

func TestRunHeartbeatLoopSendsOnTick(t *testing.T) {
	id := testIdentity(t, "loop")
	ks := storedTestKeystore(t, "loop", id.NodeID)

	ticker := newFakeTicker()
	sent := make(chan HeartbeatFrame, 4)
	var seq uint64
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunHeartbeatLoop(ctx, HeartbeatLoopOptions{
			Ticker: ticker,
			NextSequence: func() uint64 {
				seq++
				return seq
			},
			Send: func(_ context.Context, f HeartbeatFrame) error {
				sent <- f
				return nil
			},
			BuildReport:  validReport,
			Keystore:     ks,
			NodeID:       id.NodeID,
			EnrollmentID: "enr-1",
		})
		close(done)
	}()

	ticker.fire()
	select {
	case f := <-sent:
		if f.Sequence != 1 {
			t.Fatalf("got sequence %d, want 1", f.Sequence)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for heartbeat send")
	}
	cancel()
	<-done
	if !ticker.stopped {
		t.Fatal("expected Ticker.Stop() to be called on loop exit")
	}
}

func TestRunHeartbeatLoopReportsSendErrorAndContinues(t *testing.T) {
	id := testIdentity(t, "loop2")
	ks := storedTestKeystore(t, "loop2", id.NodeID)

	ticker := newFakeTicker()
	errs := make(chan error, 4)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunHeartbeatLoop(ctx, HeartbeatLoopOptions{
			Ticker:       ticker,
			NextSequence: func() uint64 { return 1 },
			Send: func(context.Context, HeartbeatFrame) error {
				return cascade.New(cascade.KindUnavailable, "controller unreachable")
			},
			BuildReport:  validReport,
			Keystore:     ks,
			NodeID:       id.NodeID,
			EnrollmentID: "enr-1",
			OnError:      func(err error) { errs <- err },
		})
		close(done)
	}()
	ticker.fire()
	select {
	case err := <-errs:
		if err == nil {
			t.Fatal("expected a non-nil send error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for OnError")
	}
	cancel()
	<-done
}
