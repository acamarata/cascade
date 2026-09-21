package cascadepa

// Purpose (this file): the shared pairing verifier's tests.
//
// SPORT: plugins/cascade-pa pairing-tests/TEST (P1-E23-W5-S48-T1).

import (
	"bytes"
	"context"
	"crypto/rand"
	"strings"
	"testing"
	"time"
)

func TestGenerateCode_ShapeAndAlphabet(t *testing.T) {
	code, err := GenerateCode(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}
	if len(code) != PairCodeLength {
		t.Fatalf("len(%q) = %d, want %d", code, len(code), PairCodeLength)
	}
	// Crockford base32 drops I, L, O and U so no glyph is ambiguous.
	for _, banned := range []string{"I", "L", "O", "U"} {
		if strings.Contains(code, banned) {
			t.Fatalf("code %q contains the ambiguous glyph %q", code, banned)
		}
	}
	const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	for _, r := range code {
		if !strings.ContainsRune(crockford, r) {
			t.Fatalf("code %q contains %q, which is outside Crockford base32", code, r)
		}
	}
}

func TestGenerateCode_EntropyFailurePropagates(t *testing.T) {
	if _, err := GenerateCode(bytes.NewReader(nil)); err == nil {
		t.Fatal("an exhausted entropy source produced a code")
	}
}

func TestPairCodeTTLAndAttemptsAreTheR1637Constants(t *testing.T) {
	if PairCodeLength != 8 || PairCodeTTL != 10*time.Minute || PairCodeMaxAttempts != 5 {
		t.Fatalf("constants drifted: length %d, ttl %v, attempts %d",
			PairCodeLength, PairCodeTTL, PairCodeMaxAttempts)
	}
}

func TestIssueAndVerify_Bound(t *testing.T) {
	ctx := context.Background()
	store := NewPairCodeStore(fixedTestClock{t0()}, newMemState(), testPairKey(t))
	code, err := store.IssueCode(ctx, fixedEntropy(), testSubject)
	if err != nil {
		t.Fatalf("IssueCode: %v", err)
	}
	outcome, err := store.VerifyAndConsume(ctx, testSubject, code)
	if err != nil {
		t.Fatalf("VerifyAndConsume: %v", err)
	}
	if !outcome.Bound {
		t.Fatalf("outcome = %+v, want Bound", outcome)
	}
	// One use: the same code cannot bind twice.
	again, err := store.VerifyAndConsume(ctx, testSubject, code)
	if err != nil {
		t.Fatalf("VerifyAndConsume: %v", err)
	}
	if again.Bound {
		t.Fatal("a consumed code bound a second time")
	}
}

// TestVerify_IsCaseInsensitiveOnTheCanonicalForm: an operator retyping a code
// in lower case must still pair, because the canonical form is upper case.
func TestVerify_IsCaseInsensitiveOnTheCanonicalForm(t *testing.T) {
	ctx := context.Background()
	store := NewPairCodeStore(fixedTestClock{t0()}, newMemState(), testPairKey(t))
	code, err := store.IssueCode(ctx, fixedEntropy(), testSubject)
	if err != nil {
		t.Fatalf("IssueCode: %v", err)
	}
	outcome, err := store.VerifyAndConsume(ctx, testSubject, strings.ToLower(code))
	if err != nil {
		t.Fatalf("VerifyAndConsume: %v", err)
	}
	if !outcome.Bound {
		t.Fatal("a lower-cased code was refused")
	}
}

// TestCodeDigest_IsWhatIsPersisted is the durability-without-plaintext proof:
// the stored row carries a digest, never the code.
func TestCodeDigest_IsWhatIsPersisted(t *testing.T) {
	ctx := context.Background()
	state := newMemState()
	store := NewPairCodeStore(fixedTestClock{t0()}, state, testPairKey(t))
	code, err := store.IssueCode(ctx, fixedEntropy(), testSubject)
	if err != nil {
		t.Fatalf("IssueCode: %v", err)
	}
	row, ok, err := state.Load(ctx, testSubject)
	if err != nil || !ok {
		t.Fatalf("Load = (%v, %v)", ok, err)
	}
	if strings.Contains(row.CodeDigest, code) || row.CodeDigest == code {
		t.Fatalf("the plaintext code was persisted: %q", row.CodeDigest)
	}
	want, err := CodeDigest(testPairKey(t), code)
	if err != nil {
		t.Fatalf("CodeDigest: %v", err)
	}
	if row.CodeDigest != want {
		t.Fatalf("stored digest %q != CodeDigest(key, code)", row.CodeDigest)
	}
	if len(row.CodeDigest) != 64 {
		t.Fatalf("digest length = %d, want a 64-character HMAC-SHA256 hex string", len(row.CodeDigest))
	}
	// The digest is KEYED: the same code under another bridge's credential is a
	// different digest, so one host's stored digest says nothing about another's
	// and an unkeyed rainbow table over the 40-bit code space is useless.
	otherKey, err := DerivePairCodeKey(syntheticCredential + "-other")
	if err != nil {
		t.Fatalf("DerivePairCodeKey: %v", err)
	}
	other, err := CodeDigest(otherKey, code)
	if err != nil {
		t.Fatalf("CodeDigest(other): %v", err)
	}
	if other == row.CodeDigest {
		t.Fatal("the digest does not depend on the key: two credentials produced one digest")
	}
}

func TestVerify_WrongCode(t *testing.T) {
	ctx := context.Background()
	state := newMemState()
	store := NewPairCodeStore(fixedTestClock{t0()}, state, testPairKey(t))
	if _, err := store.IssueCode(ctx, fixedEntropy(), testSubject); err != nil {
		t.Fatalf("IssueCode: %v", err)
	}
	outcome, err := store.VerifyAndConsume(ctx, testSubject, "00000000")
	if err != nil {
		t.Fatalf("VerifyAndConsume: %v", err)
	}
	if !outcome.Refused() {
		t.Fatalf("outcome = %+v, want Refused", outcome)
	}
	row, _, _ := state.Load(ctx, testSubject)
	if row.WrongAttempts != 1 {
		t.Fatalf("WrongAttempts = %d, want 1", row.WrongAttempts)
	}
}

