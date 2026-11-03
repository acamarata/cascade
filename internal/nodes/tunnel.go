// Purpose: the controller<->node ssh tunnel transport (R-21.220 host-key
//	binding): the state/dial abstractions reconnect.go drives, plus the
//	production ssh dialer over golang.org/x/crypto/ssh.
// Inputs: a Target (ssh reachability for one enrolled node), KnownHosts
//	(R-21.220 pinning) and the controller's signing identity: NodeKeystore
//	(NewSSHDialer) or the installation envelope.Signer
//	(NewSSHDialerFromSigner); the private key never leaves custody.
// Outputs: an ssh session forwarding the node's RPC/stream channel (the
//	D-24 ssh-forwarded-socket pattern) and direct-streamlocal dials over
//	the SAME authenticated client, or a typed fail-closed refusal.
// Constraints: Target is caller-supplied, not read off DeviceRecord
//	(records.go has no Host/User field; the liveness.go precedent).
//	Session/Listener speak this file's Conn alias (io.ReadWriteCloser),
//	not net.Conn, so untagged unit tests never import "net" (Art.7.2).
//	No agent, no X11 forwarding, no second connection, no plain fallback.
// SPORT: internal/nodes TunnelState/ADDED, Target/ADDED, Dialer/ADDED
//	(P1-E17-W4-S36-T3); Session.DialUnix/ADDED,
//	NewSSHDialerFromSigner/ADDED (P1-NODE-18).

package nodes

import (
	"context"
	"crypto/ed25519"
	"io"
	"net"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/envelope"
)

// TunnelState is the reconnect state machine's state: down -> reconnecting -> up.
type TunnelState int

