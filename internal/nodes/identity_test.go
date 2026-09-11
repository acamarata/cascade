package nodes

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestGenerateIdentityDeterministic(t *testing.T) {
	seed := strings.NewReader(strings.Repeat("a", 64))
	id, priv, err := GenerateIdentity(seed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(priv) != ed25519.PrivateKeySize {
		t.Fatalf("private key length %d, want %d", len(priv), ed25519.PrivateKeySize)
	}
	if id.NodeID != DeriveNodeID(id.PubKey) {
		t.Fatal("returned identity's NodeID does not match its own pubkey derivation")
	}
	if len(id.NodeID) != NodeIDLen {
		t.Fatalf("node id length %d, want %d", len(id.NodeID), NodeIDLen)
	}
}

func TestDeriveNodeIDStableAndDistinct(t *testing.T) {
	id1, _, err := GenerateIdentity(strings.NewReader(strings.Repeat("a", 64)))
	if err != nil {
		t.Fatal(err)
	}
	id2, _, err := GenerateIdentity(strings.NewReader(strings.Repeat("b", 64)))
	if err != nil {
		t.Fatal(err)
	}
	if DeriveNodeID(id1.PubKey) != id1.NodeID {
		t.Fatal("derivation not stable")
	}
	if id1.NodeID == id2.NodeID {
		t.Fatal("distinct keys must derive distinct node ids")
	}
}

func TestParsePublicKeyRefusals(t *testing.T) {
	valid := ed25519.PublicKey(make([]byte, ed25519.PublicKeySize))
	valid[0] = 1 // avoid all-zero

	cases := []struct {
		name string
		b64  string
	}{
		{"empty", ""},
		{"not base64", "!!!not-base64!!!"},
		{"wrong length", base64.StdEncoding.EncodeToString([]byte("too-short"))},
		{"all zero", base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParsePublicKey(c.b64)
			if err == nil {
				t.Fatal("expected refusal, got nil error")
			}
			if k, ok := cascade.KindOf(err); !ok || k != cascade.KindInvalidInput {
				t.Fatalf("expected KindInvalidInput, got %v (ok=%v)", k, ok)
			}
		})
	}

	// The one valid case must succeed.
	if _, err := ParsePublicKey(base64.StdEncoding.EncodeToString(valid)); err != nil {
		t.Fatalf("expected success for a valid key: %v", err)
	}
}

func TestValidateIdentityMismatchRefused(t *testing.T) {
	id, _, err := GenerateIdentity(strings.NewReader(strings.Repeat("c", 64)))
	if err != nil {
		t.Fatal(err)
	}
	// Correct pairing succeeds.
	got, err := ValidateIdentity(id.NodeID, id.PubKeyB64())
	if err != nil {
		t.Fatalf("unexpected error on valid pairing: %v", err)
	}
	if got.NodeID != id.NodeID {
		t.Fatal("round-trip node id mismatch")
	}

	// A claimed node id that does not match the key's own derivation must
	// be refused, never silently recomputed.
	_, err = ValidateIdentity("0000000000000000000000000000ff", id.PubKeyB64())
	if err == nil {
		t.Fatal("expected refusal for mismatched node id, got nil")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindInvalidInput {
		t.Fatalf("expected KindInvalidInput, got %v (ok=%v)", k, ok)
	}

	// Empty node id is refused too.
	if _, err := ValidateIdentity("", id.PubKeyB64()); err == nil {
		t.Fatal("expected refusal for empty node id")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) {
	return 0, errors.New("simulated entropy source failure")
}

func TestGenerateIdentityEntropyFailure(t *testing.T) {
	_, _, err := GenerateIdentity(errReader{})
	if err == nil {
		t.Fatal("expected error when the entropy source fails")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindInternal {
		t.Fatalf("expected KindInternal, got %v (ok=%v)", k, ok)
	}
}
