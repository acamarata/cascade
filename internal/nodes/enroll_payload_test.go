package nodes

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"golang.org/x/crypto/ssh"
)

func enrollIdentity(t *testing.T) (Identity, ed25519.PrivateKey) {
	t.Helper()
	id, priv, err := GenerateIdentity(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return id, priv
}

func enrollError(t *testing.T, err error, kind cascade.Kind, message string) {
	t.Helper()
	if got, ok := cascade.KindOf(err); !ok || got != kind {
		t.Fatalf("error kind = %v, %v; want %v: %v", got, ok, kind, err)
	}
	if want := cascade.New(kind, message).Error(); err.Error() != want {
		t.Fatalf("error = %q; want %q", err, want)
	}
}

func signedEnrollFixture(t *testing.T) (NodeEnrollPayload, Transcript, ed25519.PrivateKey, ed25519.PrivateKey) {
	t.Helper()
	ks, err := NewNodeKeystoreForTest(t)
	if err != nil {
		t.Fatal(err)
	}
	self, priv := enrollIdentity(t)
	controller, ctrlPriv := enrollIdentity(t)
	if err := ks.Store(context.Background(), self.NodeID, priv); err != nil {
		t.Fatal(err)
	}
	fp := HostKeyFingerprint(self.PubKey)
	p, err := BuildNodeEnrollPayload(context.Background(), ks, self, controller.PubKeyB64(), fp, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sig, err := base64.StdEncoding.DecodeString(p.NodeSignatureB64)
	if err != nil {
		t.Fatal(err)
	}
	tr := Transcript{NodeID: self.NodeID, NodePubKeyB64: self.PubKeyB64(),
		ControllerPubKeyB64: controller.PubKeyB64(), HostKeyFingerprint: fp, NodeSignature: sig}
	return p, tr, priv, ctrlPriv
}

func TestEnrollPayloadSigIsSignTranscript(t *testing.T) {
	p, tr, priv, ctrlPriv := signedEnrollFixture(t)
	if p.NodeID != tr.NodeID || p.NodePubKeyB64 != tr.NodePubKeyB64 ||
		p.ControllerPubKeyB64 != tr.ControllerPubKeyB64 || p.HostKeyFingerprint != tr.HostKeyFingerprint || !filepath.IsAbs(p.SocketDir) {
		t.Fatalf("payload fields do not match transcript: %+v", p)
	}
	if !bytes.Equal(tr.NodeSignature, SignTranscript(tr, priv)) {
		t.Fatal("payload signature differs from SignTranscript")
	}
	tr.ControllerSignature = SignTranscript(tr, ctrlPriv)
	if err := VerifyTranscript(tr, tr.HostKeyFingerprint); err != nil {
		t.Fatal(err)
	}
}

func TestEnrollPayloadBindsCtrlKey(t *testing.T) {
	_, tr, _, _ := signedEnrollFixture(t)
	other, otherPriv := enrollIdentity(t)
	tr.ControllerPubKeyB64 = other.PubKeyB64()
	tr.ControllerSignature = SignTranscript(tr, otherPriv)
	enrollError(t, VerifyTranscript(tr, tr.HostKeyFingerprint), cascade.KindIntegrity,
		"nodes: enrollment transcript node signature failed verification")
}

func TestTranscriptDomainSeparated(t *testing.T) {
	_, tr, priv, ctrlPriv := signedEnrollFixture(t)
	const prefix = "cascade.node.enroll.v1\x1f"
	want := prefix + strings.Join([]string{tr.NodeID, tr.NodePubKeyB64, tr.ControllerPubKeyB64, tr.HostKeyFingerprint}, "\x1f")
	if TranscriptDomain != "cascade.node.enroll.v1" || string(tr.SigningPayload()) != want {
		t.Fatal("transcript is not domain separated with the specified encoding")
	}
	tr.NodeSignature = ed25519.Sign(priv, []byte(strings.TrimPrefix(want, prefix)))
	tr.ControllerSignature = SignTranscript(tr, ctrlPriv)
	enrollError(t, VerifyTranscript(tr, tr.HostKeyFingerprint), cascade.KindIntegrity,
		"nodes: enrollment transcript node signature failed verification")
}

func writeEnrollHostKey(t *testing.T, pub any) (string, ssh.PublicKey) {
	t.Helper()
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "host.pub")
	if err := os.WriteFile(path, ssh.MarshalAuthorizedKey(key), 0600); err != nil {
		t.Fatal(err)
	}
	return path, key
}

func TestHostKeyFingerprintFromFile(t *testing.T) {
	self, _ := enrollIdentity(t)
	path, key := writeEnrollHostKey(t, self.PubKey)
	got, err := HostKeyFingerprintFromFile(path)
	if err != nil || got != HostKeyFingerprint(key.Marshal()) {
		t.Fatalf("fingerprint = %q, %v", got, err)
	}
	ecdsaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherPath, _ := writeEnrollHostKey(t, &ecdsaKey.PublicKey)
	_, err = HostKeyFingerprintFromFile(otherPath)
	enrollError(t, err, cascade.KindInvalidInput, "nodes: host key "+otherPath+" is not ssh-ed25519")
	missing := filepath.Join(t.TempDir(), "missing.pub")
	_, err = HostKeyFingerprintFromFile(missing)
	enrollError(t, err, cascade.KindNotFound, "nodes: host key "+missing+" not found")
	for _, tc := range []struct{ data, message string }{
		{"invalid", "nodes: host key " + path + " is not a valid authorized key"},
		{strings.Repeat("a", 16*1024+1), "nodes: host key " + path + " exceeds 16384 bytes"},
	} {
		if err := os.WriteFile(path, []byte(tc.data), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := HostKeyFingerprintFromFile(path)
		enrollError(t, err, cascade.KindInvalidInput, tc.message)
	}
}

func TestEnrollPayloadRefusals(t *testing.T) {
	ks, err := NewNodeKeystoreForTest(t)
	if err != nil {
		t.Fatal(err)
	}
	self, _ := enrollIdentity(t)
	controller, _ := enrollIdentity(t)
	fp, dir := HostKeyFingerprint(self.PubKey), t.TempDir()
	cases := []struct{ name, controller, fp, dir, message string }{
		{"controller", "", fp, dir, "nodes: public key is required"},
		{"short key", base64.StdEncoding.EncodeToString([]byte("short")), fp, dir, "nodes: public key has length 5, want 32"},
		{"fingerprint", controller.PubKeyB64(), fp[:63], dir, "nodes: host key fingerprint must be 64 lowercase hex characters"},
		{"uppercase", controller.PubKeyB64(), strings.Repeat("A", 64), dir, "nodes: host key fingerprint must be 64 lowercase hex characters"},
		{"nonhex", controller.PubKeyB64(), strings.Repeat("g", 64), dir, "nodes: host key fingerprint must be 64 lowercase hex characters"},
		{"empty directory", controller.PubKeyB64(), fp, "", "nodes: enroll payload socket_dir is required"},
		{"relative directory", controller.PubKeyB64(), fp, "relative", "nodes: enroll payload socket_dir is not an absolute path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := BuildNodeEnrollPayload(context.Background(), ks, self, tc.controller, tc.fp, tc.dir)
			enrollError(t, err, cascade.KindInvalidInput, tc.message)
			if p != (NodeEnrollPayload{}) {
				t.Fatal("refusal returned a partial payload")
			}
		})
	}
}

func TestSshDialerClientConfigPinsEd25519(t *testing.T) {
	_, priv := enrollIdentity(t)
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	d := &sshDialer{handshakeTimeout: 7 * time.Second}
	called := false
	verify := func(fp string) error {
		called = true
		if fp != HostKeyFingerprint(signer.PublicKey().Marshal()) {
			t.Fatal("callback fingerprint differs from host key")
		}
		return cascade.New(cascade.KindPermissionDenied, "test host key refusal")
	}
	cfg := d.clientConfig("node-user", signer, verify)
	if !reflect.DeepEqual(cfg.HostKeyAlgorithms, []string{ssh.KeyAlgoED25519}) || cfg.Timeout != d.handshakeTimeout {
		t.Fatalf("algorithms = %v, timeout = %v", cfg.HostKeyAlgorithms, cfg.Timeout)
	}
	if cfg.User != "node-user" || len(cfg.Auth) != 1 || cfg.HostKeyCallback == nil {
		t.Fatal("client configuration lost authentication or host verification")
	}
	err = cfg.HostKeyCallback("host", nil, signer.PublicKey())
	enrollError(t, err, cascade.KindPermissionDenied, "test host key refusal")
	if !called {
		t.Fatal("host key verifier was not called")
	}
}
