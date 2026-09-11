// Purpose: the controller<->node ssh tunnel transport (R-21.220 host-key
//
//	binding): the state/dial abstractions reconnect.go's state machine
//	drives, plus the production ssh dialer over golang.org/x/crypto/ssh.
//
// Inputs: a Target (the ssh reachability info for one enrolled node), the
//
//	collaborators a Session needs: KnownHosts (R-21.220 pinning),
//	NodeKeystore (the controller's own signing identity, R-21.220
//	custody).
//
// Outputs: a maintained ssh session forwarding the node's RPC/stream
//
//	channel back to this process (the D-24 ssh-forwarded-socket pattern),
//	or a typed fail-closed refusal.
//
// Constraints: TARGET, NOT DEVICERECORD (CONTRADICTION — full quote in the
//
//	ticket journal). The ticket's HOW section reads "tunnel establishment:
//	from the enrolled node's device record (S-36.T1) over ssh to
//	<user@host>," implying DeviceRecord carries the ssh address. It does
//	not: records.go's DeviceRecord has no Host/User field, and records.go
//	is S-36.T1's file, out of this ticket's files_scope. This is the SAME
//	gap liveness.go's Liveness-derived-not-persisted note and
//	heartbeat_sign.go's DeriveEnrollmentID note already document; this
//	file follows their identical precedent — Target is caller-supplied
//	rather than read off a field that does not exist.
//
//	CONN/LISTENER, NOT net.Conn/net.Listener. An untagged _test.go file
//	may not import "net"/"net/http" (Art.7.2). Session/Listener are
//	declared over this file's own Conn alias (io.ReadWriteCloser) so unit
//	tests fake a Dialer/Session/Listener with io.Pipe, never importing
//	"net". The production ssh adapter (this file) freely imports "net"
//	and "golang.org/x/crypto/ssh" — internal/nodes is already
//	egress-allowlisted for "net" ("node dispatch transport").
//
// SPORT: internal/nodes TunnelState/ADDED, Target/ADDED, Dialer/ADDED
//
//	(P1-E17-W4-S36-T3).

package nodes

import (
	"context"
	"crypto/ed25519"
	"io"
	"net"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TunnelState is the reconnect state machine's three states (this
// ticket's contract: "down -> reconnecting -> up").
type TunnelState int

const (
	// TunnelDown reports no session and no active retry — either never
	// started, a terminal refusal (host-key changed/unknown) stopped it,
	// or Manager.Stop was called.
	TunnelDown TunnelState = iota
	// TunnelReconnecting reports the loop dialing or waiting on backoff.
	TunnelReconnecting
	// TunnelUp reports a session established and forwarding.
	TunnelUp
)

// String renders s for logs/events/`node status`.
func (s TunnelState) String() string {
	switch s {
	case TunnelDown:
		return "down"
	case TunnelReconnecting:
		return "reconnecting"
	case TunnelUp:
		return "up"
	default:
		return "unknown"
	}
}

// Target is the ssh reachability info for one enrolled node's tunnel. See
// this file's package doc CONTRADICTIONS note for why it is caller-
// supplied rather than read off a DeviceRecord field.
type Target struct {
	NodeID string
	User   string
	// Addr is host:port, the address net.Dialer dials.
	Addr string
}

// knownHostsKey is the KnownHosts pinning key for t, matching enroll.go's
// EnrollPayload.Host convention ("user@host", enroll_test.go's
// "worker@host1").
func (t Target) knownHostsKey() string {
	return t.User + "@" + t.Addr
}

// Conn is a bidirectional byte stream: io.ReadWriteCloser exactly, kept
// as this package's own alias (rather than net.Conn) so unit tests never
// need to import "net" — see package doc.
type Conn = io.ReadWriteCloser

// Listener accepts connections forwarded from the node side of the
// tunnel (the D-24 pattern's remote-listen half: ssh -R streamlocal
// semantics — a socket on the NODE's filesystem, whose connections
// arrive here in the controller process).
type Listener interface {
	Accept() (Conn, error)
	Close() error
}

// Session is one established ssh connection to a node.
type Session interface {
	// ListenUnix opens a REMOTE listener on the node's filesystem at
	// remoteSocketPath. Idempotent per remoteSocketPath is the caller's
	// (Manager's) responsibility, not this interface's.
	ListenUnix(remoteSocketPath string) (Listener, error)
	Close() error
	// Done reports (by closing) when the underlying connection ends, for
	// any reason: remote close, network drop, or a local Close() call.
	Done() <-chan struct{}
}

// HostKeyVerifier is called with the SHA-256 fingerprint (HostKeyFingerprint's
// format) of the key the remote host actually presented mid-handshake.
// Production wires this to KnownHosts.Verify; its error is returned
// UNCHANGED to the underlying ssh handshake, which refuses the connection
// before any channel exists on ANY non-nil return — R-21.220's fail-closed
// check is therefore atomic with connection establishment.
type HostKeyVerifier func(fingerprint string) error

// Dialer establishes a Session to target, verifying the presented host
// key via verify before the handshake completes.
type Dialer interface {
	Dial(ctx context.Context, target Target, verify HostKeyVerifier) (Session, error)
}

// keystoreSigner adapts NodeKeystore.Sign to ssh.Signer, so ssh
// authenticates as this controller's already-enrolled identity without a
// second key ever being generated or a private key ever leaving custody
// (R-21.220 — Sign is the ONLY path to the private key, unchanged from
// keystore.go's own contract).
type keystoreSigner struct {
	ctx      context.Context
	keystore *NodeKeystore
	nodeID   string
	pub      ed25519.PublicKey
}

func (s *keystoreSigner) PublicKey() ssh.PublicKey {
	k, err := ssh.NewPublicKey(s.pub)
	if err != nil {
		// s.pub is always a valid-length ed25519 key by construction
		// (NewTunnel validates it); this cannot fail in practice.
		panic("nodes: ssh public key conversion failed: " + err.Error())
	}
	return k
}

func (s *keystoreSigner) Sign(_ io.Reader, data []byte) (*ssh.Signature, error) {
	sig, err := s.keystore.Sign(s.ctx, s.nodeID, data)
	if err != nil {
		return nil, err
	}
	return &ssh.Signature{Format: ssh.KeyAlgoED25519, Blob: sig}, nil
}

// sshDialer is the production Dialer, over a real TCP dial and a real ssh
// handshake.
type sshDialer struct {
	keystore         *NodeKeystore
	controllerNodeID string
	controllerPub    ed25519.PublicKey
	handshakeTimeout time.Duration
}

// NewSSHDialer returns the production Dialer, authenticating as
// controllerNodeID via keystore's custody-held private key.
func NewSSHDialer(keystore *NodeKeystore, controllerNodeID string, controllerPub ed25519.PublicKey, handshakeTimeout time.Duration) Dialer {
	if handshakeTimeout <= 0 {
		handshakeTimeout = 15 * time.Second
	}
	return &sshDialer{keystore: keystore, controllerNodeID: controllerNodeID, controllerPub: controllerPub, handshakeTimeout: handshakeTimeout}
}

// Dial implements Dialer over a real net.Dial + ssh.NewClientConn. Any
// error verify returns (ErrHostKeyUnknown/ErrHostKeyChanged, typed
// KindPermissionDenied) propagates verbatim through the ssh handshake —
// TestTunnelHostKeyChangedRefused/TestTunnelUnknownHostKeyRefused assert
// this end to end.
func (d *sshDialer) Dial(ctx context.Context, target Target, verify HostKeyVerifier) (Session, error) {
	var netDialer net.Dialer
	conn, err := netDialer.DialContext(ctx, "tcp", target.Addr)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "nodes: ssh tunnel tcp dial failed")
	}
	cfg := &ssh.ClientConfig{
		User:            target.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(&keystoreSigner{ctx: ctx, keystore: d.keystore, nodeID: d.controllerNodeID, pub: d.controllerPub})},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error { return verify(HostKeyFingerprint(key.Marshal())) },
		Timeout:         d.handshakeTimeout,
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, target.Addr, cfg)
	if err != nil {
		_ = conn.Close()
		if _, ok := cascade.KindOf(err); ok {
			return nil, err // verify's own typed refusal, unwrapped
		}
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "nodes: ssh handshake failed")
	}
	return newSSHSession(ssh.NewClient(sshConn, chans, reqs)), nil
}

