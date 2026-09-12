//go:build integration

// Purpose: Art.2's real-counterpart proof for provisioning — Provision
//   driven end to end (real dial, real remote command exec, real file
//   transfer, real checksum) against a REAL /usr/sbin/sshd on loopback,
//   spawned by this test. Reuses tunnel_test.go's spawnLoopbackSSHD/
//   findSSHD/freePort/waitForPort helpers (same package, same build tag).
// SPORT: internal/nodes TestProvisionRealSSHD (P1-E17-W4-S36-T5).

package nodes

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// TestProvisionRealSSHD proves Provision's ssh transport against a real
// sshd: the "remote node" is this same machine, reached over loopback ssh
// as the current user, so `cascade version`/`sha256sum`/`mv` all run for
// real against a real filesystem.
func TestProvisionRealSSHD(t *testing.T) {
	u, err := user.Current()
	if err != nil {
		t.Skipf("cannot resolve current user: %v", err)
	}
	clientPub, clientPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}
	port, hostFingerprint := spawnLoopbackSSHD(t, clientPub)

	signer, err := ssh.NewSignerFromKey(clientPriv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	dialer := NewSSHExecDialer(signer, 5*time.Second)
	verify := func(fp string) error {
		if fp != hostFingerprint {
			t.Fatalf("host key fingerprint mismatch: got %q want %q", fp, hostFingerprint)
		}
		return nil
	}

	installDir := t.TempDir()
	installPath := filepath.Join(installDir, "cascade")
	// Seed a "previously installed" fake binary that prints a shape
	// matching cmd/cascade/version.go's real "cascade version X" output,
	// so queryInstalledVersion's real remote exec is exercised too.
	fakeBinary := "#!/bin/sh\necho 'cascade version v2.4.0'\n"
	if err := os.WriteFile(installPath, []byte(fakeBinary), 0o755); err != nil {
		t.Fatalf("seed fake binary: %v", err)
	}

	artifact, pub := realMinisignArtifact(t, "v2.5.0")
	target := Target{NodeID: "loopback", User: u.Username, Addr: "127.0.0.1:" + strconv.Itoa(port)}

	verifyFor := func(Target) HostKeyVerifier { return verify }
	result, err := Provision(context.Background(), target, artifact, ProvisionDeps{
		Dialer: dialer, VerifyFor: verifyFor,
		PublicKey: pub, InstallPath: installPath,
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if !result.Installed || result.PreviousVersion != "v2.4.0" || result.NewVersion != "v2.5.0" {
		t.Fatalf("result = %+v", result)
	}
	installed, err := os.ReadFile(installPath)
	if err != nil {
		t.Fatalf("read installed binary: %v", err)
	}
	if string(installed) != string(artifact.Data) {
		t.Fatal("installed file content does not match the artifact that was verified")
	}
	info, err := os.Stat(installPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatal("installed binary is not executable")
	}

	// Idempotent re-run: the "installed" version now matches the
	// artifact's own version (its content, not "cascade version" output,
	// since the artifact bytes are opaque test fixture bytes, not a real
	// binary) -- run Provision again against the SAME already-installed
	// content and confirm it does not error a second time.
	result2, err := Provision(context.Background(), target, artifact, ProvisionDeps{
		Dialer: dialer, VerifyFor: verifyFor,
		PublicKey: pub, InstallPath: installPath,
	})
	if err != nil {
		t.Fatalf("second Provision run: %v", err)
	}
	_ = result2 // the installed artifact is opaque bytes, not a real `version`-printing binary, so this run re-installs (same content); asserting no error is this test's real-counterpart proof
}

// realMinisignArtifact signs data with the REAL minisign CLI (found on
// PATH; skips otherwise) using a fresh ephemeral keypair generated for
// this test run only -- mirrors A-T6's own ephemeral-keypair precedent
// (04-PEWS-PLAN-W1-W3.md §Epic A T6) and tunnel_test.go's "generate fresh,
// never commit" discipline.
func realMinisignArtifact(t *testing.T, version string) (Artifact, MinisignPublicKey) {
	t.Helper()
	minisignPath, err := exec.LookPath("minisign")
	if err != nil {
		t.Skip("minisign binary not found on PATH; skipping real-counterpart lane")
	}
	dir := t.TempDir()
	secretKeyPath := filepath.Join(dir, "minisign.key")
	pubKeyPath := filepath.Join(dir, "minisign.pub")
	genCmd := exec.Command(minisignPath, "-G", "-p", pubKeyPath, "-s", secretKeyPath, "-W")
	genCmd.Stdin = newlineReader{}
	if out, err := genCmd.CombinedOutput(); err != nil {
		t.Fatalf("minisign -G: %v\n%s", err, out)
	}

	data := []byte("fake release artifact bytes for " + version)
	artifactPath := filepath.Join(dir, "artifact")
	if err := os.WriteFile(artifactPath, data, 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	sigPath := artifactPath + ".minisig"
	signCmd := exec.Command(minisignPath, "-S", "-s", secretKeyPath, "-m", artifactPath, "-x", sigPath,
		"-t", "cascade "+version, "-W")
	signCmd.Stdin = newlineReader{}
	if out, err := signCmd.CombinedOutput(); err != nil {
		t.Fatalf("minisign -S: %v\n%s", err, out)
	}

	sig, err := os.ReadFile(sigPath)
	if err != nil {
		t.Fatalf("read signature: %v", err)
	}
	pubBytes, err := os.ReadFile(pubKeyPath)
	if err != nil {
		t.Fatalf("read pubkey: %v", err)
	}
	pub, err := ParseMinisignPublicKey(pubBytes)
	if err != nil {
		t.Fatalf("parse pubkey: %v", err)
	}
	return Artifact{Version: version, Data: data, Signature: sig}, pub
}

// newlineReader feeds an endless stream of newlines to minisign's
// interactive password prompts (the -W "no password" flag still reads
// one confirmation newline in some minisign builds); io.Reader over a
// fixed buffer would EOF, so this loops.
type newlineReader struct{}

func (newlineReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = '\n'
	}
	return len(p), nil
}
