//go:build integration

// Purpose: Art.2's real-counterpart proof for the transport half of remote
//   dispatch — the node-facing dispatch verbs answered over a REAL
//   ssh-forwarded socket, served by a REAL /usr/sbin/sshd on loopback.
//
//   The controller serves the verbs on its own unix socket with ConnContext
//   set, exactly as the composition root does: internal/rpc's handler
//   refuses a peer it cannot prove owns the socket, so a TCP listener here
//   would be testing a configuration that never ships.
//
//   This is the direction the D-24 tunnel actually runs
//   (journals/RULING-dispatch-direction-reverse-tunnel.md): the node dials
//   out, so the verbs it calls are mounted on the CONTROLLER.
//
//   Reuses tunnel_test.go's spawnLoopbackSSHD/currentUser/fakeSleeper.
// SPORT: internal/nodes TestDispatchRealSSHD (P1-E17-W4-S37-T2).

package nodes

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
)

// waitForRemoteSocket blocks until the forwarded socket accepts a
// connection, so a test asserting over the tunnel fails for what it is
// about rather than for arriving early.
func waitForRemoteSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := (&net.Dialer{}).DialContext(context.Background(), "unix", path)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the forwarded socket %s never accepted a connection", path)
}

// dispatchOverTunnel issues one JSON-RPC call to the node-forwarded socket
// and returns the decoded response body.
func dispatchOverTunnel(t *testing.T, remoteSock, method, params string) map[string]any {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", remoteSock)
		},
	}}
	body := `{"jsonrpc":"2.0","method":"` + method + `","params":` + params + `,"id":1}`
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"http://unix"+RPCPath, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s over the real tunnel: %v", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s response: %v", method, err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode %s response (status %d): %v\nbody: %s", method, resp.StatusCode, err, raw)
	}
	return decoded
}

// TestDispatchRealSSHD proves the node-facing dispatch verbs answer over a
// REAL ssh-forwarded socket: the controller serves them on its own local
// endpoint, the tunnel forwards the node's connections to it, and a claim
// placed on that node comes back over the wire.
//
// This is the direction the D-24 tunnel actually runs
// (journals/RULING-dispatch-direction-reverse-tunnel.md): the node dials
// out, so the verbs it calls are mounted on the CONTROLLER.
func TestDispatchRealSSHD(t *testing.T) {
	ks, err := NewNodeKeystoreForTest(t)
	if err != nil {
		t.Fatalf("keystore: %v", err)
	}
	ident, priv, err := GenerateIdentity(rand.Reader)
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	if err := ks.Store(context.Background(), ident.NodeID, priv); err != nil {
		t.Fatalf("store key: %v", err)
	}

	// The controller's own RPC endpoint, carrying the dispatch verbs.
	rendezvous := NewRendezvous()
	reg := rpc.NewRegistry()
	RegisterDispatchNodeHandlers(reg, rendezvous)
	registry := &ServeRegistry{reg: reg}
	// Served over a UNIX socket with ConnContext set, exactly as the
	// composition root serves it: internal/rpc's handler refuses a peer it
	// cannot prove owns the socket, so a TCP listener here would be
	// testing a configuration that never ships.
	controller := &http.Server{
		Handler:           registry.Handler(),
		ConnContext:       ConnContext,
		ReadHeaderTimeout: 5 * time.Second,
	}
	// A short base dir: a unix socket path is capped near 104 bytes and
	// t.TempDir's is already most of that on macOS.
	sockDir, err := os.MkdirTemp("/tmp", "cd")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	ln, err := net.Listen("unix", filepath.Join(sockDir, "c.sock"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = controller.Serve(ln) }()
	t.Cleanup(func() { _ = controller.Close() })

	port, fp := spawnLoopbackSSHD(t, ident.PubKey)
	target := Target{NodeID: "real-node", User: currentUser(t), Addr: "127.0.0.1:" + strconv.Itoa(port)}
	kh := NewKnownHosts(newMemKnownHostsBackend())
	if err := kh.Pin(target.knownHostsKey(), fp, false); err != nil {
		t.Fatalf("seed pin: %v", err)
	}
	remoteSock := filepath.Join(sockDir, "n.sock")
	tun := newTunnel(TunnelConfig{
		Target: target, Dialer: NewSSHDialer(ks, ident.NodeID, ident.PubKey, 5*time.Second),
		KnownHosts: kh, Sleeper: &fakeSleeper{},
		RemoteSocketPath: remoteSock,
		LocalDial: func(ctx context.Context) (Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", ln.Addr().String())
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tun.run(ctx)

	deadline := time.Now().Add(10 * time.Second)
	for tun.State() != TunnelUp && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if tun.State() != TunnelUp {
		t.Fatalf("tunnel never reached TunnelUp against the real sshd (state=%v)", tun.State())
	}
	// TunnelUp means the SSH session is established, not that sshd has
	// finished creating the forwarded socket on the remote side. The two
	// are separate events and the gap widens on a loaded machine: under
	// `-race` with the rest of the package running, the first call here
	// failed with "no such file or directory" for n.sock. Wait for the
	// carrier to actually carry before asserting anything over it.
	waitForRemoteSocket(t, remoteSock)

	// A claim for a node with nothing placed on it is refused, over the
	// real wire, as a typed error rather than an empty success.
	refused := dispatchOverTunnel(t, remoteSock, DispatchClaimMethod, `{"node_id":"real-node"}`)
	if refused["error"] == nil {
		t.Fatalf("a claim with no work placed returned %v, want an error", refused)
	}

	// Now place work and claim it over the same real tunnel.
	attempt := Attempt{DispatchID: "d-ssh", Attempt: 7, NodeID: "real-node", Branch: "dispatch/d-ssh/7"}
	if _, err := rendezvous.publish("real-node", attempt); err != nil {
		t.Fatalf("publish: %v", err)
	}
	claimed := dispatchOverTunnel(t, remoteSock, DispatchClaimMethod, `{"node_id":"real-node"}`)
	result, ok := claimed["result"].(map[string]any)
	if !ok {
		t.Fatalf("claim over the real tunnel returned %v", claimed)
	}
	if result["dispatch_id"] != "d-ssh" || result["branch"] != "dispatch/d-ssh/7" {
		t.Fatalf("claimed %v, want the attempt that was placed on this node", result)
	}

	// And a report from a SUPERSEDED attempt is refused over the wire too,
	// so fencing holds across the real transport and not only in-process.
	frame := DispatchFrame{DispatchID: "d-ssh", Attempt: 6, NodeID: "real-node", Outcome: OutcomeSucceeded}
	frame.SignatureB64 = base64.StdEncoding.EncodeToString([]byte("unused-here"))
	encoded, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	stale := dispatchOverTunnel(t, remoteSock, DispatchReportMethod, string(encoded))
	if stale["error"] == nil {
		t.Fatalf("a superseded report was accepted over the real tunnel: %v", stale)
	}
}
