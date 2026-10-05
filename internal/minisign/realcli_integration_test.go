//go:build integration

// Purpose: signs a fresh artifact with the REAL minisign CLI (found on PATH;
//   skips otherwise) using an ephemeral keypair generated for this test run
//   only, so the integration proof never rests on a committed fixture.
// SPORT: internal/minisign realArtifact

package minisign

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// realArtifact signs data with the real minisign CLI and returns the data,
// the signature file bytes and the parsed public key.
func realArtifact(t *testing.T, version string) ([]byte, []byte, PublicKey) {
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
	pub, err := ParsePublicKey(pubBytes)
	if err != nil {
		t.Fatalf("parse pubkey: %v", err)
	}
	return data, sig, pub
}

// newlineReader feeds an endless stream of newlines to minisign's
// interactive password prompts (the -W flag still reads one confirmation
// newline in some builds); a fixed buffer would EOF, so this loops.
type newlineReader struct{}

func (newlineReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = '\n'
	}
	return len(p), nil
}
