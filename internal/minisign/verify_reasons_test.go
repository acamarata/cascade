// Purpose: pins the refusal contract of Verify by identity and message, not
//   by Kind alone (errors.Is compares Kind only): each failure mode returns a
//   KindIntegrity error whose exact message names its reason.
// SPORT: internal/minisign TestVerify_FailureNamesReason

package minisign

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestVerify_FailureNamesReason(t *testing.T) {
	tamperedMsg := readTestdataMinisign(t, "artifact.bin")
	tamperedMsg[0] ^= 0xFF

	cases := []struct {
		name   string
		mutate func(msg []byte, pub *PublicKey, sig *Signature) []byte
		reason string
	}{
		{"tampered message", func(_ []byte, _ *PublicKey, _ *Signature) []byte { return tamperedMsg },
			"message signature does not verify"},
		{"wrong key id", func(m []byte, pub *PublicKey, _ *Signature) []byte { pub.KeyID[0] ^= 0xFF; return m },
			"signature key id does not match the verifying public key"},
		{"flipped global signature", func(m []byte, _ *PublicKey, sig *Signature) []byte { sig.GlobalSignature[0] ^= 0xFF; return m },
			"global (trusted comment) signature does not verify"},
		{"short public key", func(m []byte, pub *PublicKey, _ *Signature) []byte { pub.Key = pub.Key[:len(pub.Key)-1]; return m },
			"public key is not a valid ed25519 key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := readTestdataMinisign(t, "artifact.bin")
			sig, err := ParseSignature(readTestdataMinisign(t, "artifact.bin.minisig"))
			if err != nil {
				t.Fatalf("ParseSignature: %v", err)
			}
			pub, err := ParsePublicKey(readTestdataMinisign(t, "test.pub"))
			if err != nil {
				t.Fatalf("ParsePublicKey: %v", err)
			}
			msg = tc.mutate(msg, &pub, &sig)

			got := Verify(pub, msg, sig)
			if got == nil {
				t.Fatalf("%s: Verify returned nil", tc.name)
			}
			want := ErrSignatureInvalid(tc.reason)
			if got.Error() != want.Error() {
				t.Fatalf("message = %q, want %q", got.Error(), want.Error())
			}
			if kind, ok := cascade.KindOf(got); !ok || kind != cascade.KindIntegrity {
				t.Fatalf("kind = %v (ok=%v), want KindIntegrity", kind, ok)
			}
		})
	}
}
