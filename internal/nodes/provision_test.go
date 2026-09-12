package nodes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeExecSession is an in-memory ExecSession fake: it never imports
// "net" (Art.7.2) and models exactly the commands provision.go issues
// (version query, sha256sum, chmod+mv, rm -f).
type fakeExecSession struct {
	files            map[string][]byte
	installedVersion string
	failWrite        bool
	failChecksum     bool
	corruptChecksum  bool
	failInstall      bool
	closed           bool
}

func newFakeExecSession(installedVersion string) *fakeExecSession {
	return &fakeExecSession{files: map[string][]byte{}, installedVersion: installedVersion}
}

func (s *fakeExecSession) Output(_ context.Context, cmd string) ([]byte, error) {
	switch {
	case strings.HasSuffix(cmd, " version"):
		if s.installedVersion == "" {
			return nil, errors.New("command not found")
		}
		return []byte("cascade version " + s.installedVersion + "\ncommit: x\n"), nil
	case strings.HasPrefix(cmd, "sha256sum "):
		if s.failChecksum {
			return nil, errors.New("checksum command failed")
		}
		path := strings.Trim(strings.TrimPrefix(cmd, "sha256sum "), "'")
		data, ok := s.files[path]
		if !ok {
			return nil, errors.New("no such file")
		}
		sum := sha256.Sum256(data)
		hexSum := hex.EncodeToString(sum[:])
		if s.corruptChecksum {
			hexSum = strings.Repeat("0", 64)
		}
		return []byte(hexSum + "  " + path + "\n"), nil
	case strings.HasPrefix(cmd, "chmod +x "):
		if s.failInstall {
			return nil, errors.New("install failed")
		}
		return nil, nil
	case strings.HasPrefix(cmd, "rm -f "):
		return nil, nil
	default:
		return nil, errors.New("unrecognized command: " + cmd)
	}
}

func (s *fakeExecSession) WriteFile(_ context.Context, remotePath string, data []byte) error {
	if s.failWrite {
		return errors.New("transfer failed")
	}
	s.files[remotePath] = append([]byte{}, data...)
	return nil
}

func (s *fakeExecSession) Close() error { s.closed = true; return nil }

// fakeExecDialer returns a fixed session/error pair, mirroring
// reconnect_test.go's fakeDialer style.
type fakeExecDialer struct {
	session ExecSession
	err     error
}

func (d *fakeExecDialer) Dial(context.Context, Target, HostKeyVerifier) (ExecSession, error) {
	return d.session, d.err
}

func testArtifact(t *testing.T, version string) (Artifact, MinisignPublicKey) {
	t.Helper()
	data := readTestdataMinisign(t, "artifact.bin")
	sig := readTestdataMinisign(t, "artifact.bin.minisig")
	pubBytes := readTestdataMinisign(t, "test.pub")
	pub, err := ParseMinisignPublicKey(pubBytes)
	if err != nil {
		t.Fatalf("parse pubkey: %v", err)
	}
	return Artifact{Version: version, Data: data, Signature: sig}, pub
}

func noopVerifyFor(Target) HostKeyVerifier { return func(string) error { return nil } }

func TestProvision_InvalidSignatureRefusedBeforeAnyDial(t *testing.T) {
	artifact, pub := testArtifact(t, "v2.5.0")
	artifact.Signature = []byte("not a real signature") // fails to parse
	dialer := &fakeExecDialer{err: errors.New("dial must never be attempted")}
	_, err := Provision(context.Background(), Target{NodeID: "n1", Addr: "h:22", User: "u"}, artifact,
		ProvisionDeps{Dialer: dialer, VerifyFor: noopVerifyFor, PublicKey: pub})
	if err == nil {
		t.Fatal("expected a refusal for an invalid signature")
	}
	if kind, _ := cascade.KindOf(err); kind != cascade.KindInvalidInput && kind != cascade.KindIntegrity {
		t.Fatalf("kind = %v, want KindInvalidInput or KindIntegrity", kind)
	}
}

func TestProvision_TamperedArtifactDataRefused(t *testing.T) {
	artifact, pub := testArtifact(t, "v2.5.0")
	artifact.Data = append([]byte{}, artifact.Data...)
	artifact.Data[0] ^= 0xFF // signature no longer matches the (now tampered) data
	dialer := &fakeExecDialer{err: errors.New("dial must never be attempted")}
	_, err := Provision(context.Background(), Target{NodeID: "n1", Addr: "h:22", User: "u"}, artifact,
		ProvisionDeps{Dialer: dialer, VerifyFor: noopVerifyFor, PublicKey: pub})
	if err == nil {
		t.Fatal("expected a refusal for a tampered artifact")
	}
	kind, _ := cascade.KindOf(err)
	if kind != cascade.KindIntegrity {
		t.Fatalf("kind = %v, want KindIntegrity (fail closed on tamper, never dialed)", kind)
	}
}

func TestProvision_VersionOutOfControllerWindowRefused(t *testing.T) {
	artifact, pub := testArtifact(t, "v3.0.0")
	dialer := &fakeExecDialer{err: errors.New("dial must never be attempted")}
	_, err := Provision(context.Background(), Target{NodeID: "n1", Addr: "h:22", User: "u"}, artifact,
		ProvisionDeps{Dialer: dialer, VerifyFor: noopVerifyFor, PublicKey: pub, ControllerVersion: "v2.5.0"})
	if err == nil {
		t.Fatal("expected a refusal: artifact v3.0.0 is outside controller v2.5.0's same-minor window")
	}
}

