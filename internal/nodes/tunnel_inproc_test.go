//go:build integration

package nodes

// Purpose: drives the production ssh dialer and session against an
// in-process x/crypto/ssh server on a loopback listener (no sshd binary),
// covering Dial, DialUnix, ListenUnix and the signer adapters.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/acamarata/cascade/pkg/cascade"
)

type inprocServer struct {
	addr string
	fp   string
}

// startInprocSSH serves one-at-a-time connections that accept clientPub.
// direct-streamlocal channels echo their bytes; a
// streamlocal-forward for a path containing "deny" is rejected.
func startInprocSSH(t *testing.T, clientPub ed25519.PublicKey) inprocServer {
	t.Helper()
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := ssh.NewPublicKey(clientPub)
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if string(k.Marshal()) != string(want.Marshal()) {
			return nil, io.ErrUnexpectedEOF
		}
		return &ssh.Permissions{}, nil
	}}
	cfg.AddHostKey(hostSigner)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go serveInproc(c, cfg)
		}
	}()
	return inprocServer{addr: l.Addr().String(), fp: HostKeyFingerprint(hostSigner.PublicKey().Marshal())}
}

func serveInproc(c net.Conn, cfg *ssh.ServerConfig) {
	conn, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	go func() {
		for r := range reqs {
			ok := r.Type == "streamlocal-forward@openssh.com" && !strings.Contains(string(r.Payload), "deny")
			if r.WantReply {
				_ = r.Reply(ok, nil)
			}
		}
	}()
	for nc := range chans {
		if nc.ChannelType() != "direct-streamlocal@openssh.com" {
			_ = nc.Reject(ssh.UnknownChannelType, "no")
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go ssh.DiscardRequests(creqs)
		go func() { _, _ = io.Copy(ch, ch); _ = ch.Close() }()
	}
}

func inprocDial(t *testing.T) (Session, inprocServer) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	srv := startInprocSSH(t, pub)
	d := NewSSHDialerFromSigner(payloadSigner{priv}, "ctl-node", 0)
	sess, err := d.Dial(context.Background(), Target{NodeID: "n", User: "u", Addr: srv.addr},
		func(fp string) error {
			if fp != srv.fp {
				return cascade.New(cascade.KindPermissionDenied, "fingerprint mismatch")
			}
			return nil
		})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess, srv
}

func TestInprocSessionDialUnixEchoes(t *testing.T) {
	sess, _ := inprocDial(t)
	c, err := sess.DialUnix("/run/node.sock")
	if err != nil {
		t.Fatalf("DialUnix: %v", err)
	}
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo = %q, %v; want ping", buf, err)
	}
	_ = c.Close()
	if err := sess.Close(); err != nil {
		t.Logf("Close: %v", err)
	}
	select {
	case <-sess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done never closed after Close")
	}
	if _, err := sess.DialUnix("/run/node.sock"); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("DialUnix on a closed session = %v, want unavailable", err)
	}
}

func TestInprocSessionListenUnix(t *testing.T) {
	sess, _ := inprocDial(t)
	if _, err := sess.ListenUnix("/run/deny.sock"); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("rejected forward = %v, want unavailable", err)
	}
	l, err := sess.ListenUnix("/run/ok.sock")
	if err != nil {
		t.Fatalf("ListenUnix: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Logf("listener close: %v", err)
	}
	if _, err := l.Accept(); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Accept after Close = %v, want unavailable", err)
	}
}

func TestInprocDialRefusals(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	srv := startInprocSSH(t, pub)
	ctx, tgt := context.Background(), Target{User: "u", Addr: srv.addr}
	verifyOK := func(string) error { return nil }

	_, err := NewSSHDialerFromSigner(nil, "ctl", time.Second).Dial(ctx, tgt, verifyOK)
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("nil signer = %v, want invalid-input", err)
	}
	_, err = NewSSHDialerFromSigner(payloadSigner{priv}, "ctl", time.Second).Dial(ctx, tgt,
		func(string) error { return cascade.New(cascade.KindPermissionDenied, "host key changed") })
	if !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("verify refusal = %v, want permission-denied passed through", err)
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	_, err = NewSSHDialerFromSigner(payloadSigner{other}, "ctl", time.Second).Dial(ctx, tgt, verifyOK)
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("unauthorised key = %v, want unavailable handshake failure", err)
	}
	dead, _ := net.Listen("tcp", "127.0.0.1:0")
	deadAddr := dead.Addr().String()
	_ = dead.Close()
	_, err = NewSSHDialerFromSigner(payloadSigner{priv}, "ctl", time.Second).Dial(ctx, Target{Addr: deadAddr}, verifyOK)
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("closed port = %v, want unavailable", err)
	}
}

func TestSSHDialerSignerAdapters(t *testing.T) {
	short := shortKeySigner{}
	_, err := NewSSHDialerFromSigner(short, "ctl", 0).Dial(context.Background(), Target{Addr: "127.0.0.1:1"}, nil)
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("short public key = %v, want invalid-input", err)
	}
	if got := defaultHandshakeTimeout(0); got != 15*time.Second {
		t.Errorf("default handshake timeout = %v, want 15s", got)
	}
	if got := defaultHandshakeTimeout(3 * time.Second); got != 3*time.Second {
		t.Errorf("explicit handshake timeout = %v, want 3s", got)
	}
}

type shortKeySigner struct{ payloadSigner }

func (shortKeySigner) PublicKey() ed25519.PublicKey { return ed25519.PublicKey{1, 2, 3} }
