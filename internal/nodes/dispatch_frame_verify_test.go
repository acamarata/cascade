package nodes

// Purpose (this file): the refusal branches of VerifyDispatchFrame and of
//   the rendezvous, which had no test: a corrupt stored public key, a
//   signature that is the right shape and the wrong bytes, and a rendezvous
//   published with a missing identifier.
// WHY: these are the checks that decide whether a frame claiming to be from
//   a node is acted on. Each one was reachable only by being wrong, and
//   none of them was ever driven, so nothing proved the refusal is a typed
//   error rather than a nil-pointer panic in the verifier.
// SPORT: internal/nodes tests (ADD) — P1-E17-W4-S37-T2.

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestAFrameAgainstACorruptStoredKeyIsAnIntegrityFailure separates the two
// things that go wrong here. A bad SIGNATURE is the node's problem and is
// reported as an unverified signer; a bad STORED KEY is the controller's
// own record being damaged, and calling that "unverified signer" would send
// an operator to inspect the wrong machine.
func TestAFrameAgainstACorruptStoredKeyIsAnIntegrityFailure(t *testing.T) {
	rec := DeviceRecord{NodeID: "n1", PubKeyB64: "this is not base64 of a key"}
	err := VerifyDispatchFrame(DispatchFrame{NodeID: "n1"}, rec, 0, 1)
	if err == nil {
		t.Fatal("a frame verified against a corrupt stored key was accepted")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindIntegrity {
		t.Errorf("kind = %v (typed=%v), want %v: a damaged record is not a bad signer",
			kind, ok, cascade.KindIntegrity)
	}
}

// TestAWellFormedSignatureFromTheWrongKeyIsRefused is the branch that
// matters most: the signature decodes, is exactly the right length, and
// still does not verify. A check that stopped at "decodes and is 64 bytes"
// would pass this.
func TestAWellFormedSignatureFromTheWrongKeyIsRefused(t *testing.T) {
	enrolled, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, impostorPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := DeviceRecord{NodeID: "n1", PubKeyB64: base64.StdEncoding.EncodeToString(enrolled)}

	frame := DispatchFrame{NodeID: "n1", DispatchID: "d1", Attempt: 1, Outcome: OutcomeSucceeded}
	sig := ed25519.Sign(impostorPriv, frame.signingPayload())
	frame.SignatureB64 = base64.StdEncoding.EncodeToString(sig)
	if len(sig) != ed25519.SignatureSize {
		t.Fatalf("test built a %d-byte signature; the branch under test needs a well-formed one", len(sig))
	}

	err = VerifyDispatchFrame(frame, rec, 0, 1)
	if err == nil {
		t.Fatal("a frame signed by a key the controller never enrolled was accepted")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindPermissionDenied {
		t.Errorf("kind = %v (typed=%v), want %v", kind, ok, cascade.KindPermissionDenied)
	}
}

// TestARendezvousWithoutBothIdentifiersIsRefused covers the publish
// boundary. A rendezvous keyed on an empty dispatch id would make every
// such call collide with every other one.
func TestARendezvousWithoutBothIdentifiersIsRefused(t *testing.T) {
	rv := NewRendezvous()
	for _, tc := range []struct {
		name    string
		nodeID  string
		attempt Attempt
	}{
		{"no dispatch id", "n1", Attempt{Attempt: 1}},
		{"no node id", "", Attempt{DispatchID: "d1", Attempt: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := rv.Call(context.Background(), tc.nodeID, tc.attempt); err == nil {
				t.Fatal("an unidentified rendezvous was published")
			}
		})
	}
}

// TestClaimingWithNoNodeIDIsRefused is the other side of the same rule: a
// claim with no node id would be handed the first pending attempt for any
// node at all.
func TestClaimingWithNoNodeIDIsRefused(t *testing.T) {
	_, err := NewRendezvous().Claim("")
	if err == nil {
		t.Fatal("a claim with no node id was accepted")
	}
	if !strings.Contains(err.Error(), "node id") {
		t.Errorf("error = %v, want it to say which identifier was missing", err)
	}
}
