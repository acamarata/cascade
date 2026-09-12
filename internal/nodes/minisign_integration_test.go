//go:build integration

// Purpose: Art.2's real-counterpart proof for minisign verification in
//   isolation from ssh: ParseMinisignSignature/VerifyMinisign driven
//   against signatures the REAL minisign CLI produces at test run time,
//   with a fresh ephemeral keypair (never committed) — the same
//   provenance discipline as provision_integration_test.go's
//   realMinisignArtifact, factored out so this ticket's own
//   `-run TestVersionVerifyRealMinisign` check has a standalone target
//   that does not also require a real sshd.
// SPORT: internal/nodes TestVersionVerifyRealMinisign (P1-E17-W4-S36-T5).

package nodes

import "testing"

// TestVersionVerifyRealMinisign proves VerifyMinisign against the real
// minisign 0.12+ CLI: sign with the real tool, verify with this
// package's own parser, then confirm the real tool's OWN "-V" verify
// agrees — the strongest available proof neither side drifted from the
// other's wire format.
func TestVersionVerifyRealMinisign(t *testing.T) {
	artifact, pub := realMinisignArtifact(t, "v2.5.0")
	sig, err := ParseMinisignSignature(artifact.Signature)
	if err != nil {
		t.Fatalf("ParseMinisignSignature: %v", err)
	}
	if err := VerifyMinisign(pub, artifact.Data, sig); err != nil {
		t.Fatalf("VerifyMinisign against a real minisign-produced signature: %v", err)
	}

	// A tampered message must be refused by this package's verifier too
	// — never only by the CLI.
	tampered := append([]byte{}, artifact.Data...)
	tampered[0] ^= 0xFF
	if err := VerifyMinisign(pub, tampered, sig); err == nil {
		t.Fatal("expected a refusal for a tampered message against a real minisign signature")
	}
}
