package nodes

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the signed dispatch envelope — that a forged or
//   replayed frame is refused, and that a superseded attempt cannot pass
//   itself off as the current one.
// SPORT: internal/nodes tests (ADD) — P1-E17-W4-S37-T2.

// signedDispatchFrame builds a frame signed by priv, so each test states only the
// field it is varying.
func signedDispatchFrame(t *testing.T, priv ed25519.PrivateKey, rec DeviceRecord, mutate func(*DispatchFrame)) DispatchFrame {
	t.Helper()
	f := DispatchFrame{
		NodeID:       rec.NodeID,
		EnrollmentID: DeriveEnrollmentID(rec),
		Sequence:     1,
		DispatchID:   "d1",
		Attempt:      1,
		ActionID:     "a1",
		Outcome:      OutcomeSucceeded,
		ResultCommit: "abc123",
	}
	if mutate != nil {
		mutate(&f)
	}
	f = mustSign(t, f, priv)
	return f
}

// enrolledDispatchNode builds a device record with a real key pair.
func enrolledDispatchNode(t *testing.T) (DeviceRecord, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return DeviceRecord{
		NodeID:     "node-a",
		PubKeyB64:  base64.StdEncoding.EncodeToString(pub),
		EnrolledAt: time.Unix(1700000000, 0).UTC(),
		Tier:       TierWorkerTrusted,
	}, priv
}

// TestAGenuineFrameVerifies is the baseline that keeps every refusal below
// meaningful: without it they could all pass because nothing verifies.
func TestAGenuineFrameVerifies(t *testing.T) {
	rec, priv := enrolledDispatchNode(t)
	f := signedDispatchFrame(t, priv, rec, nil)
	if err := VerifyDispatchFrame(f, rec, 0, 1); err != nil {
		t.Fatalf("a genuine frame was refused: %v", err)
	}
}