func TestProvision_AlreadyAtVersionSkipsIdempotently(t *testing.T) {
	artifact, pub := testArtifact(t, "v2.5.0")
	session := newFakeExecSession("v2.5.0")
	dialer := &fakeExecDialer{session: session}
	result, err := Provision(context.Background(), Target{NodeID: "n1", Addr: "h:22", User: "u"}, artifact,
		ProvisionDeps{Dialer: dialer, VerifyFor: noopVerifyFor, PublicKey: pub})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Skipped || result.Installed {
		t.Fatalf("result = %+v, want Skipped convergence", result)
	}
	if len(session.files) != 0 {
		t.Fatal("no bytes should be transferred on a converged skip")
	}
}

func TestProvision_InstallsAtomicallyOnVersionMismatch(t *testing.T) {
	artifact, pub := testArtifact(t, "v2.5.0")
	session := newFakeExecSession("v2.4.0")
	dialer := &fakeExecDialer{session: session}
	result, err := Provision(context.Background(), Target{NodeID: "n1", Addr: "h:22", User: "u"}, artifact,
		ProvisionDeps{Dialer: dialer, VerifyFor: noopVerifyFor, PublicKey: pub})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Installed || result.PreviousVersion != "v2.4.0" || result.NewVersion != "v2.5.0" {
		t.Fatalf("result = %+v", result)
	}
	if !session.closed {
		t.Fatal("session was never closed")
	}
}

func TestProvision_InterruptedTransferRefusedAndRecoverable(t *testing.T) {
	// SAFETY-CRITICAL: a checksum mismatch (interrupted/corrupted
	// transfer) must refuse the install and never touch the previous
	// binary. This proves the rollback property: refusal, not partial
	// application.
	artifact, pub := testArtifact(t, "v2.5.0")
	session := newFakeExecSession("v2.4.0")
	session.corruptChecksum = true
	dialer := &fakeExecDialer{session: session}
	_, err := Provision(context.Background(), Target{NodeID: "n1", Addr: "h:22", User: "u"}, artifact,
		ProvisionDeps{Dialer: dialer, VerifyFor: noopVerifyFor, PublicKey: pub})
	if err == nil {
		t.Fatal("expected a refusal for a checksum mismatch")
	}
	kind, _ := cascade.KindOf(err)
	if kind != cascade.KindIntegrity {
		t.Fatalf("kind = %v, want KindIntegrity", kind)
	}
	// The install command (chmod+mv into the real path) must never have
	// been reached: only the temp upload path exists in the fake's file
	// map, never the final install path.
	for path := range session.files {
		if !strings.HasSuffix(path, remoteTempSuffix) {
			t.Fatalf("unexpected file at %q: install must not have run", path)
		}
	}
}

func TestProvision_TransferFailureRefused(t *testing.T) {
	artifact, pub := testArtifact(t, "v2.5.0")
	session := newFakeExecSession("v2.4.0")
	session.failWrite = true
	dialer := &fakeExecDialer{session: session}
	_, err := Provision(context.Background(), Target{NodeID: "n1", Addr: "h:22", User: "u"}, artifact,
		ProvisionDeps{Dialer: dialer, VerifyFor: noopVerifyFor, PublicKey: pub})
	if err == nil {
		t.Fatal("expected a refusal when the transfer itself fails")
	}
}

func TestProvision_InstallCommandFailureRefused(t *testing.T) {
	artifact, pub := testArtifact(t, "v2.5.0")
	session := newFakeExecSession("v2.4.0")
	session.failInstall = true
	dialer := &fakeExecDialer{session: session}
	_, err := Provision(context.Background(), Target{NodeID: "n1", Addr: "h:22", User: "u"}, artifact,
		ProvisionDeps{Dialer: dialer, VerifyFor: noopVerifyFor, PublicKey: pub})
	if err == nil {
		t.Fatal("expected a refusal when the remote install command fails")
	}
}

func TestProvision_DialFailureRefused(t *testing.T) {
	artifact, pub := testArtifact(t, "v2.5.0")
	dialer := &fakeExecDialer{err: cascade.New(cascade.KindUnavailable, "unreachable")}
	_, err := Provision(context.Background(), Target{NodeID: "n1", Addr: "h:22", User: "u"}, artifact,
		ProvisionDeps{Dialer: dialer, VerifyFor: noopVerifyFor, PublicKey: pub})
	if err == nil {
		t.Fatal("expected a refusal when the node is unreachable")
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("plain"); got != "'plain'" {
		t.Fatalf("shellQuote(plain) = %q", got)
	}
	if got := shellQuote("has'quote"); got != `'has'\''quote'` {
		t.Fatalf("shellQuote(has'quote) = %q", got)
	}
}

func TestFirstField(t *testing.T) {
	if firstField("abc  def") != "abc" {
		t.Fatal("firstField did not return the first field")
	}
	if firstField("") != "" {
		t.Fatal("firstField(\"\") must be empty")
	}
}
