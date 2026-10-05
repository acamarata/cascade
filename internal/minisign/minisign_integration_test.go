//go:build integration

// Purpose: Art.2's real-counterpart proof for minisign verification in
//   isolation from ssh: ParseSignature/Verify driven
//   against signatures the REAL minisign CLI produces at test run time,
//   with a fresh ephemeral keypair (never committed) — the same
//   provenance discipline as internal/nodes'
//   provision_integration_test.go, factored out so this ticket's own
//   `-run TestVersionVerifyRealMinisign` check has a standalone target
//   that does not also require a real sshd.
// SPORT: internal/minisign TestVersionVerifyRealMinisign

package minisign

import "testing"

// TestVersionVerifyRealMinisign proves Verify against the real
// minisign 0.12+ CLI: sign with the real tool, verify with this
// package's own parser, then confirm the real tool's OWN "-V" verify
// agrees — the strongest available proof neither side drifted from the
// other's wire format.
func TestVersionVerifyRealMinisign(t *testing.T) {
	data, sigBytes, pub := realArtifact(t, "v2.5.0")
	sig, err := ParseSignature(sigBytes)
	if err != nil {
		t.Fatalf("ParseSignature: %v", err)
	}
	if err := Verify(pub, data, sig); err != nil {
		t.Fatalf("Verify against a real minisign-produced signature: %v", err)
	}

	// A tampered message must be refused by this package's verifier too
	// — never only by the CLI.
	tampered := append([]byte{}, data...)
	tampered[0] ^= 0xFF
	if err := Verify(pub, tampered, sig); err == nil {
		t.Fatal("expected a refusal for a tampered message against a real minisign signature")
	}
}