func TestVerify_ExpiredCode(t *testing.T) {
	ctx := context.Background()
	clock := &advancingClock{at: t0()}
	store := NewPairCodeStore(clock, newMemState(), testPairKey(t))
	code, err := store.IssueCode(ctx, fixedEntropy(), testSubject)
	if err != nil {
		t.Fatalf("IssueCode: %v", err)
	}
	clock.advance(PairCodeTTL + time.Second)
	outcome, err := store.VerifyAndConsume(ctx, testSubject, code)
	if err != nil {
		t.Fatalf("VerifyAndConsume: %v", err)
	}
	if !outcome.Refused() {
		t.Fatalf("an expired code produced %+v", outcome)
	}
}

func TestVerify_UnknownSubjectAndEmptyInputs(t *testing.T) {
	ctx := context.Background()
	store := NewPairCodeStore(fixedTestClock{t0()}, newMemState(), testPairKey(t))
	for _, tc := range []struct{ subject, candidate string }{
		{testSubject, "ABCDEFGH"}, // no code was ever issued
		{"", "ABCDEFGH"},
		{testSubject, ""},
	} {
		outcome, err := store.VerifyAndConsume(ctx, tc.subject, tc.candidate)
		if err != nil {
			t.Fatalf("VerifyAndConsume(%q,%q): %v", tc.subject, tc.candidate, err)
		}
		if !outcome.Refused() {
			t.Fatalf("VerifyAndConsume(%q,%q) = %+v, want Refused", tc.subject, tc.candidate, outcome)
		}
	}
}

func TestVerify_FifthWrongAttemptBurnsCode(t *testing.T) {
	ctx := context.Background()
	store := NewPairCodeStore(fixedTestClock{t0()}, newMemState(), testPairKey(t))
	code, err := store.IssueCode(ctx, fixedEntropy(), testSubject)
	if err != nil {
		t.Fatalf("IssueCode: %v", err)
	}
	for i := 1; i < PairCodeMaxAttempts; i++ {
		outcome, verr := store.VerifyAndConsume(ctx, testSubject, "ZZZZZZZZ")
		if verr != nil {
			t.Fatalf("attempt %d: %v", i, verr)
		}
		if outcome.LockedOut {
			t.Fatalf("locked out on attempt %d, want %d", i, PairCodeMaxAttempts)
		}
	}
	outcome, err := store.VerifyAndConsume(ctx, testSubject, "ZZZZZZZZ")
	if err != nil {
		t.Fatalf("final attempt: %v", err)
	}
	if !outcome.LockedOut {
		t.Fatalf("attempt %d produced %+v, want LockedOut", PairCodeMaxAttempts, outcome)
	}
	after, err := store.VerifyAndConsume(ctx, testSubject, code)
	if err != nil {
		t.Fatalf("post-lockout verify: %v", err)
	}
	if after.Bound {
		t.Fatal("the correct code still bound after the lockout burned it")
	}
}

func TestIssueCode_SupersedesPriorPendingAndResetsAttempts(t *testing.T) {
	ctx := context.Background()
	state := newMemState()
	store := NewPairCodeStore(fixedTestClock{t0()}, state, testPairKey(t))
	first, err := store.IssueCode(ctx, fixedEntropy(), testSubject)
	if err != nil {
		t.Fatalf("IssueCode: %v", err)
	}
	if _, err := store.VerifyAndConsume(ctx, testSubject, "ZZZZZZZZ"); err != nil {
		t.Fatalf("VerifyAndConsume: %v", err)
	}
	second, err := store.IssueCode(ctx, bytes.NewReader([]byte{9, 8, 7, 6, 5}), testSubject)
	if err != nil {
		t.Fatalf("IssueCode: %v", err)
	}
	if first == second {
		t.Fatal("the second issuance produced the same code")
	}
	row, _, _ := state.Load(ctx, testSubject)
	if row.WrongAttempts != 0 {
		t.Fatalf("WrongAttempts = %d after reissue, want 0", row.WrongAttempts)
	}
	if outcome, _ := store.VerifyAndConsume(ctx, testSubject, first); outcome.Bound {
		t.Fatal("the superseded code still bound")
	}
}

func TestIssueCode_RequiresSubject(t *testing.T) {
	if _, err := NewPairCodeStore(fixedTestClock{t0()}, newMemState(), testPairKey(t)).
		IssueCode(context.Background(), fixedEntropy(), ""); err == nil {
		t.Fatal("a code was issued with no subject")
	}
}

func TestPending(t *testing.T) {
	ctx := context.Background()
	clock := &advancingClock{at: t0()}
	store := NewPairCodeStore(clock, newMemState(), testPairKey(t))
	if pending, err := store.Pending(ctx, testSubject); err != nil || pending {
		t.Fatalf("Pending before issuance = (%v, %v)", pending, err)
	}
	if _, err := store.IssueCode(ctx, fixedEntropy(), testSubject); err != nil {
		t.Fatalf("IssueCode: %v", err)
	}
	if pending, err := store.Pending(ctx, testSubject); err != nil || !pending {
		t.Fatalf("Pending after issuance = (%v, %v)", pending, err)
	}
	clock.advance(PairCodeTTL + time.Second)
	if pending, err := store.Pending(ctx, testSubject); err != nil || pending {
		t.Fatalf("Pending after expiry = (%v, %v)", pending, err)
	}
}
