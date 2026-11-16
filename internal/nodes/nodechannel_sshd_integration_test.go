//go:build integration

// Purpose: the real-counterpart proof of the node channel. A REAL
//   /usr/sbin/sshd on loopback plays the node: NodeChannel.Call reaches the
//   node's node.sock through direct-streamlocal on the tunnel's session,
//   and a heartbeat sent into the remote-forwarded controller.sock is
//   handled by the node-facing registry with the enrolled node as its
//   channel identity. The node-facing registry has no socket of its own on
//   the controller: once the tunnel stops, nothing reaches it.
//   Reuses tunnel_test.go's spawnLoopbackSSHD/currentUser and
//   dispatch_sshd_integration_test.go's waitForRemoteSocket and
//   dispatchOverTunnel. No sshd binary skips (a BLOCKED leg, never a pass).

package nodes

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
)

// fakeNodeSock serves node.sock on the "node" (the loopback sshd host):
// every request answers with its own method, and the methods are kept.
type fakeNodeSock struct {
	mu      sync.Mutex
	methods []string
}

func (n *fakeNodeSock) serve(t *testing.T, sockPath string) {
	t.Helper()
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen node.sock: %v", err)
	}
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpc.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		n.mu.Lock()
		n.methods = append(n.methods, r.URL.Path+" "+req.Method)
		n.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"method":"` + req.Method + `"}}`))
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

// realChannel wires a NodeChannel whose reconcile starts a real ssh tunnel
// to the loopback sshd for the harness's enrolled node.
func realChannel(t *testing.T, sockDir string) (*channelFixture, *sync.Map) {
	t.Helper()
	ks, ident := storedTestIdentity(t)
	port, fp := spawnLoopbackSSHD(t, ident.PubKey)
	f := newChannelFixture(t)
	idents := &sync.Map{}
	f.h.f.Registry().Use(func(method string, next rpc.HandlerFunc) rpc.HandlerFunc {
		return func(ctx context.Context, params json.RawMessage) (any, error) {
			if id, ok := ChannelIdentityFrom(ctx); ok {
				idents.Store(method, id)
			}
			return next(ctx, params)
		}
	})
	route := Route{User: currentUser(t), Addr: "127.0.0.1:" + strconv.Itoa(port), HostKeyFP: fp, SocketDir: sockDir}
	setRoute(t, f.h.records, f.h.nodeID, route)
	f.nc.SetReconcileDeps(ReconcileDeps{
		Configure: func(target Target, route Route) TunnelConfig {
			if err := f.kh.Pin(target.knownHostsKey(), route.HostKeyFP, false); err != nil {
				t.Errorf("pin: %v", err)
			}
			return TunnelConfig{Dialer: NewSSHDialer(ks, ident.NodeID, ident.PubKey, 5*time.Second),
				KnownHosts: f.kh, Sleeper: &fakeSleeper{}, LocalDial: f.h.f.DialAs(target.NodeID)}
		},
		NewTicker: func(time.Duration) Ticker { return f.ticker },
	})
	return f, idents
}

// TestNodeChannelRealSSHD: Call reaches node.sock over DialUnix, the
// forwarded controller.sock heartbeat runs as the enrolled node, and the
// node-facing registry is reachable only through the tunnel.
func TestNodeChannelRealSSHD(t *testing.T) {
	// A short base dir: a unix socket path is capped near 104 bytes.
	sockDir, err := os.MkdirTemp("/tmp", "nc")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	node := &fakeNodeSock{}
	node.serve(t, filepath.Join(sockDir, NodeSocketName))
	f, idents := realChannel(t, sockDir)
	id := f.h.nodeID
	f.start(t)
	waitChannelState(t, f.m, id, TunnelUp, true)
	controllerSock := filepath.Join(sockDir, ControllerSocketName)
	waitForRemoteSocket(t, controllerSock)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out struct{ Method string }
	if err := f.nc.Caller(id)(ctx, "node.ping", map[string]string{"x": "y"}, &out); err != nil || out.Method != "node.ping" {
		t.Fatalf("call over the real tunnel: %+v %v", out, err)
	}
	node.mu.Lock()
	got := append([]string(nil), node.methods...)
	node.mu.Unlock()
	if len(got) != 1 || got[0] != RPCPath+" node.ping" {
		t.Fatalf("node.sock saw %v, want one %s node.ping", got, RPCPath)
	}
	assertForwardedHeartbeat(t, f, idents, controllerSock)

	f.m.Stop(id)
	assertNodeFacingUnreachable(t, controllerSock)
}

// assertForwardedHeartbeat sends one heartbeat into the forwarded
// controller.sock and requires the handler ran as the enrolled node.
func assertForwardedHeartbeat(t *testing.T, f *channelFixture, idents *sync.Map, controllerSock string) {
	t.Helper()
	frame, err := json.Marshal(f.h.build(1))
	if err != nil {
		t.Fatal(err)
	}
	reply := dispatchOverTunnel(t, controllerSock, nodeHeartbeatMethod, string(frame))
	if reply["error"] != nil {
		t.Fatalf("heartbeat over the forwarded controller.sock refused: %v", reply)
	}
	v, ok := idents.Load(nodeHeartbeatMethod)
	if !ok || v.(ChannelIdentity).NodeID != f.h.nodeID {
		t.Fatalf("heartbeat channel identity %v (seen %v), want %q", v, ok, f.h.nodeID)
	}
	f.h.mu.Lock()
	c := f.h.seen[nodeHeartbeatMethod]
	f.h.mu.Unlock()
	if c.Principal == nil || c.Principal.Kind != principal.KindNode || c.Principal.ID != f.h.nodeID {
		t.Fatalf("heartbeat caller %+v, want node principal %q", c, f.h.nodeID)
	}
}

// assertNodeFacingUnreachable: with the tunnel stopped, the forwarded
// socket no longer reaches the node-facing registry, which has no socket
// of its own on the controller.
func assertNodeFacingUnreachable(t *testing.T, controllerSock string) {
	t.Helper()
	unreachable := eventually(10*time.Second, func() bool {
		conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(context.Background(), "unix", controllerSock)
		if err != nil {
			return true
		}
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		_, _ = conn.Write([]byte("POST " + RPCPath + " HTTP/1.1\r\nHost: x\r\nContent-Length: 2\r\n\r\n{}"))
		buf := make([]byte, 1)
		_, rerr := conn.Read(buf)
		_ = conn.Close()
		return rerr != nil
	})
	if !unreachable {
		t.Fatal("the node-facing registry still answers after the tunnel stopped")
	}
}
