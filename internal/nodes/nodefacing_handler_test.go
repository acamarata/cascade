package nodes

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// nodeFacingHarness is one NodeFacing over an enrolled node, with a probe
// that records the caller every dispatched handler sees.
type nodeFacingHarness struct {
	f        *NodeFacing
	records  *RecordStore
	clock    *testkit.FrozenClock
	nodeID   string
	build    func(seq uint64) HeartbeatFrame
	rv       *Rendezvous
	attempts *AttemptRegister
	sink     *recordingSink

	mu   sync.Mutex
	seen map[string]principal.Caller
}

func newNodeFacingHarness(t *testing.T) *nodeFacingHarness {
	t.Helper()
	deps, rec, _, build := newHeartbeatHarness(t)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	h := &nodeFacingHarness{records: deps.Records, clock: deps.Clock.(*testkit.FrozenClock), nodeID: rec.NodeID,
		build: build, rv: NewRendezvous(), attempts: NewAttemptRegister(), sink: &recordingSink{},
		seen: map[string]principal.Caller{}}
	h.f = NewNodeFacing(NodeFacingDeps{Records: deps.Records, Sequences: deps.Sequences, Rendezvous: h.rv,
		Journal: JournalStreamDeps{Attempts: h.attempts, Sink: h.sink}, Clock: deps.Clock, Signer: payloadSigner{key}})
	h.f.Registry().Use(func(method string, next rpc.HandlerFunc) rpc.HandlerFunc {
		return func(ctx context.Context, params json.RawMessage) (any, error) {
			c, _ := principal.CallerFrom(ctx)
			h.mu.Lock()
			h.seen[method] = c
			h.mu.Unlock()
			return next(ctx, params)
		}
	})
	return h
}

func (h *nodeFacingHarness) sawCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.seen)
}

// nodeFacingReply is one decoded HTTP response from the node-facing server.
type nodeFacingReply struct {
	status int
	Result json.RawMessage  `json:"result"`
	Error  *rpc.ErrorObject `json:"error"`
}

// nodeFacingCall sends one JSON-RPC request over a DialAs pipe as nodeID,
// written and read as raw HTTP/1.1 bytes. headers are name, value pairs.
func nodeFacingCall(ctx context.Context, t *testing.T, f *NodeFacing, nodeID, method string, params any, headers ...string) nodeFacingReply {
	t.Helper()
	raw, err := json.Marshal(params)
	body, err2 := json.Marshal(rpc.Request{JSONRPC: "2.0", Method: method, Params: raw, ID: json.RawMessage("1")})
	if err != nil || err2 != nil {
		t.Fatal(err, err2)
	}
	conn, err := f.DialAs(nodeID)(ctx)
	if err != nil {
		t.Fatalf("DialAs: %v", err)
	}
	defer func() { _ = conn.Close() }()
	var req bytes.Buffer
	fmt.Fprintf(&req, "POST %s HTTP/1.1\r\nHost: node-facing\r\nContent-Type: application/json\r\nContent-Length: %d\r\n", rpc.RPCPath, len(body))
	for i := 0; i+1 < len(headers); i += 2 {
		fmt.Fprintf(&req, "%s: %s\r\n", headers[i], headers[i+1])
	}
	req.WriteString("\r\n")
	req.Write(body)
	if _, err := conn.Write(req.Bytes()); err != nil {
		t.Fatalf("write request: %v", err)
	}
	return readNodeFacingReply(t, bufio.NewReader(conn))
}

func readNodeFacingReply(t *testing.T, r *bufio.Reader) nodeFacingReply {
	t.Helper()
	line, err := r.ReadString('\n')
	fields := strings.Fields(line)
	if err != nil || len(fields) < 2 {
		t.Fatalf("status line %q: %v", line, err)
	}
	status, _ := strconv.Atoi(fields[1])
	length := -1
	for header := ""; err == nil; {
		if header, err = r.ReadString('\n'); strings.TrimSpace(header) == "" {
			break
		}
		if name, value, ok := strings.Cut(strings.TrimSpace(header), ":"); ok && strings.EqualFold(name, "Content-Length") {
			length, _ = strconv.Atoi(strings.TrimSpace(value))
		}
	}
	body := make([]byte, max(length, 0))
	if _, rerr := io.ReadFull(r, body); err != nil || rerr != nil || length < 0 {
		t.Fatalf("read reply (length %d): %v %v", length, err, rerr)
	}
	reply := nodeFacingReply{status: status}
	if err := json.Unmarshal(body, &reply); err != nil {
		t.Fatalf("decode envelope %s: %v", body, err)
	}
	return reply
}

