package crunch

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestMissingFieldsFailsClosed(t *testing.T) {
	calls := 0
	estimator := func([]GatewayQuotaEvidence) (time.Time, string, error) {
		calls++
		return time.Time{}, "", nil
	}
	validEvidence := []GatewayQuotaEvidence{
		{Usage: 1, Capacity: 10, ResetsAt: time.Now(), Stale: false},
	}

	tests := []struct {
		name     string
		ticket   GatewayTicket
		evidence []GatewayQuotaEvidence
		estimate GatewayResetEstimator
	}{
		{"empty ID", GatewayTicket{ID: ""}, validEvidence, estimator},
		{"blank ID", GatewayTicket{ID: "   "}, validEvidence, estimator},
		{"nil evidence", GatewayTicket{ID: "valid"}, nil, estimator},
		{"empty evidence", GatewayTicket{ID: "valid"}, []GatewayQuotaEvidence{}, estimator},
		{"nil estimator", GatewayTicket{ID: "valid"}, validEvidence, nil},
		{"invalid evidence usage < 0", GatewayTicket{ID: "valid"}, []GatewayQuotaEvidence{{Usage: -1, Capacity: 10}}, estimator},
		{"invalid evidence capacity < 0", GatewayTicket{ID: "valid"}, []GatewayQuotaEvidence{{Usage: 1, Capacity: -1}}, estimator},
		{"invalid evidence usage > capacity", GatewayTicket{ID: "valid"}, []GatewayQuotaEvidence{{Usage: 11, Capacity: 10}}, estimator},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prevCalls := calls
			auth, err := PopulateGatewayAuth(tt.ticket, tt.evidence, tt.estimate)
			if !reflect.DeepEqual(auth, GatewayAuth{}) {
				t.Errorf("expected zero GatewayAuth, got %+v", auth)
			}
			if !errors.Is(err, ErrGatewayAuthFields) {
				t.Errorf("expected ErrGatewayAuthFields, got %v", err)
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
