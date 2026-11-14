package crunch

import (
	"errors"
	"strings"
	"time"
)

// GCTicket represents a gate-control ticket.
type GCTicket struct {
	ID    string
	Gated bool
}

// GCQuotaEvidence holds quota usage and capacity data.
type GCQuotaEvidence struct {
	Usage    int64
	Capacity int64
	ResetsAt time.Time
	Stale    bool
}

// GCResetEstimator estimates the next reset time from quota evidence.
type GCResetEstimator func([]GCQuotaEvidence) (time.Time, string, error)

// GCUsageSample records a usage sample with its reset time and source.
type GCUsageSample struct {
	ResetAt time.Time
	Source  string
}

// GCAuth is the populated authentication context for gate control.
type GCAuth struct {
	ID          string
	OwnerFlag   bool
	SummaryOps  []string
	DigestNodes []string
	Model       string
	GateOpen    bool
	Samples     []GCUsageSample
}

// ErrGCAuthFields indicates missing or invalid fields in GCAuth population.
var ErrGCAuthFields = errors.New("CRUN:054-missing-fields")

// ErrGCUsageEstimate indicates a failure during usage estimation.
var ErrGCUsageEstimate = errors.New("CRUN:054-reset-estimate")

// PopulateGCAuth validates inputs and populates a GCAuth value.
func PopulateGCAuth(t GCTicket, evidence []GCQuotaEvidence, estimate GCResetEstimator) (GCAuth, error) {
	if strings.TrimSpace(t.ID) == "" {
		return GCAuth{}, ErrGCAuthFields
	}
	if len(evidence) == 0 {
		return GCAuth{}, ErrGCAuthFields
	}
	if estimate == nil {
		return GCAuth{}, ErrGCAuthFields
	}
	for _, ev := range evidence {
		if ev.Usage < 0 || ev.Capacity < 0 || ev.Usage > ev.Capacity {
			return GCAuth{}, ErrGCAuthFields
		}
	}

	evCopy := make([]GCQuotaEvidence, len(evidence))
	copy(evCopy, evidence)

	resetTime, source, err := estimate(evCopy)
	if err != nil {
		return GCAuth{}, errors.Join(ErrGCUsageEstimate, err)
	}

	return GCAuth{
		ID:          t.ID,
		OwnerFlag:   true,
		SummaryOps:  []string{"build", "verify", "land"},
		DigestNodes: []string{"ticket", "usage"},
		Model:       "local-model",
		GateOpen:    !t.Gated,
		Samples: []GCUsageSample{
			{ResetAt: resetTime, Source: source},
		},
	}, nil
}
