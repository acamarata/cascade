package crunch

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestMissingFieldsFailsClosed(t *testing.T) {
	calls := 0
	estimator := func([]GCQuotaEvidence) (time.Time, string, error) {
		calls++
		return time.Time{}, "", nil
	}
	validEvidence := []GCQuotaEvidence{
		{Usage: 1, Capacity: 10, ResetsAt: time.Now(), Stale: false},
	}

	tests := []struct {
		name     string
		ticket   GCTicket
		evidence []GCQuotaEvidence
		estimate GCResetEstimator
	}{
		{
			name:     "empty ID",
			ticket:   GCTicket{ID: "", Gated: false},
			evidence: validEvidence,
			estimate: estimator,
		},
		{
			name:     "blank ID",
			ticket:   GCTicket{ID: "   ", Gated: false},
			evidence: validEvidence,
			estimate: estimator,
		},
		{
			name:     "nil evidence",
			ticket:   GCTicket{ID: "valid", Gated: false},
			evidence: nil,
			estimate: estimator,
		},
		{
			name:     "empty evidence",
			ticket:   GCTicket{ID: "valid", Gated: false},
			evidence: []GCQuotaEvidence{},
			estimate: estimator,
		},
		{
			name:     "nil estimator",
			ticket:   GCTicket{ID: "valid", Gated: false},
			evidence: validEvidence,
			estimate: nil,
		},
		{
			name:   "invalid evidence usage < 0",
			ticket: GCTicket{ID: "valid", Gated: false},
			evidence: []GCQuotaEvidence{
				{Usage: -1, Capacity: 10},
			},
			estimate: estimator,
		},
		{
			name:   "invalid evidence capacity < 0",
			ticket: GCTicket{ID: "valid", Gated: false},
			evidence: []GCQuotaEvidence{
				{Usage: 1, Capacity: -1},
			},
			estimate: estimator,
		},
		{
			name:   "invalid evidence usage > capacity",
			ticket: GCTicket{ID: "valid", Gated: false},
			evidence: []GCQuotaEvidence{
				{Usage: 11, Capacity: 10},
			},
			estimate: estimator,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prevCalls := calls
			auth, err := PopulateGCAuth(tt.ticket, tt.evidence, tt.estimate)
			if !reflect.DeepEqual(auth, GCAuth{}) {
				t.Errorf("expected zero GCAuth, got %+v", auth)
			}
			if !errors.Is(err, ErrGCAuthFields) {
				t.Errorf("expected ErrGCAuthFields, got %v", err)
			}
			if err != nil && err.Error() != "CRUN:054-missing-fields" {
				t.Errorf("expected error message CRUN:054-missing-fields, got %s", err.Error())
			}
			if calls != prevCalls {
				t.Errorf("estimator should not be called, but was called %d times", calls-prevCalls)
			}
		})
	}
}
