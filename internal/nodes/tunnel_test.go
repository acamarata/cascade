//go:build integration

// Purpose: Art.2's real-counterpart proof — the ssh tunnel exercised
//
//	against a REAL sshd (loopback, spawned by this test), never a
//	self-authored dialect. Tagged `integration` because it imports "net"
//	and dials a real socket (Art.7.2's default-unit-lane rule).
//
// SPORT: internal/nodes TestTunnelRealSSHD (P1-E17-W4-S36-T3).

package nodes

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// spawnLoopbackSSHD generates a fresh ed25519 host key and an
// authorized_keys entry for clientPub, then launches a REAL /usr/sbin/sshd
// (OpenSSH — see testdata/README.md for provenance) on 127.0.0.1 at a free
// port, returning that port. Skips (not fails) when sshd cannot run here,
// so this lane degrades gracefully on a restricted sandbox rather than
// reporting a false negative.
func spawnLoopbackSSHD(t *testing.T, clientPub ed25519.PublicKey) (port int, hostKeyFingerprint string) {
	t.Helper()
	sshdPath := findSSHD(t)
	dir := t.TempDir()

	hostPub, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	hostSSHPub, err := ssh.NewPublicKey(hostPub)
	if err != nil {
		t.Fatalf("host pub: %v", err)
	}
	hostKeyFingerprint = HostKeyFingerprint(hostSSHPub.Marshal())
	block, err := ssh.MarshalPrivateKey(hostPriv, "")
	if err != nil {
		t.Fatalf("marshal host key: %v", err)
	}
	hostKeyPath := filepath.Join(dir, "hostkey")
	if err := os.WriteFile(hostKeyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write host key: %v", err)
	}

	sshPub, err := ssh.NewPublicKey(clientPub)
	if err != nil {
		t.Fatalf("client pub: %v", err)
	}
	authorizedKeysPath := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(authorizedKeysPath, ssh.MarshalAuthorizedKey(sshPub), 0o600); err != nil {
		t.Fatalf("write authorized_keys: %v", err)
	}

	port = freePort(t)
	cfgPath := filepath.Join(dir, "sshd_config")
	cfg := strings.Join([]string{
		"Port " + strconv.Itoa(port), "ListenAddress 127.0.0.1", "HostKey " + hostKeyPath,
		"AuthorizedKeysFile " + authorizedKeysPath, "PubkeyAuthentication yes",
		"PasswordAuthentication no", "UsePAM no", "StrictModes no", "LogLevel ERROR",
		"Subsystem sftp none", "AllowStreamLocalForwarding yes", "StreamLocalBindUnlink yes",
	}, "\n") + "\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write sshd_config: %v", err)
	}

	cmd := exec.Command(sshdPath, "-f", cfgPath, "-D", "-e")
	if err := cmd.Start(); err != nil {
		t.Skipf("could not start sshd (sandboxed environment): %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	waitForPort(t, port)
	return port, hostKeyFingerprint
}

func findSSHD(t *testing.T) string {
	t.Helper()
	for _, p := range []string{"/usr/sbin/sshd", "/usr/bin/sshd"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("no sshd binary found; skipping real-counterpart lane")
	return ""
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

func waitForPort(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("sshd did not become reachable")
}

// localEchoServer is the LocalDial target: whatever it reads, it echoes
// back verbatim, so a round trip through the real ssh-forwarded socket is
// provable from outside.
type localEchoServer struct {
	l net.Listener
}

func newLocalEchoServer(t *testing.T) *localEchoServer {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	s := &localEchoServer{l: l}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c) }()
		}
	}()
	return s
}

func (s *localEchoServer) Addr() string { return s.l.Addr().String() }
func (s *localEchoServer) Close()       { _ = s.l.Close() }

