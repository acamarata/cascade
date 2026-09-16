package nodes

import (
	"context"
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the ship sequence's ORDER, which is the security
//   contract — a refusal must cost no egress, and results must never be
//   fetched on the word of an unverified frame.
// SPORT: internal/nodes tests (ADD) — P1-E17-W4-S37-T2.

// recordingCaller answers the node leg from a canned frame and records
// whether it was reached at all.
type recordingCaller struct {
	frame  DispatchFrame
	err    error
	calls  int
	signer ed25519.PrivateKey
	rec    DeviceRecord
}

func (c *recordingCaller) Call(_ context.Context, _ string, attempt Attempt) (DispatchFrame, error) {
	c.calls++
	if c.err != nil {
		return DispatchFrame{}, c.err
	}
	f := c.frame
	f.NodeID = c.rec.NodeID
	f.EnrollmentID = DeriveEnrollmentID(c.rec)
	f.DispatchID = attempt.DispatchID
	if f.Attempt == 0 {
		f.Attempt = attempt.Attempt
	}
	signed, err := SignDispatchFrame(context.Background(), f, ed25519Signer(c.signer))
	if err != nil {
		return DispatchFrame{}, err
	}
	return signed, nil
}

// shipHarness wires a dispatch over a real repository and a fake node.
func shipHarness(t *testing.T, frame DispatchFrame) (ShipDeps, DeviceRecord, *recordingCaller, string) {
	t.Helper()
	root, head := newGitRepo(t)
	remote := t.TempDir()
	runner := testGitRunner{}
	if _, err := runner.Run(context.Background(), remote, "init", "--bare"); err != nil {
		t.Fatal(err)
	}

	rec, priv := enrolledDispatchNode(t)
	caller := &recordingCaller{frame: frame, signer: priv, rec: rec}
	deps := ShipDeps{
		Git:       newDispatchGit(root, runner),
		Caller:    caller,
		Attempts:  NewAttemptRegister(),
		Sequences: NewSequenceStore(),
		Remote:    remote,
	}
	return deps, rec, caller, head
}

// TestShipDeliversAndReturnsTheResultCommit is the happy path, asserted on
// the commit that actually came back rather than on a bare nil error.
func TestShipDeliversAndReturnsTheResultCommit(t *testing.T) {
	deps, rec, caller, head := shipHarness(t, DispatchFrame{
		Sequence: 1, ActionID: "a1", Outcome: OutcomeSucceeded,
	})

	got, err := Ship(context.Background(), deps, rec, ShipRequest{
		DispatchID:  "d1",
		Head:        head,
		Work:        Action{ID: "a1"},
		Sensitivity: SensitivityNormal,
	})
	if err != nil {
		t.Fatalf("Ship: %v", err)
	}
	if got.Commit != head {
		t.Errorf("result commit = %s, want %s", got.Commit, head)
	}
	if got.Attempt == 0 {
		t.Error("the outcome reports no attempt, so its journal records cannot be correlated")
	}
	if caller.calls != 1 {
		t.Errorf("the node was called %d times, want 1", caller.calls)
	}
}

// TestAnInadmissibleDispatchCostsNoEgress is the ordering rule that
// matters most: local-only work must be refused BEFORE anything is pushed
// and before the node is reached at all.
func TestAnInadmissibleDispatchCostsNoEgress(t *testing.T) {
	deps, rec, caller, head := shipHarness(t, DispatchFrame{
		Sequence: 1, Outcome: OutcomeSucceeded,
	})

	_, err := Ship(context.Background(), deps, rec, ShipRequest{
		DispatchID:  "d1",
		Head:        head,
		Work:        Action{ID: "a1"},
		Sensitivity: SensitivityLocalOnly,
	})
	if err == nil {
		t.Fatal("local-only work was shipped")
	}
	if caller.calls != 0 {
		t.Errorf("the node was reached %d times for work that may never leave the machine", caller.calls)
	}
	if got := deps.Attempts.Current("d1"); got != 0 {
		t.Errorf("an attempt was minted (%d) for a refused dispatch", got)
	}
}

// TestAStaticKeyInThePayloadIsRefusedBeforeShipping proves the defense in
// depth actually runs before egress, not after.
func TestAStaticKeyInThePayloadIsRefusedBeforeShipping(t *testing.T) {
	deps, rec, caller, head := shipHarness(t, DispatchFrame{
		Sequence: 1, Outcome: OutcomeSucceeded,
	})

	_, err := Ship(context.Background(), deps, rec, ShipRequest{
		DispatchID:  "d1",
		Head:        head,
		Work:        Action{ID: "a1"},
		Sensitivity: SensitivityNormal,
		Payload:     []byte(`{"env":{"OPENAI_API_KEY":"sk-livekeymaterial"}}`),
	})
	if err == nil {
		t.Fatal("a payload carrying a static key was shipped")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindPolicyDenied {
		t.Errorf("kind = %v (ok=%v), want KindPolicyDenied", kind, ok)
	}
	if caller.calls != 0 {
		t.Error("the node was reached despite the payload carrying a static key")
	}
}

// TestACallFailureIsADroppedTunnelNotAnUnreachableNode is the distinction
// S-37.T3's recovery depends on. The branch was already pushed, so work
// may be running: treating this as "never shipped" would abandon it.
func TestACallFailureIsADroppedTunnelNotAnUnreachableNode(t *testing.T) {
	deps, rec, caller, head := shipHarness(t, DispatchFrame{})
	caller.err = errors.New("connection reset")

	_, err := Ship(context.Background(), deps, rec, ShipRequest{
		DispatchID: "d1", Head: head, Work: Action{ID: "a1"}, Sensitivity: SensitivityNormal,
	})
	if err == nil {
		t.Fatal("a failed node call reported success")
	}
	if !strings.Contains(err.Error(), "outcome is unknown") {
		t.Errorf("error = %v, want the dropped-tunnel wording (work may be running)", err)
	}
}

// TestASupersededFrameIsRefusedOnTheShipPath proves fencing reaches the
// real sequence, not just VerifyDispatchFrame in isolation.
func TestASupersededFrameIsRefusedOnTheShipPath(t *testing.T) {
	deps, rec, caller, head := shipHarness(t, DispatchFrame{
		Sequence: 1, ActionID: "a1", Outcome: OutcomeSucceeded,
		Attempt: 99, // a frame from an attempt this controller never minted
	})
	_ = caller

	_, err := Ship(context.Background(), deps, rec, ShipRequest{
		DispatchID: "d1", Head: head, Work: Action{ID: "a1"}, Sensitivity: SensitivityNormal,
	})
	if err == nil {
		t.Fatal("a frame from a superseded attempt was accepted")
	}
	if !errors.Is(err, ErrStaleAttempt) {
		t.Errorf("error = %v, want it to wrap ErrStaleAttempt", err)
	}
}

// TestARefusedDuplicateIsReportedAsAConflict proves a node that already
// ran the action is reported as a conflict — the work happened — rather
// than as a failure a caller would retry.
func TestARefusedDuplicateIsReportedAsAConflict(t *testing.T) {
	deps, rec, _, head := shipHarness(t, DispatchFrame{
		Sequence: 1, ActionID: "a1", Outcome: OutcomeRefused,
	})

	_, err := Ship(context.Background(), deps, rec, ShipRequest{
		DispatchID: "d1", Head: head, Work: Action{ID: "a1"}, Sensitivity: SensitivityNormal,
	})
	if err == nil {
		t.Fatal("a refused duplicate reported success")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindConflict {
		t.Errorf("kind = %v (ok=%v), want KindConflict", kind, ok)
	}
}

// TestAnAcknowledgedButUnfinishedDispatchHoldsNonIdempotentWork proves the
// ambiguous outcome reaches the hold rule through the real path.
func TestAnAcknowledgedButUnfinishedDispatchHoldsNonIdempotentWork(t *testing.T) {
	deps, rec, _, head := shipHarness(t, DispatchFrame{
		Sequence: 1, ActionID: "a1", Outcome: OutcomeAccepted,
	})

	_, err := Ship(context.Background(), deps, rec, ShipRequest{
		DispatchID: "d1", Head: head, Work: Action{ID: "a1"}, Sensitivity: SensitivityNormal,
	})
	if err == nil {
		t.Fatal("an acknowledged-but-unfinished non-idempotent dispatch was reported as done")
	}
	if !strings.Contains(err.Error(), "held for a decision") {
		t.Errorf("error = %v, want the held-for-a-human wording", err)
	}
}

// TestShipRefusesWhenUnwired proves a half-built dispatch path fails as a
// typed error rather than a nil-pointer panic mid-ship.
func TestShipRefusesWhenUnwired(t *testing.T) {
	rec, _ := enrolledDispatchNode(t)
	_, err := Ship(context.Background(), ShipDeps{}, rec, ShipRequest{
		DispatchID: "d1", Work: Action{ID: "a1"}, Sensitivity: SensitivityNormal,
	})
	if err == nil {
		t.Fatal("an unwired dispatch path shipped")
	}
}
