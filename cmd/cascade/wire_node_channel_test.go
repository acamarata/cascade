//go:build !windows

// Package main tests: tunnels start only on the controller, through Reconcile,
// with the node-facing registry as the far end and the installation key as
// every channel key. No socket opens: the session hands over in-memory pipes.
package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/runtime"
	scopeenvelope "github.com/acamarata/cascade/pkg/envelope"
)

const testChannelHostFP = "SHA256:node-channel-fixture"

var testChannelRoute = nodes.Route{User: "cascade", Addr: "127.0.0.1:2222", HostKeyFP: testChannelHostFP, SocketDir: "/srv/node/nodes"}

// channelWire is a data dir, a wiring over it and what the dialer seam saw.
type channelWire struct {
	w         *daemonWiring
	dataDir   string
	signers   []scopeenvelope.Signer
	dials     []string
	listeners chan *channelListener
}

func newChannelWire(t *testing.T) *channelWire {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	settleDesignation(dataDir)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := &daemonWiring{Ctx: ctx, Manifest: testManifest(), Clock: runtime.SystemClock{}, Paths: fakeDaemonPaths{root: root},
		Deps: daemonRuntime{Daemon: daemonDeps{NodeTunnels: nodes.NewManager()}}}
	return &channelWire{w: w, dataDir: dataDir, listeners: make(chan *channelListener, 8)}
}