// roundTripThroughRemoteSocket dials the REAL filesystem path sshd
// created for the remote forward, writes msg, and reads back the echoed
// bytes — proving the D-24 pattern's channel genuinely carries traffic
// through the ssh tunnel to the controller's local target and back.
func roundTripThroughRemoteSocket(t *testing.T, remoteSock string) string {
	t.Helper()
	var conn net.Conn
	var err error
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.Dial("unix", remoteSock)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial remote forwarded socket: %v", err)
	}
	defer func() { _ = conn.Close() }()
	msg := "hello-from-node"
	if _, err := conn.Write([]byte(msg)); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	return string(buf)
}

func currentUser(t *testing.T) string {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatalf("current user: %v", err)
	}
	return u.Username
}

// TestTunnelRealSSHD drives Tunnel.run (the real production entry point:
// NewSSHDialer's real ssh.Dial + host-key handshake) against a REAL
// spawned sshd end to end: host-key pin on first contact, a forwarded
// remote unix socket accepting a connection, and bytes round-tripped
// through the tunnel to a local echo target (D-24).
func TestTunnelRealSSHD(t *testing.T) {
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

	port, fp := spawnLoopbackSSHD(t, ident.PubKey)
	target := Target{NodeID: "real-node", User: currentUser(t), Addr: "127.0.0.1:" + strconv.Itoa(port)}
	dialer := NewSSHDialer(ks, ident.NodeID, ident.PubKey, 5*time.Second)

	kh := NewKnownHosts(newMemKnownHostsBackend())
	// First-contact bootstrap: mirrors enroll.go's operator
	// --host-key-fingerprint out-of-band confirmation (S-36.T1).
	if err := kh.Pin(target.knownHostsKey(), fp, false); err != nil {
		t.Fatalf("seed pin: %v", err)
	}
	remoteSock := filepath.Join(t.TempDir(), "remote.sock")

	echoSrv := newLocalEchoServer(t)
	defer echoSrv.Close()

	tun := newTunnel(TunnelConfig{
		Target: target, Dialer: dialer, KnownHosts: kh, Sleeper: &fakeSleeper{},
		RemoteSocketPath: remoteSock,
		LocalDial:        func(context.Context) (Conn, error) { return net.Dial("tcp", echoSrv.Addr()) },
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

	if got := roundTripThroughRemoteSocket(t, remoteSock); got != "hello-from-node" {
		t.Fatalf("round trip through the real ssh-forwarded socket = %q, want echo of the sent bytes", got)
	}
}

// TestTunnelRealSSHD_ChangedHostKeyRefused proves the SAME terminal
// host-key-changed refusal (unit-tested in reconnect_test.go with a fake
// Dialer) also holds against a REAL sshd handshake: pin one fingerprint,
// then dial a DIFFERENT real sshd (a different host key) and confirm the
// connection is refused before any channel exists.
func TestTunnelRealSSHD_ChangedHostKeyRefused(t *testing.T) {
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
	port, _ := spawnLoopbackSSHD(t, ident.PubKey)
	target := Target{NodeID: "real-node", User: currentUser(t), Addr: "127.0.0.1:" + strconv.Itoa(port)}
	dialer := NewSSHDialer(ks, ident.NodeID, ident.PubKey, 5*time.Second)

	kh := NewKnownHosts(newMemKnownHostsBackend())
	// Pin a fingerprint that does NOT match the real sshd's actual host
	// key, simulating a previously-pinned host whose key has since
	// changed.
	if err := kh.Pin(target.knownHostsKey(), "0000000000000000000000000000000000000000000000000000000000000000", false); err != nil {
		t.Fatalf("seed pin: %v", err)
	}

	tun := newTunnel(TunnelConfig{Target: target, Dialer: dialer, KnownHosts: kh, Sleeper: &fakeSleeper{}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tun.run(ctx)

	if got, want := tun.State(), TunnelDown; got != want {
		t.Fatalf("state = %v, want %v (terminal refusal against real sshd)", got, want)
	}
}
