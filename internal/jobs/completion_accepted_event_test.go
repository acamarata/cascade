package jobs

// Purpose: jobs.accepted is published once per accepted job, only after the
// accepted transaction commits (P1-CI-15).

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/events"
)

func acceptedEvents(t *testing.T, bus *events.Bus) []events.Event {
	t.Helper()
	evs, err := bus.Replay(context.Background(), completionAcceptedNamespace, 0)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	return evs
}

func TestJobAcceptedEventPublishedAfterCommit(t *testing.T) {
	t.Run("accepted publishes once after commit", checkAcceptedPublishesOnce)
	t.Run("refused publishes nothing", checkRefusedPublishesNothing)
	t.Run("failed commit publishes nothing", checkFailedCommitPublishesNothing)
}

func checkAcceptedPublishesOnce(t *testing.T) {
	ctx := context.Background()
	cp, store, req, row := attestedCompletion(t, RiskClassNormal, false)
	if err := cp.Transition(ctx, req); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if storedJobState(t, store, req.Job.ID) != JobStateAccepted {
		t.Fatal("state not committed")
	}
	evs := acceptedEvents(t, cp.deps.Bus)
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1", len(evs))
	}
	if evs[0].Kind != EventJobAccepted {
		t.Fatalf("kind = %q, want %q", evs[0].Kind, EventJobAccepted)
	}
	var payload struct {
		JobID            string `json:"job_id"`
		AttestationNonce string `json:"attestation_nonce"`
	}
	if err := json.Unmarshal(evs[0].Payload, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload.JobID != req.Job.ID || payload.AttestationNonce != row.Nonce || row.Nonce == "" {
		t.Fatalf("payload = %+v, want job_id=%s attestation_nonce=%s", payload, req.Job.ID, row.Nonce)
	}
}

func checkRefusedPublishesNothing(t *testing.T) {
	ctx := context.Background()
	cp, _, req, _ := attestedCompletion(t, RiskClassNormal, false)
	cp.deps.Acceptance = acceptanceVerifierFunc(func(context.Context, string) (AcceptanceVerdict, error) {
		return AcceptanceVerdict{Passed: false}, nil
	})
	if err := cp.Transition(ctx, req); err == nil {
		t.Fatal("refused transition succeeded")
	}
	if n := len(acceptedEvents(t, cp.deps.Bus)); n != 0 {
		t.Fatalf("events after refusal = %d, want 0", n)
	}
}

func checkFailedCommitPublishesNothing(t *testing.T) {
	ctx := context.Background()
	cp, store, req, row := attestedCompletion(t, RiskClassNormal, false)
	if _, err := store.db.Exec(`UPDATE attestation_nonce SET expires_at = 0 WHERE nonce = ?`, row.Nonce); err != nil {
		t.Fatal(err)
	}
	if err := cp.Transition(ctx, req); err == nil {
		t.Fatal("expired-nonce commit succeeded")
	}
	if storedJobState(t, store, req.Job.ID) != JobStateReviewing {
		t.Fatal("failed commit changed state")
	}
	if n := len(acceptedEvents(t, cp.deps.Bus)); n != 0 {
		t.Fatalf("events after failed commit = %d, want 0", n)
	}
}