func (c *channelWire) wire(t *testing.T) {
	t.Helper()
	err := wireNodeChannelWith(c.w, nodeChannelSeams{
		newDialer: func(s scopeenvelope.Signer, _ string) nodes.Dialer {
			c.signers = append(c.signers, s)
			return c
		},
		newTicker: func(time.Duration) nodes.Ticker { return nodes.NewSystemTicker(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Dial presents the enrolled host key; the test drives the session.
func (c *channelWire) Dial(_ context.Context, target nodes.Target, verify nodes.HostKeyVerifier) (nodes.Session, error) {
	if err := verify(testChannelHostFP); err != nil {
		return nil, err
	}
	c.dials = append(c.dials, target.NodeID)
	return channelSession{listeners: c.listeners, nodeID: target.NodeID}, nil
}

func (c *channelWire) listener(t *testing.T) *channelListener {
	t.Helper()
	select {
	case l := <-c.listeners:
		return l
	case <-time.After(5 * time.Second):
	}
	t.Fatal("no tunnel opened its remote forward")
	return nil
}

// enroll adds a node record carrying route.
func (c *channelWire) enroll(t *testing.T, route nodes.Route) string {
	t.Helper()
	id, _, err := nodes.GenerateIdentity(rand.Reader)
	backend := nodes.NewFileRecordBackend(c.dataDir)
	if err == nil {
		_, err = nodes.NewRecordStore(backend, runtime.SystemClock{}).Enroll(id, nodes.TierWorkerTrusted)
	}
	all := map[string]nodes.DeviceRecord{}
	if err == nil {
		all, err = backend.Load()
	}
	rec := all[id.NodeID]
	rec.NodeRoute = route
	all[id.NodeID] = rec
	if err != nil || backend.Save(all) != nil {
		t.Fatalf("enroll a routed node: %v", err)
	}
	return id.NodeID
}

func (c *channelWire) status() daemon.SubsystemStatus {
	for _, s := range c.w.Manifest.Snapshot() {
		if s.Name == nodeChannelSubsystem {
			return s
		}
	}
	return daemon.SubsystemStatus{}
}

// pin is the key `node enroll --controller-endpoint` prints.
func (c *channelWire) pin(ctx context.Context, t *testing.T) ed25519.PublicKey {
	t.Helper()
	b64, err := controllerPubKeyB64(ctx, c.w.Paths)
	pub, derr := base64.StdEncoding.DecodeString(b64)
	if err != nil || derr != nil || len(pub) != ed25519.PublicKeySize {
		t.Fatalf("controller pubkey %q: %v %v", b64, err, derr)
	}
	return pub
}

// channelSession adds the remote-listen half to fakeSession.
type channelSession struct {
	fakeSession
	listeners chan<- *channelListener
	nodeID    string
}

func (s channelSession) ListenUnix(path string) (nodes.Listener, error) {
	l := &channelListener{path: path, nodeID: s.nodeID, conns: make(chan nodes.Conn), done: make(chan struct{})}
	s.listeners <- l
	return l, nil
}

type channelListener struct {
	path, nodeID string
	conns        chan nodes.Conn
	done         chan struct{}
	once         sync.Once
}

func (l *channelListener) Accept() (nodes.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, io.ErrClosedPipe
	}
}

func (l *channelListener) Close() error { l.once.Do(func() { close(l.done) }); return nil }

// duplexPipe is one end of an in-memory connection.
type duplexPipe struct {
	*io.PipeReader
	*io.PipeWriter
}

func (p duplexPipe) Close() error { _ = p.PipeWriter.Close(); return p.PipeReader.Close() }

// exchange sends one POST through the tunnel as the node does; status, body.
func (l *channelListener) exchange(t *testing.T, header, body string) (int, string) {
	t.Helper()
	ar, aw := io.Pipe()
	br, bw := io.Pipe()
	local := duplexPipe{br, aw}
	defer func() { _ = local.Close() }()
	l.conns <- duplexPipe{ar, bw}
	go func() {
		_, _ = fmt.Fprintf(local, "POST %s HTTP/1.1\r\nHost: node\r\nContent-Type: application/json\r\n%sContent-Length: %d\r\n\r\n%s",
			nodes.RPCPath, header, len(body), body)
	}()
	r := bufio.NewReader(local)
	line, err := r.ReadString('\n')
	fields, length := strings.Fields(line), 0
	for err == nil {
		var h string
		if h, err = r.ReadString('\n'); h == "\r\n" {
			break
		}
		if k, v, ok := strings.Cut(h, ":"); ok && strings.EqualFold(k, "Content-Length") {
			length, _ = strconv.Atoi(strings.TrimSpace(v))
		}
	}
	reply := make([]byte, length)
	if _, rerr := io.ReadFull(r, reply); err != nil || rerr != nil || len(fields) < 2 {
		t.Fatalf("far end reply %q: %v %v", line, err, rerr)
	}
	status, _ := strconv.Atoi(fields[1])
	return status, string(reply)
}

// assertHelloPinned: a hello through the tunnel is signed for l's node by pin.
func assertHelloPinned(t *testing.T, l *channelListener, pin ed25519.PublicKey) {
	t.Helper()
	nonce := []byte(strings.Repeat("n", 32))
	params, _ := json.Marshal(map[string][]byte{"nonce": nonce})
	status, raw := l.exchange(t, "", `{"jsonrpc":"2.0","id":1,"method":"node.channel.hello","params":`+string(params)+`}`)
	var env struct {
		Result struct{ Signature []byte } `json:"result"`
		Error  json.RawMessage            `json:"error"`
	}
	if err := json.Unmarshal([]byte(raw), &env); err != nil || status != 200 || env.Error != nil {
		t.Fatalf("hello through the tunnel: %d %s (%v)", status, raw, err)
	}
	if err := nodes.VerifyControllerHello(pin, l.nodeID, nonce, env.Result.Signature); err != nil {
		t.Fatalf("hello not signed by the pinned controller key: %v", err)
	}
}

// TestTunnelFarEndIsNodeFacing: the tunnel forwards controller.sock to the node-facing
// registry as that node: an owner-CLI header gets 400, a hello answers for the node.
func TestTunnelFarEndIsNodeFacing(t *testing.T) {
	c := newChannelWire(t)
	pinInstallationSigner(t)
	writeTestDesignation(t, c.dataDir)
	id := c.enroll(t, testChannelRoute)
	c.wire(t)
	l := c.listener(t)
	if want := testChannelRoute.SocketDir + "/controller.sock"; l.path != want || l.nodeID != id {
		t.Fatalf("remote forward %s for %s, want %s for %s", l.path, l.nodeID, want, id)
	}
	status, body := l.exchange(t, principal.HeaderOwnerCLI+": cli\r\n", `{"jsonrpc":"2.0","id":1,"method":"node.heartbeat","params":{}}`)
	if status != 400 || !strings.Contains(body, "carries no caller identity header") {
		t.Fatalf("owner-CLI header through the tunnel: %d %s, want the node-facing 400", status, body)
	}
	assertHelloPinned(t, l, c.pin(context.Background(), t))
	if c.w.Deps.NodeChannel.Load() == nil || c.w.Deps.NodeFacing == nil || c.w.Deps.Rendezvous == nil {
		t.Fatal("node channel, node-facing registry or rendezvous not exposed through daemonRuntime")
	}
}

// TestWireNodeChannelSkipsOnNodeRole: a node or role conflict records Skipped and starts
// nothing; on the controller a routeless record gets no tunnel and the manifest names it.
func TestWireNodeChannelSkipsOnNodeRole(t *testing.T) {
	for designation, want := range map[bool]string{false: "role node: node tunnels start only on the controller", true: "role does not resolve: "} {
		c := newChannelWire(t)
		if designation {
			writeTestDesignation(t, c.dataDir)
		}
		writeTestBinding(t, c.dataDir, nodes.ControllerBinding{Endpoint: "controller.example.invalid:7000", EnrollmentID: "enroll-fixture", ControllerPubKeyB64: "cGlubmVkLWtleQ"})
		id := c.enroll(t, testChannelRoute)
		c.wire(t)
		if s := c.status(); s.State != daemon.SubsystemSkipped || !strings.Contains(s.Detail, want) {
			t.Fatalf("status %+v, want skipped with %q", s, want)
		}
		if _, ok := c.w.Deps.Daemon.NodeTunnels.State(id); ok || len(c.signers) != 0 || c.w.Deps.NodeChannel.Load() != nil || c.w.Deps.NodeFacing != nil {
			t.Fatalf("%q: a channel was composed or a tunnel registered", want)
		}
	}
	c := newChannelWire(t)
	writeTestDesignation(t, c.dataDir)
	bare := c.enroll(t, nodes.Route{User: "cascade", Addr: "127.0.0.1:2222"})
	routed := c.enroll(t, testChannelRoute)
	c.wire(t)
	if l := c.listener(t); l.nodeID != routed {
		t.Fatalf("tunnel opened for %s, want %s", l.nodeID, routed)
	}
	if _, ok := c.w.Deps.Daemon.NodeTunnels.State(bare); ok || strings.Join(c.dials, ",") != routed {
		t.Fatalf("dials %v; the routeless node %s must get no tunnel", c.dials, bare)
	}
	if s := c.status(); s.State != daemon.SubsystemRunning || !strings.Contains(s.Detail, "no complete route (no tunnel): "+bare) {
		t.Fatalf("status %+v does not name the routeless node", s)
	}
}

// TestChannelKeysAreThePin: userauth, hello and NodeFacingDeps.Signer are the printed pin.
func TestChannelKeysAreThePin(t *testing.T) {
	ctx := context.Background()
	c := newChannelWire(t)
	pinInstallationSigner(t)
	writeTestDesignation(t, c.dataDir)
	c.enroll(t, testChannelRoute)
	c.wire(t)
	pin := c.pin(ctx, t)
	if len(c.signers) != 1 || !pin.Equal(c.signers[0].PublicKey()) {
		t.Fatalf("ssh userauth signers %d; the key is not the pin", len(c.signers))
	}
	if sig, err := c.signers[0].Sign(ctx, []byte("probe")); err != nil || !ed25519.Verify(pin, []byte("probe"), sig) {
		t.Fatalf("userauth signer does not sign with the pin: %v", err)
	}
	assertHelloPinned(t, c.listener(t), pin)
}
