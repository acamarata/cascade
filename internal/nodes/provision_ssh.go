// Purpose: the production ExecDialer/ExecSession over a real ssh
//   handshake (split out of provision.go purely for the 300-line cap;
//   same ticket, same files_scope-add file "provision.go" documented as
//   needing this sibling — mirrors node_admit.go/node_keys.go's
//   identical split precedent from S-36.T4's own journal).
// Inputs: an ssh.Signer (typically NodeKeystore-backed) and a Target.
// Outputs: an ExecSession that runs commands and writes files over a
//   real ssh connection.
// Constraints: authenticates via PublicKeys(signer) exactly like
//   tunnel.go's sshDialer — the private key never leaves wherever the
//   caller's signer holds it (custody, per keystore.go).
// SPORT: internal/nodes NewSSHExecDialer/ADDED (P1-E17-W4-S36-T5).

package nodes

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"io"
	"net"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/acamarata/cascade/pkg/cascade"
)

// sshExecDialer is the production ExecDialer, over a real ssh handshake.
// Independent of tunnel.go's sshDialer (which builds a tunnel.Session,
// not an ExecSession) but authenticates identically: PublicKeys auth via
// an injected ssh.Signer, so the private key never leaves wherever the
// caller's signer holds it.
type sshExecDialer struct {
	signer           ssh.Signer
	handshakeTimeout time.Duration
}

// NewSSHExecDialer returns the production ExecDialer, authenticating via
// signer (typically a NodeKeystore-backed signer, mirroring tunnel.go's
// keystoreSigner — constructed by the caller so this package does not
// duplicate that adapter a second time in this file).
func NewSSHExecDialer(signer ssh.Signer, handshakeTimeout time.Duration) ExecDialer {
	if handshakeTimeout <= 0 {
		handshakeTimeout = 15 * time.Second
	}
	return &sshExecDialer{signer: signer, handshakeTimeout: handshakeTimeout}
}

func (d *sshExecDialer) Dial(ctx context.Context, target Target, verify HostKeyVerifier) (ExecSession, error) {
	var netDialer net.Dialer
	conn, err := netDialer.DialContext(ctx, "tcp", target.Addr)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "nodes: provision ssh tcp dial failed")
	}
	cfg := &ssh.ClientConfig{
		User:            target.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(d.signer)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error { return verify(HostKeyFingerprint(key.Marshal())) },
		Timeout:         d.handshakeTimeout,
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, target.Addr, cfg)
	if err != nil {
		_ = conn.Close()
		if _, ok := cascade.KindOf(err); ok {
			return nil, err
		}
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "nodes: provision ssh handshake failed")
	}
	return &sshExecSession{client: ssh.NewClient(sshConn, chans, reqs)}, nil
}

// sshExecSession is the production ExecSession over a real *ssh.Client.
type sshExecSession struct {
	client *ssh.Client
}

func (s *sshExecSession) Output(_ context.Context, cmd string) ([]byte, error) {
	sess, err := s.client.NewSession()
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "nodes: provision: open ssh session")
	}
	defer func() { _ = sess.Close() }()
	out, err := sess.CombinedOutput(cmd)
	if err != nil {
		return out, cascade.Wrap(cascade.KindUnavailable, err, "nodes: provision: remote command failed")
	}
	return out, nil
}

func (s *sshExecSession) WriteFile(_ context.Context, remotePath string, data []byte) error {
	sess, err := s.client.NewSession()
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "nodes: provision: open ssh session")
	}
	defer func() { _ = sess.Close() }()
	stdin, err := sess.StdinPipe()
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "nodes: provision: stdin pipe")
	}
	if err := sess.Start("cat > " + shellQuote(remotePath)); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "nodes: provision: start remote write")
	}
	if _, err := io.Copy(stdin, bytes.NewReader(data)); err != nil {
		_ = stdin.Close()
		return cascade.Wrap(cascade.KindUnavailable, err, "nodes: provision: write transfer")
	}
	if err := stdin.Close(); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "nodes: provision: close transfer")
	}
	if err := sess.Wait(); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "nodes: provision: remote write failed")
	}
	return nil
}

func (s *sshExecSession) Close() error { return s.client.Close() }

// keystoreExecSigner adapts NodeKeystore.Sign to ssh.Signer for
// NewSSHExecDialer callers, matching tunnel.go's keystoreSigner
// precedent exactly (duplicated rather than exported across files: both
// are tiny, single-call-site adapters — see node_keys.go's identical
// small-duplication precedent).
type keystoreExecSigner struct {
	ctx      context.Context
	keystore *NodeKeystore
	nodeID   string
	pub      ed25519.PublicKey
}

// NewKeystoreExecSigner builds the production ssh.Signer for
// NewSSHExecDialer, signing via keystore's custody-held private key.
func NewKeystoreExecSigner(ctx context.Context, keystore *NodeKeystore, nodeID string, pub ed25519.PublicKey) ssh.Signer {
	return &keystoreExecSigner{ctx: ctx, keystore: keystore, nodeID: nodeID, pub: pub}
}

func (s *keystoreExecSigner) PublicKey() ssh.PublicKey {
	k, err := ssh.NewPublicKey(s.pub)
	if err != nil {
		panic("nodes: ssh public key conversion failed: " + err.Error())
	}
	return k
}

func (s *keystoreExecSigner) Sign(_ io.Reader, data []byte) (*ssh.Signature, error) {
	sig, err := s.keystore.Sign(s.ctx, s.nodeID, data)
	if err != nil {
		return nil, err
	}
	return &ssh.Signature{Format: ssh.KeyAlgoED25519, Blob: sig}, nil
}