// assertRefused checks a reply is the JSON-RPC error of kind with exactly msg.
func assertRefused(t *testing.T, name string, got nodeFacingReply, kind cascade.Kind, msg string) {
	t.Helper()
	want := cascade.New(kind, msg).Error()
	if got.Error == nil || got.Error.Code != kind.JSONRPCCode() || got.Error.Message != want {
		t.Fatalf("%s: got %+v (status %d), want code %d %q", name, got.Error, got.status, kind.JSONRPCCode(), want)
	}
}

// TestNodeFacingCallerIsNodePrincipal: every node-facing handler sees the
// node principal of the channel, and identity headers are refused 400
// before any handler runs.
func TestNodeFacingCallerIsNodePrincipal(t *testing.T) {
	ctx := context.Background()
	h := newNodeFacingHarness(t)
	for _, hdr := range [][]string{{principal.HeaderOwnerCLI, "owner-cli"}, {principal.HeaderToken, "a-valid-looking-token"}} {
		got := nodeFacingCall(ctx, t, h.f, h.nodeID, nodeHeartbeatMethod, h.build(1), hdr...)
		if got.status != 400 {
			t.Fatalf("%s: status %d, want 400", hdr[0], got.status)
		}
		assertRefused(t, hdr[0], got, cascade.KindInvalidInput, "nodes: a node-facing request carries no caller identity header")
	}
	if n := h.sawCount(); n != 0 {
		t.Fatalf("an identity-header request reached %d handlers", n)
	}
	h.attempts.Next("d1")
	nonce := bytes.Repeat([]byte{7}, 32)
	calls := map[string]any{
		nodeChannelHelloMethod: controllerHelloParams{Nonce: nonce},
		nodeHeartbeatMethod:    h.build(1),
		DispatchClaimMethod:    ClaimRequest{NodeID: h.nodeID},
		DispatchReportMethod:   DispatchFrame{NodeID: h.nodeID, DispatchID: "d1", Attempt: 1},
		DispatchJournalMethod:  JournalRecord{DispatchID: "d1", Attempt: 1, EntityID: "e1", OperationID: "op1"},
	}
	for method, params := range calls {
		_ = nodeFacingCall(ctx, t, h.f, h.nodeID, method, params)
	}
	for _, method := range h.f.Registry().Methods() {
		h.mu.Lock()
		c, ok := h.seen[method]
		h.mu.Unlock()
		if !ok || c.Principal == nil {
			t.Fatalf("%s: handler saw no principal", method)
		}
		if c.Principal.Kind != principal.KindNode || c.Principal.ID != h.nodeID || c.OwnerCLI {
			t.Fatalf("%s: caller %+v owner=%v, want node principal %q", method, *c.Principal, c.OwnerCLI, h.nodeID)
		}
	}
	if len(h.f.Registry().Methods()) != len(calls) {
		t.Fatalf("node-facing registry lists %v, want exactly the five node verbs", h.f.Registry().Methods())
	}
}

// TestNodeFacingRefusesNonNodeAccess: a method declared OwnerOnly, Open or
// not at all is refused before its handler; an unknown method is not found.
func TestNodeFacingRefusesNonNodeAccess(t *testing.T) {
	ctx := context.Background()
	h := newNodeFacingHarness(t)
	ran := 0
	for _, method := range []string{"approval.deny", "status.get", "nodefacing.undeclared.probe"} {
		h.f.Registry().Register(method, func(context.Context, json.RawMessage) (any, error) {
			ran++
			return "ran", nil
		})
		got := nodeFacingCall(ctx, t, h.f, h.nodeID, method, ClaimRequest{NodeID: h.nodeID})
		assertRefused(t, method, got, cascade.KindPermissionDenied, "nodes: "+method+" is not a node-facing verb")
	}
	if ran != 0 || h.sawCount() != 0 {
		t.Fatalf("a non-node method reached its handler (ran %d, probe %d)", ran, h.sawCount())
	}
	got := nodeFacingCall(ctx, t, h.f, h.nodeID, "node.never.registered", ClaimRequest{NodeID: h.nodeID})
	if got.Error == nil || got.Error.Code != -32601 {
		t.Fatalf("unregistered method: got %+v, want method-not-found", got.Error)
	}
}

// claimStillOpen reports whether the published dispatch is still unclaimed.
func claimStillOpen(rv *Rendezvous, dispatchID string) bool {
	rv.mu.Lock()
	defer rv.mu.Unlock()
	p, ok := rv.pending[dispatchID]
	return ok && !p.claimed
}

func (h *nodeFacingHarness) publish(t *testing.T) <-chan DispatchFrame {
	t.Helper()
	waiter, err := h.rv.publish(h.nodeID, Attempt{DispatchID: "d1", Attempt: 1, NodeID: h.nodeID, Branch: "dispatch/d1/1"})
	if err != nil {
		t.Fatal(err)
	}
	return waiter
}

