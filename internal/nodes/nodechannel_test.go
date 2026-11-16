package nodes

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

const channelFP = "SHA256:node-channel-test"

// channelFixture is a NodeChannel over a node-facing harness's records,
// with fake tunnels whose sessions answer node.sock from reply.
type channelFixture struct {
	h       *nodeFacingHarness
	m       *Manager
	nc      *NodeChannel
	ticker  *fakeTicker
	changes chan struct{}
	kh      *KnownHosts

	mu    sync.Mutex
	dials []string
	reply func(rpc.Request) string
}

func testRoute() Route {
	return Route{User: "worker", Addr: "node.invalid:22", HostKeyFP: channelFP, SocketDir: "/srv/node/nodes"}
}

func newChannelFixture(t *testing.T) *channelFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	h := newNodeFacingHarness(t)
	f := &channelFixture{h: h, m: NewManager(), ticker: newFakeTicker(), changes: make(chan struct{}, 1),
		kh: NewKnownHosts(newMemKnownHostsBackend())}
	f.nc = NewNodeChannel(f.m, h.records)
	f.nc.SetReconcileDeps(ReconcileDeps{
		Configure: func(target Target, route Route) TunnelConfig {
			_ = f.kh.Pin(target.knownHostsKey(), route.HostKeyFP, false) // a failed pin fails the dial, so the wait fails
			outcome := func() (Session, error) { return &nodeSockSession{fakeSession: newFakeSession(), f: f}, nil }
			return TunnelConfig{Dialer: &fakeDialer{fp: route.HostKeyFP, outcomes: []func() (Session, error){outcome}},
				KnownHosts: f.kh, Sleeper: &fakeSleeper{}, LocalDial: h.f.DialAs(target.NodeID)}
		},
		NewTicker: func(time.Duration) Ticker { return f.ticker },
		Changes:   f.changes,
	})
	return f
}

// start runs Reconcile until the test ends.
func (f *channelFixture) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); f.nc.Reconcile(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
}

func (f *channelFixture) dialCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.dials)
}

func setRoute(t *testing.T, records *RecordStore, nodeID string, route Route) {
	t.Helper()
	rec, err := records.Get(nodeID)
	if err != nil {
		t.Fatal(err)
	}
	rec.NodeRoute = route
	if err := records.put(rec); err != nil {
		t.Fatal(err)
	}
}

func waitChannelState(t *testing.T, m *Manager, nodeID string, want TunnelState, registered bool) {
	t.Helper()
	if !eventually(5*time.Second, func() bool { s, ok := m.State(nodeID); return ok == registered && (!ok || s == want) }) {
		s, ok := m.State(nodeID)
		t.Fatalf("tunnel %s: state %v registered %v, want %v registered %v", nodeID, s, ok, want, registered)
	}
}

func assertCascadeErr(t *testing.T, name string, err error, kind cascade.Kind, msg string) {
	t.Helper()
	got, ok := cascade.KindOf(err)
	if err == nil || !ok || got != kind || err.Error() != cascade.New(kind, msg).Error() {
		t.Fatalf("%s: got %v, want %v %q", name, err, kind, msg)
	}
}

// nodeSockSession answers DialUnix with an in-memory node.sock that
// replies through its fixture.
type nodeSockSession struct {
	*fakeSession
	f *channelFixture
}

func (s *nodeSockSession) DialUnix(p string) (Conn, error) {
	s.f.mu.Lock()
	s.f.dials = append(s.f.dials, p)
	reply := s.f.reply
	s.f.mu.Unlock()
	reqR, reqW := io.Pipe()
	respR, respW := io.Pipe()
	go serveFakeNodeSock(bufio.NewReader(reqR), respW, reply)
	return pipeConnPair{Reader: respR, Writer: reqW, closers: []io.Closer{reqW, respR}}, nil
}

type pipeConnPair struct {
	io.Reader
	io.Writer
	closers []io.Closer
}

func (p pipeConnPair) Close() error {
	for _, c := range p.closers {
		_ = c.Close()
	}
	return nil
}

// serveFakeNodeSock reads one raw HTTP/1.1 request and writes reply's body.
func serveFakeNodeSock(r *bufio.Reader, w *io.PipeWriter, reply func(rpc.Request) string) {
	defer func() { _ = w.Close() }()
	length := 0
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		if strings.TrimSpace(line) == "" {
			break
		}
		if name, value, ok := strings.Cut(strings.TrimSpace(line), ":"); ok && strings.EqualFold(name, "Content-Length") {
			length, _ = strconv.Atoi(strings.TrimSpace(value))
		}
	}
	body := make([]byte, length)
	var req rpc.Request
	if _, err := io.ReadFull(r, body); err != nil || json.Unmarshal(body, &req) != nil {
		return
	}
	out := reply(req)
	_, _ = fmt.Fprintf(w, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(out), out)
}

// TestNodeChannelCallNoTunnelUnavailable: without an up tunnel Call is
// exactly KindUnavailable and dials nothing; over an up tunnel a JSON-RPC
// error object comes back as its typed error and a result decodes.
func TestNodeChannelCallNoTunnelUnavailable(t *testing.T) {
	ctx := context.Background()
	f := newChannelFixture(t)
	id := f.h.nodeID
	want := "node channel: no tunnel up for " + id
	setRoute(t, f.h.records, id, testRoute())
	assertCascadeErr(t, "never started", f.nc.Call(ctx, id, "node.ping", nil, nil), cascade.KindUnavailable, want)
	assertCascadeErr(t, "unknown node", f.nc.Caller("other")(ctx, "node.ping", nil, nil), cascade.KindUnavailable, "node channel: no tunnel up for other")
	if f.dialCount() != 0 {
		t.Fatalf("a refused call dialled %v", f.dials)
	}
	f.start(t)
	waitChannelState(t, f.m, id, TunnelUp, true)
	assertTypedReplies(ctx, t, f)
	setRoute(t, f.h.records, id, Route{User: "worker", Addr: "node.invalid:22"})
	assertCascadeErr(t, "incomplete route", f.nc.Call(ctx, id, "node.ping", nil, nil), cascade.KindUnavailable, want)
	if f.dialCount() != 2 {
		t.Fatalf("dials = %v, want exactly the two calls over the up tunnel", f.dials)
	}
}

func assertTypedReplies(ctx context.Context, t *testing.T, f *channelFixture) {
	t.Helper()
	code := cascade.KindNotFound.JSONRPCCode()
	f.mu.Lock()
	f.reply = func(rpc.Request) string {
		return fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"error":{"code":%d,"message":"not-found: nodes: no such repo"}}`, code)
	}
	f.mu.Unlock()
	err := f.nc.Call(ctx, f.h.nodeID, "ci.attest.put", map[string]string{"repo": "r"}, nil)
	assertCascadeErr(t, "rpc error", err, cascade.KindNotFound, "nodes: no such repo")
	if got := f.dials[0]; got != "/srv/node/nodes/"+NodeSocketName {
		t.Fatalf("dialled %q, want the node.sock under the route socket dir", got)
	}
	f.mu.Lock()
	f.reply = func(req rpc.Request) string {
		return `{"jsonrpc":"2.0","id":1,"result":{"method":"` + req.Method + `"}}`
	}
	f.mu.Unlock()
	var out struct{ Method string }
	if err := f.nc.Caller(f.h.nodeID)(ctx, "node.ping", nil, &out); err != nil || out.Method != "node.ping" {
		t.Fatalf("result call: %+v %v", out, err)
	}
}