// sshSession is the production Session, over a real *ssh.Client.
type sshSession struct {
	client *ssh.Client
	done   chan struct{}
}

func newSSHSession(client *ssh.Client) *sshSession {
	s := &sshSession{client: client, done: make(chan struct{})}
	go func() {
		_ = client.Wait()
		close(s.done)
	}()
	return s
}

func (s *sshSession) ListenUnix(remoteSocketPath string) (Listener, error) {
	l, err := s.client.ListenUnix(remoteSocketPath)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "nodes: ssh remote forward failed")
	}
	return sshListener{l}, nil
}

func (s *sshSession) Close() error          { return s.client.Close() }
func (s *sshSession) Done() <-chan struct{} { return s.done }

// sshListener adapts net.Listener to this package's Listener (Conn, not
// net.Conn, in Accept's return — see package doc).
type sshListener struct{ l net.Listener }

func (s sshListener) Accept() (Conn, error) {
	c, err := s.l.Accept()
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "nodes: ssh forwarded accept failed")
	}
	return c, nil
}

func (s sshListener) Close() error { return s.l.Close() }

// TunnelEventPublisher is the seam tunnel state transitions publish
// through, duck-typed against internal/runtime.EventPublisher's exact
// method signature (mirrors heartbeat.go's Ticker / records.go's Clock
// precedent: this package must not import internal/runtime for a
// one-method interface it does not otherwise need).
type TunnelEventPublisher interface {
	Publish(ctx context.Context, name string, payload map[string]interface{})
}

// DiscardTunnelEvents discards every event; the default when no publisher
// is injected (mirrors internal/runtime.DiscardEventPublisher).
type DiscardTunnelEvents struct{}

// Publish implements TunnelEventPublisher by discarding the event.
func (DiscardTunnelEvents) Publish(context.Context, string, map[string]interface{}) {}

// windowsTunnelHint is the actionable refusal message for the
// controller-side tunnel service on Windows (R-21.226), mirroring
// serve.go's windowsTier2Hint convention exactly.
const windowsTunnelHint = "cascade has no controller-side ssh tunnel service on Windows (tier-2); " +
	"the tunnel service is daemon-class, outside the binary + headless one-shot promise (06-FORGE-SPEC §2)"

// RefuseTunnelServiceOnGOOS reports the typed tier-2 refusal for
// goos == "windows", nil otherwise. Mirrors serve.go's RefuseOnGOOS
// exactly: a pure function, unit-tested against the literal string
// "windows" on every platform; tunnel_windows_test.go additionally
// asserts it natively on a real windows/amd64 build (R-21.226).
func RefuseTunnelServiceOnGOOS(goos string) error {
	if goos == "windows" {
		return cascade.New(cascade.KindUnsupported, "node tunnel: "+windowsTunnelHint)
	}
	return nil
}

// Manager lives in reconnect.go (the 300-line file cap left no room here).
