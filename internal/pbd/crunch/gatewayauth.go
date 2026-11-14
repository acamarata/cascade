package crunch

import (
	"errors"
	"strings"
	"time"
)

// GatewayTicket represents a gate-control ticket.
type GatewayTicket struct {
	ID    string
	Gated bool
}

// GatewayQuotaEvidence holds quota usage and capacity data.
type GatewayQuotaEvidence struct {
	Usage    int64
	Capacity int64
	ResetsAt time.Time
	Stale    bool
}

// GatewayResetEstimator estimates the next reset time from quota evidence.
type GatewayResetEstimator func([]GatewayQuotaEvidence) (time.Time, string, error)

// GatewayUsageSample records a usage sample with its reset time and source.
type GatewayUsageSample struct {
	ResetAt time.Time
	Source  string
}

// GatewayAuth is the populated authentication context for gate control.
type GatewayAuth struct {
	ID          string
	OwnerFlag   bool
	SummaryOps  []string
	DigestNodes []string
	Model       string
	GateOpen    bool
	Samples     []GatewayUsageSample
}

// ErrGatewayAuthFields indicates missing or invalid fields in GatewayAuth population.
var ErrGatewayAuthFields = errors.New("CRUN:054-missing-fields")

// ErrGatewayUsageEstimate indicates a failure during usage estimation.
var ErrGatewayUsageEstimate = errors.New("CRUN:054-reset-estimate")

// PopulateGatewayAuth validates inputs and populates a GatewayAuth value.
func PopulateGatewayAuth(t GatewayTicket, evidence []GatewayQuotaEvidence, estimate GatewayResetEstimator) (GatewayAuth, error) {
	if strings.TrimSpace(t.ID) == "" {
		return GatewayAuth{}, ErrGatewayAuthFields
	}
	if len(evidence) == 0 {
		return GatewayAuth{}, ErrGatewayAuthFields
	}
	if estimate == nil {
		return GatewayAuth{}, ErrGatewayAuthFields
	}
	for _, ev := range evidence {
		if ev.Usage < 0 || ev.Capacity < 0 || ev.Usage > ev.Capacity {
			return GatewayAuth{}, ErrGatewayAuthFields
		}
	}

	evCopy := make([]GatewayQuotaEvidence, len(evidence))
	copy(evCopy, evidence)

	resetTime, source, err := estimate(evCopy)
	if err != nil {
		return GatewayAuth{}, errors.Join(ErrGatewayUsageEstimate, err)
	}

	return GatewayAuth{
		ID:          t.ID,
		OwnerFlag:   true,
		SummaryOps:  []string{"build", "verify", "land"},
		DigestNodes: []string{"ticket", "usage"},
		Model:       "local-model",
		GateOpen:    !t.Gated,
		Samples: []GatewayUsageSample{
			{ResetAt: resetTime, Source: source},
		},
	}, nil
}
