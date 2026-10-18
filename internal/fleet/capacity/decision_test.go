// Purpose: the scheduler decision Lease writes in the Bind transaction: the
// TierSelection fields reach the DecisionRecorder port, and a Record error
// rolls the whole lease back.
//
// SPORT: fleet.capacity.lease.

package capacity

import (
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestFleetLeaseRecordsDecision(t *testing.T) {
	f := newFx(t)
	f.seedJob("job-1", provider.SensitivityInternal)
	req := request()
	req.MinQuality, req.ReserveTier0 = QualityMax, true
	l, err := f.fleet.Lease(t.Context(), req, dispatchFor("job-1"), 1)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	if got := len(f.decisions.recorded); got != 1 {
		t.Fatalf("decisions recorded = %d, want exactly one", got)
	}
	d := f.decisionOf(l.DecisionID)
	want := DecisionRecord{ID: l.DecisionID, JobID: "job-1", ExecutionID: l.ExecutionID, TaskClass: conductor.TaskClassCode,
		SelectedTier: TierZero, LaneID: "lane-t0", NodeID: testControllerNode, ScoreAtSelection: 0.9, FallbackLevel: "global",
		JumpRuleFired: true, JumpReasonCode: JumpReasonMinQuality, ReserveTier0: true, DecidedAt: f.clock.t}
	if d != want {
		t.Fatalf("decision = %+v, want %+v", d, want)
	}
	var tier, lane, node, code string
	var jump, reserve bool
	if err := f.db.QueryRow(`SELECT tier, lane, node, jump_fired, jump_code, reserve FROM test_decision WHERE id = ?`, l.DecisionID).
		Scan(&tier, &lane, &node, &jump, &code, &reserve); err != nil {
		t.Fatalf("read the stored decision: %v", err)
	}
	if tier != "tier-0" || lane != "lane-t0" || node != testControllerNode || !jump || code != "min_quality" || !reserve {
		t.Fatalf("stored decision = %s %s %s %v %s %v, want the TierSelection fields", tier, lane, node, jump, code, reserve)
	}
}

func TestFleetLeaseDecisionErrorRollsBackTheLease(t *testing.T) {
	f := newFx(t)
	f.seedJob("job-1", provider.SensitivityInternal)
	cause := errors.New("decision table is read-only")
	f.decisions.err = cause
	_, err := f.fleet.Lease(t.Context(), request(), dispatchFor("job-1"), 1)
	requireSentinel(t, err, ErrReservationFailed, "decision table is read-only")
	if !errors.Is(err, cause) {
		t.Fatalf("err = %v, want the recorder's cause kept in the chain", err)
	}
	if f.count("jobs_execution") != 0 || f.count("test_decision") != 0 {
		t.Fatalf("a failed Record left rows behind: execution %d, decision %d", f.count("jobs_execution"), f.count("test_decision"))
	}
	f.mustState(economics.ReservationRolledBack)
}