const (
	// TunnelDown reports no session and no retry: never started, a terminal
	// host-key refusal (changed/unknown) or Manager.Stop.
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

// Target is the caller-supplied ssh reachability for one node's tunnel.
type Target struct {
	NodeID string
	User   string
	// Addr is host:port, the address net.Dialer dials.
	Addr string
}

// knownHostsKey is t's KnownHosts pinning key (EnrollPayload.Host's "user@host").
func (t Target) knownHostsKey() string {
	return t.User + "@" + t.Addr
}

// Conn is a bidirectional byte stream (io.ReadWriteCloser, never net.Conn).
type Conn = io.ReadWriteCloser

// Listener accepts connections forwarded from a socket on the NODE's
// filesystem (ssh -R streamlocal, the D-24 remote-listen half).
type Listener interface {
	Accept() (Conn, error)
	Close() error
}

// Session is one established ssh connection to a node.
type Session interface {
	// ListenUnix opens a REMOTE listener at remoteSocketPath on the node.
	ListenUnix(remoteSocketPath string) (Listener, error)
	// DialUnix opens direct-streamlocal to remotePath on the node over the
	// SAME authenticated client, never a second connection.
	DialUnix(remotePath string) (Conn, error)
	Close() error
	// Done closes when the connection ends for any reason.
	Done() <-chan struct{}
}

// HostKeyVerifier gets the HostKeyFingerprint of the key the host presented
// mid-handshake (production: KnownHosts.Verify). Any non-nil return refuses
// the connection before a channel exists (R-21.220, fail closed).
type HostKeyVerifier func(fingerprint string) error

// Dialer establishes a Session to target, verifying the presented host
// key via verify before the handshake completes.
type Dialer interface {
	Dial(ctx context.Context, target Target, verify HostKeyVerifier) (Session, error)
}

// keystoreSigner adapts NodeKeystore.Sign to ssh.Signer; Sign is the only
// path to the custody-held private key (R-21.220).
type keystoreSigner struct {
	ctx      context.Context
	keystore *NodeKeystore
	nodeID   string
	pub      ed25519.PublicKey
}

func (s *keystoreSigner) PublicKey() ssh.PublicKey {
	k, err := ssh.NewPublicKey(s.pub)
	if err != nil {
		// s.pub is a valid-length ed25519 key by construction.
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

// envelopeSSHSigner adapts the installation envelope.Signer to ssh-ed25519
// ssh.Signer, the only such adapter in the non-test tree.
type envelopeSSHSigner struct {
	ctx    context.Context
	signer envelope.Signer
	pub    ssh.PublicKey
}

func (s *envelopeSSHSigner) PublicKey() ssh.PublicKey { return s.pub }

func (s *envelopeSSHSigner) Sign(_ io.Reader, data []byte) (*ssh.Signature, error) {
	sig, err := s.signer.Sign(s.ctx, data)
	if err != nil {
		return nil, err
	}
	return &ssh.Signature{Format: ssh.KeyAlgoED25519, Blob: sig}, nil
}

// sshDialer is the production Dialer; authSigner yields one Dial's identity.
type sshDialer struct {
	controllerNodeID string
	authSigner       func(ctx context.Context) (ssh.Signer, error)
	handshakeTimeout time.Duration
}

func defaultHandshakeTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return 15 * time.Second
	}
	return d
}

// NewSSHDialer returns the production Dialer, authenticating as
// controllerNodeID via keystore's custody-held private key.
func NewSSHDialer(keystore *NodeKeystore, controllerNodeID string, controllerPub ed25519.PublicKey, handshakeTimeout time.Duration) Dialer {
	auth := func(ctx context.Context) (ssh.Signer, error) {
		return &keystoreSigner{ctx: ctx, keystore: keystore, nodeID: controllerNodeID, pub: controllerPub}, nil
	}
	return &sshDialer{controllerNodeID: controllerNodeID, authSigner: auth, handshakeTimeout: defaultHandshakeTimeout(handshakeTimeout)}
}

// NewSSHDialerFromSigner returns the production Dialer authenticating with
// the controller installation identity s (contract:installation-identity):
// the userauth key is exactly s.PublicKey(); a nil s or a bad key refuses
// every Dial KindInvalidInput.
func NewSSHDialerFromSigner(s envelope.Signer, controllerNodeID string, handshakeTimeout time.Duration) Dialer {
	auth := func(ctx context.Context) (ssh.Signer, error) {
		if s == nil {
			return nil, cascade.New(cascade.KindInvalidInput, "nodes: ssh dialer has no installation signer")
		}
		pub := s.PublicKey()
		if len(pub) != ed25519.PublicKeySize {
			return nil, cascade.New(cascade.KindInvalidInput, "nodes: installation public key is not an ed25519 key")
		}
		k, err := ssh.NewPublicKey(pub)
		if err != nil {
			return nil, cascade.Wrap(cascade.KindInvalidInput, err, "nodes: installation public key rejected")
		}
		return &envelopeSSHSigner{ctx: ctx, signer: s, pub: k}, nil
	}
	return &sshDialer{controllerNodeID: controllerNodeID, authSigner: auth, handshakeTimeout: defaultHandshakeTimeout(handshakeTimeout)}
}

// Dial implements Dialer over net.Dial + ssh.NewClientConn. verify's typed
// refusal (KindPermissionDenied) comes back verbatim; x/crypto/ssh re-runs
// it on every re-key, so a key changing on a started channel ends it too.
func (d *sshDialer) Dial(ctx context.Context, target Target, verify HostKeyVerifier) (Session, error) {
	signer, err := d.authSigner(ctx)
	if err != nil {
		return nil, err
	}
	var netDialer net.Dialer
	conn, err := netDialer.DialContext(ctx, "tcp", target.Addr)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "nodes: ssh tunnel tcp dial failed")
	}
	cfg := &ssh.ClientConfig{
		User:            target.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
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

// DialUnix opens direct-streamlocal to remotePath on the
// already-authenticated client; bytes are never read or logged here.
func (s *sshSession) DialUnix(remotePath string) (Conn, error) {
	c, err := s.client.Dial("unix", remotePath)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "nodes: ssh direct-streamlocal dial failed")
	}
	return c, nil
}

func (s *sshSession) Close() error          { return s.client.Close() }
func (s *sshSession) Done() <-chan struct{} { return s.done }

// sshListener adapts net.Listener to this package's Listener.
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
// through, duck-typed against internal/runtime.EventPublisher.
type TunnelEventPublisher interface {
	Publish(ctx context.Context, name string, payload map[string]interface{})
}

// DiscardTunnelEvents discards every event; the default when no publisher
// is injected (mirrors internal/runtime.DiscardEventPublisher).
type DiscardTunnelEvents struct{}

// Publish implements TunnelEventPublisher by discarding the event.
func (DiscardTunnelEvents) Publish(context.Context, string, map[string]interface{}) {}

// windowsTunnelHint is the Windows tier-2 refusal message (R-21.226).
const windowsTunnelHint = "cascade has no controller-side ssh tunnel service on Windows (tier-2); " +
	"the tunnel service is daemon-class, outside the binary + headless one-shot promise (06-FORGE-SPEC §2)"

// RefuseTunnelServiceOnGOOS reports the typed tier-2 refusal for
// goos == "windows", nil otherwise (mirrors serve.go's RefuseOnGOOS).
func RefuseTunnelServiceOnGOOS(goos string) error {
	if goos == "windows" {
		return cascade.New(cascade.KindUnsupported, "node tunnel: "+windowsTunnelHint)
	}
	return nil
}