// TestASupersededAttemptIsRefused is the fencing rule. A partitioned node
// that kept working must not be able to land its stale result once a
// replacement attempt is live.
func TestASupersededAttemptIsRefused(t *testing.T) {
	rec, priv := enrolledDispatchNode(t)
	f := signedDispatchFrame(t, priv, rec, func(f *DispatchFrame) { f.Attempt = 1 })

	err := VerifyDispatchFrame(f, rec, 0, 2) // attempt 2 is now current
	if err == nil {
		t.Fatal("a frame from a superseded attempt was accepted")
	}
	if !errors.Is(err, ErrStaleAttempt) {
		t.Errorf("error = %v, want it to wrap ErrStaleAttempt", err)
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindConflict {
		t.Errorf("kind = %v (ok=%v), want KindConflict", kind, ok)
	}
}

// TestTheAttemptCannotBeRelabelled is the reason the attempt is INSIDE the
// signed payload. Rewriting it in transit must break the signature, not
// merely change a number the controller then trusts.
func TestTheAttemptCannotBeRelabelled(t *testing.T) {
	rec, priv := enrolledDispatchNode(t)
	f := signedDispatchFrame(t, priv, rec, func(f *DispatchFrame) { f.Attempt = 1 })

	// An attacker rewrites the attempt to look current, keeping the
	// signature that was made over attempt 1.
	f.Attempt = 2

	err := VerifyDispatchFrame(f, rec, 0, 2)
	if err == nil {
		t.Fatal("a relabelled attempt was accepted")
	}
	// It must fail as a SIGNATURE failure, not as a stale attempt: the
	// point is that the rewrite is detectable at all.
	if errors.Is(err, ErrStaleAttempt) {
		t.Errorf("error = %v, want a signature refusal — the attempt is inside the signed payload", err)
	}
}

// TestAForgedSignerIsRefused proves a frame signed by some other key is
// refused even when every visible field is right.
func TestAForgedSignerIsRefused(t *testing.T) {
	rec, _ := enrolledDispatchNode(t)
	_, attacker, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	f := signedDispatchFrame(t, attacker, rec, nil)

	if err := VerifyDispatchFrame(f, rec, 0, 1); err == nil {
		t.Fatal("a frame signed by an unenrolled key was accepted")
	}
}

// TestAReplayedSequenceIsRefused proves a captured frame cannot be
// resubmitted.
func TestAReplayedSequenceIsRefused(t *testing.T) {
	rec, priv := enrolledDispatchNode(t)
	f := signedDispatchFrame(t, priv, rec, func(f *DispatchFrame) { f.Sequence = 5 })

	if err := VerifyDispatchFrame(f, rec, 5, 1); err == nil {
		t.Fatal("a frame replaying an already-seen sequence was accepted")
	}
	if err := VerifyDispatchFrame(f, rec, 4, 1); err != nil {
		t.Errorf("a strictly increasing sequence was refused: %v", err)
	}
}

// TestAMismatchedEnrollmentIsRefused proves a frame from a previous
// enrollment of the same node id does not pass.
func TestAMismatchedEnrollmentIsRefused(t *testing.T) {
	rec, priv := enrolledDispatchNode(t)
	f := signedDispatchFrame(t, priv, rec, func(f *DispatchFrame) { f.EnrollmentID = "not-the-enrollment" })

	if err := VerifyDispatchFrame(f, rec, 0, 1); err == nil {
		t.Fatal("a frame carrying another enrollment id was accepted")
	}
}

// TestAFrameForAnotherNodeIsRefused proves the claimed node must be the
// one whose record is being checked.
func TestAFrameForAnotherNodeIsRefused(t *testing.T) {
	rec, priv := enrolledDispatchNode(t)
	f := signedDispatchFrame(t, priv, rec, func(f *DispatchFrame) { f.NodeID = "node-b" })

	if err := VerifyDispatchFrame(f, rec, 0, 1); err == nil {
		t.Fatal("a frame claiming a different node was accepted")
	}
}

// TestAnUnrecognizedOutcomeIsRefused is the fail-closed reading: an
// outcome this build does not understand must not be treated as success.
func TestAnUnrecognizedOutcomeIsRefused(t *testing.T) {
	rec, priv := enrolledDispatchNode(t)
	f := signedDispatchFrame(t, priv, rec, func(f *DispatchFrame) { f.Outcome = DispatchOutcome("mostly-fine") })

	err := VerifyDispatchFrame(f, rec, 0, 1)
	if err == nil {
		t.Fatal("an unrecognized outcome was accepted")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("kind = %v (ok=%v), want KindInvalidInput", kind, ok)
	}
}

// TestEveryKnownOutcomeVerifies keeps the check above from passing by
// refusing everything.
func TestEveryKnownOutcomeVerifies(t *testing.T) {
	rec, priv := enrolledDispatchNode(t)
	for _, outcome := range []DispatchOutcome{
		OutcomeAccepted, OutcomeSucceeded, OutcomeFailed, OutcomeRefused,
	} {
		f := signedDispatchFrame(t, priv, rec, func(f *DispatchFrame) { f.Outcome = outcome })
		if err := VerifyDispatchFrame(f, rec, 0, 1); err != nil {
			t.Errorf("outcome %q was refused: %v", outcome, err)
		}
	}
}

// TestTheResultCommitIsSigned proves an attacker cannot redirect the
// controller to arbitrary content by rewriting the commit in transit.
func TestTheResultCommitIsSigned(t *testing.T) {
	rec, priv := enrolledDispatchNode(t)
	f := signedDispatchFrame(t, priv, rec, nil)
	f.ResultCommit = "deadbeef"

	if err := VerifyDispatchFrame(f, rec, 0, 1); err == nil {
		t.Fatal("a rewritten result commit was accepted")
	}
}

// ed25519Signer adapts a raw key to FrameSigner for tests. Production
// passes NodeKeystore.Sign instead, which never yields the private key.
func ed25519Signer(priv ed25519.PrivateKey) FrameSigner {
	return func(_ context.Context, _ string, payload []byte) ([]byte, error) {
		return ed25519.Sign(priv, payload), nil
	}
}

// mustSign signs f or fails the test.
func mustSign(t *testing.T, f DispatchFrame, priv ed25519.PrivateKey) DispatchFrame {
	t.Helper()
	signed, err := SignDispatchFrame(context.Background(), f, ed25519Signer(priv))
	if err != nil {
		t.Fatalf("SignDispatchFrame: %v", err)
	}
	return signed
}
