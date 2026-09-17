package nodes

// Purpose (this file): the last few branches on the ship and stream legs
//   that only a failing collaborator reaches — a push that fails, a fetch
//   that fails after the node already reported success, a record whose
//   payload will not encode, a credential plan whose grant is unbindable,
//   and the zero-value attempt register.
// WHY: each is a path a running controller takes on a bad day and no test
//   took at all. The fetch one in particular is the interesting case: the
//   node said "succeeded" and the results cannot be read, which must not be
//   reported as success.
// SPORT: internal/nodes tests (ADD) — P1-E17-W4-S37-T2.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestTheZeroValueAttemptRegisterStillFences pins the contract that makes
// every embedded register safe. A register whose map is nil must mint
// attempt 1, not panic — a panic here would take down the controller on the
// first dispatch of a process that built one by value.
func TestTheZeroValueAttemptRegisterStillFences(t *testing.T) {
	var reg AttemptRegister
	if got := reg.Next("d1"); got != 1 {
		t.Fatalf("first attempt = %d, want 1", got)
	}
	if got := reg.Next("d1"); got != 2 {
		t.Errorf("second attempt = %d, want 2; the register is not counting", got)
	}
}

// TestAnUnbindableScopedPlanIsRefusedNotShipped covers the credential
// plan's failure branch. A scoped lane that cannot mint a bound grant must
// produce NO plan: returning a plan with a nil grant would ship a lane with
// no credential and fail much later, on the node.
func TestAnUnbindableScopedPlanIsRefusedNotShipped(t *testing.T) {
	plan, err := PlanCredentials(CredentialScoped, "", "node-a", "github",
		"vault/github", []string{"read"}, credNow, time.Minute)
	if err == nil {
		t.Fatalf("a scoped plan with no job id was accepted: %+v", plan)
	}
	if plan.Grant != nil {
		t.Error("a refused plan still carried a grant")
	}
}

// TestAPushFailureStopsTheDispatchBeforeTheNodeIsCalled is the ordering
// rule. The node must never be asked to claim work whose branch is not
// there — it would check out nothing and report a failure the controller
// would then have to interpret.
func TestAPushFailureStopsTheDispatchBeforeTheNodeIsCalled(t *testing.T) {
	deps, rec, caller, head := shipHarness(t, DispatchFrame{})
	deps.Remote = strings.TrimSuffix(deps.Remote, "/") + "-does-not-exist"

	_, err := Ship(context.Background(), deps, rec, ShipRequest{
		DispatchID:  "d1",
		Head:        head,
		Work:        Action{ID: "a1"},
		Sensitivity: SensitivityNormal,
	})
	if err == nil {
		t.Fatal("a dispatch whose branch could not be pushed reported success")
	}
	if caller.calls != 0 {
		t.Errorf("the node was called %d time(s) for work that was never pushed", caller.calls)
	}
}

// TestARecordThatWillNotEncodeIsRefusedBeforeTheStore covers the stamping
// leg. The fencing wrapper is built by re-encoding the node's payload, so a
// payload that is not valid JSON fails there — and it must fail, rather
// than reaching the store as a half-built entry.
func TestARecordThatWillNotEncodeIsRefusedBeforeTheStore(t *testing.T) {
	deps, sink, attempt := streamHarness(t)
	rec := goodRecord(attempt)
	rec.Payload = json.RawMessage(`{"unterminated":`)

	if err := StreamJournalRecord(context.Background(), deps, rec); err == nil {
		t.Fatal("a record whose payload is not JSON was appended")
	}
	if len(sink.payloads) != 0 {
		t.Errorf("the store holds %d entr(y/ies) for a record that never encoded", len(sink.payloads))
	}
}

// TestAReservationFailureStopsTheNodeLeg separates the two ways
// ReserveAction fails. A duplicate is reported as a refusal — the work
// already happened. Anything else (an unwritable log) must abort: a node
// that could not record the reservation and ran anyway has turned dedup
// off for that action.
func TestAReservationFailureStopsTheNodeLeg(t *testing.T) {
	deps, _ := executeHarness(t)
	deps.Actions = NewFileActionLog(unwritableDir(t))

	if _, err := ExecuteDispatch(context.Background(), deps, goodExecute()); err == nil {
		t.Fatal("the node leg ran an action it could not reserve")
	}
}

// TestACompletionFailureIsSurfaced covers the other end of the same rule.
// The frame is signed by then, so it is tempting to return it and let the
// completion record fail quietly — but a reserved action with no terminal
// state is indistinguishable from one still running.
func TestACompletionFailureIsSurfaced(t *testing.T) {
	deps, _ := executeHarness(t)
	deps.Actions = refusingCompleteLog{ActionLog: deps.Actions}

	if _, err := ExecuteDispatch(context.Background(), deps, goodExecute()); err == nil {
		t.Fatal("a completion that could not be recorded was reported as a clean dispatch")
	}
}

// refusingCompleteLog reserves normally and refuses every completion.
type refusingCompleteLog struct{ ActionLog }

func (refusingCompleteLog) Complete(context.Context, string, DispatchOutcome) error {
	return errors.New("the action log rejected the outcome")
}

// TestAFetchFailureAfterASuccessfulRunIsNotSuccess is the sharpest of these.
// The node reported "succeeded" and the controller cannot read what came
// back. Reporting success would hand a caller an empty result commit for
// work that really ran.
func TestAFetchFailureAfterASuccessfulRunIsNotSuccess(t *testing.T) {
	deps, rec, caller, head := shipHarness(t, DispatchFrame{
		Sequence: 1, ActionID: "a1", Outcome: OutcomeSucceeded,
	})
	deps.Git = newDispatchGit(t.TempDir(), testGitRunner{}) // a root that is not a repository

	out, err := Ship(context.Background(), deps, rec, ShipRequest{
		DispatchID:  "d1",
		Head:        head,
		Work:        Action{ID: "a1"},
		Sensitivity: SensitivityNormal,
	})
	if err == nil {
		t.Fatalf("a dispatch whose results could not be read reported success: %+v", out)
	}
	if out.Commit != "" {
		t.Errorf("a failed dispatch returned commit %q", out.Commit)
	}
	_ = caller
}
