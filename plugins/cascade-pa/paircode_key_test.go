package cascadepa

// Purpose (this file): the pairing code's KEYED digest — that the key really
//   binds the digest to the bridge's own credential, that the derivation is
//   stable across restarts, and that a store with no key REFUSES both verbs
//   rather than falling back to an unkeyed hash (the fallback is what made a
//   stolen cascade.db enough to pair).
//
// SPORT: plugins/cascade-pa DerivePairCodeKey/TESTED, CodeDigest/TESTED
//   (P1-E23-W5-S48-T1).

import (
	"context"
	"strings"
	"testing"
)

// TestPairCodeStore_WithNoKeyRefusesBothVerbs: a store built without the
// bridge's credential must refuse, never fall back to an unkeyed digest — the
// fallback is what made a stolen database enough to pair.
func TestPairCodeStore_WithNoKeyRefusesBothVerbs(t *testing.T) {
	ctx := context.Background()
	state := newMemState()
	store := NewPairCodeStore(fixedTestClock{t0()}, state, nil)
	if _, err := store.IssueCode(ctx, fixedEntropy(), testSubject); err == nil {
		t.Fatal("a code was issued with no derived key")
	}
	// Seed a row the KEYED store would have written, then verify without a key.
	keyed, err := CodeDigest(testPairKey(t), "ABCDEFGH")
	if err != nil {
		t.Fatalf("CodeDigest: %v", err)
	}
	state.seed(SubjectState{Subject: testSubject, CodeDigest: keyed,
		CodeExpiresAt: t0().Add(PairCodeTTL)})
	out, err := store.VerifyAndConsume(ctx, testSubject, "ABCDEFGH")
	if err == nil {
		t.Fatalf("verification succeeded with no key: %+v", out)
	}
	if !strings.Contains(err.Error(), "no pairing-code key") {
		t.Fatalf("refusal = %v, want the missing-key refusal", err)
	}
	if out.Bound {
		t.Fatal("an unkeyed store bound a sender")
	}
}

// TestDerivePairCodeKey_IsCredentialBoundAndStable pins the two properties the
// key has to have: the same credential gives the same key (so a restart can
// still verify an outstanding code) and a different credential does not.
func TestDerivePairCodeKey_IsCredentialBoundAndStable(t *testing.T) {
	first, err := DerivePairCodeKey(syntheticCredential)
	if err != nil {
		t.Fatalf("DerivePairCodeKey: %v", err)
	}
	again, err := DerivePairCodeKey(syntheticCredential)
	if err != nil {
		t.Fatalf("DerivePairCodeKey (again): %v", err)
	}
	if string(first) != string(again) {
		t.Fatal("the derivation is not stable, so a restart could not verify an outstanding code")
	}
	other, err := DerivePairCodeKey(syntheticCredential + "2")
	if err != nil {
		t.Fatalf("DerivePairCodeKey (other): %v", err)
	}
	if string(other) == string(first) {
		t.Fatal("two credentials derived one key")
	}
	if len(first) != pairCodeKeyLen {
		t.Fatalf("key length = %d, want %d", len(first), pairCodeKeyLen)
	}
	if _, err := DerivePairCodeKey("   "); err == nil {
		t.Fatal("a blank credential produced a key")
	}
}