// TestDrainedNodeRefusesClaim: a drained node's claim is refused while its
// heartbeat and in-flight report land; a revoked or removed node is refused
// claim and heartbeat with no state change.
func TestDrainedNodeRefusesClaim(t *testing.T) {
	ctx := context.Background()
	h := newNodeFacingHarness(t)
	waiter := h.publish(t)
	if _, err := h.records.Drain(h.nodeID); err != nil {
		t.Fatal(err)
	}
	got := nodeFacingCall(ctx, t, h.f, h.nodeID, DispatchClaimMethod, ClaimRequest{NodeID: h.nodeID})
	assertRefused(t, "drained claim", got, cascade.KindPermissionDenied, `nodes: node "`+h.nodeID+`" is drained and takes no new work`)
	if !claimStillOpen(h.rv, "d1") {
		t.Fatal("a refused claim consumed the published dispatch")
	}
	if got := nodeFacingCall(ctx, t, h.f, h.nodeID, nodeHeartbeatMethod, h.build(1)); got.Error != nil {
		t.Fatalf("drained heartbeat refused: %+v", got.Error)
	}
	report := DispatchFrame{NodeID: h.nodeID, DispatchID: "d1", Attempt: 1}
	if got := nodeFacingCall(ctx, t, h.f, h.nodeID, DispatchReportMethod, report); got.Error != nil {
		t.Fatalf("drained in-flight report refused: %+v", got.Error)
	}
	if frame := <-waiter; frame.DispatchID != "d1" {
		t.Fatalf("report delivered %+v", frame)
	}
	assertRecordStateRefusals(ctx, t, "revoked", func(h *nodeFacingHarness) error { _, err := h.records.Revoke(h.nodeID); return err })
	assertRecordStateRefusals(ctx, t, "not enrolled", func(h *nodeFacingHarness) error { return h.records.Remove(h.nodeID) })
}

// assertRecordStateRefusals applies change to a fresh node, then checks claim
// and heartbeat are refused for reason and leave the claim and LastSeen alone.
func assertRecordStateRefusals(ctx context.Context, t *testing.T, reason string, change func(*nodeFacingHarness) error) {
	t.Helper()
	h := newNodeFacingHarness(t)
	h.publish(t)
	if got := nodeFacingCall(ctx, t, h.f, h.nodeID, nodeHeartbeatMethod, h.build(1)); got.Error != nil {
		t.Fatalf("%s: first heartbeat refused: %+v", reason, got.Error)
	}
	before, _ := h.records.Get(h.nodeID)
	if err := change(h); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(time.Minute)
	want := `nodes: node "` + h.nodeID + `" is ` + reason
	got := nodeFacingCall(ctx, t, h.f, h.nodeID, DispatchClaimMethod, ClaimRequest{NodeID: h.nodeID})
	assertRefused(t, reason+" claim", got, cascade.KindPermissionDenied, want)
	got = nodeFacingCall(ctx, t, h.f, h.nodeID, nodeHeartbeatMethod, h.build(2))
	assertRefused(t, reason+" heartbeat", got, cascade.KindPermissionDenied, want)
	if !claimStillOpen(h.rv, "d1") {
		t.Fatalf("%s: a refused claim consumed the published dispatch", reason)
	}
	if after, err := h.records.Get(h.nodeID); err == nil && !after.LastSeen.Equal(before.LastSeen) {
		t.Fatalf("%s: a refused heartbeat moved LastSeen", reason)
	}
}

// TestNodeFacingJournalNeedsNoParamsNodeID: a journal record carries no
// node_id, yet it reaches its handler and the journal sink.
func TestNodeFacingJournalNeedsNoParamsNodeID(t *testing.T) {
	h := newNodeFacingHarness(t)
	if !h.f.Registry().Registered(DispatchJournalMethod) {
		t.Fatal("node.dispatch.journal is not mounted on the node-facing registry")
	}
	h.attempts.Next("d1")
	rec := JournalRecord{DispatchID: "d1", Attempt: 1, EntityID: "e1", OperationID: "op1", Payload: json.RawMessage(`{"step":1}`)}
	if raw, err := json.Marshal(rec); err != nil || strings.Contains(string(raw), "node_id") {
		t.Fatalf("journal params %s carry a node_id (%v)", raw, err)
	}
	if got := nodeFacingCall(context.Background(), t, h.f, h.nodeID, DispatchJournalMethod, rec); got.Error != nil {
		t.Fatalf("journal call refused: %+v", got.Error)
	}
	if len(h.sink.entities) != 1 || h.sink.entities[0] != "e1" || h.sink.operation[0] != "op1" {
		t.Fatalf("journal sink got %v %v, want one append for e1/op1", h.sink.entities, h.sink.operation)
	}
}
