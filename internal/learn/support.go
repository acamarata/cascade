package learn

// Purpose: validate proposal support without counting its beneficiary.
// Inputs: structured outcome rows and the beneficiary's complete lane id.
// Outputs: supporting rows or ErrInsufficientSupport.
// Constraints: fixed thresholds and window; no caller-owned slice is changed.
// SPORT: internal.learn.ValidateSupport/ADDED (P1-LRN-02a).

import (
	"errors"
	"time"
)

// ErrInsufficientSupport means either fixed support threshold was not met.
var ErrInsufficientSupport = errors.New("learn: insufficient independent proposal support")

// OutcomeRow is a structured telemetry outcome with its associated findings.
type OutcomeRow struct {
	OutcomeID                                      int64
	JobID, RepoID, Component, TaskClass, RiskClass string
	Lane                                           LaneIdentity
	Findings                                       []Finding
	CompletedAt                                    time.Time
}

// ValidateSupport removes every beneficiary row before computing support.
// The inclusive Window ends at the latest remaining completion, allowing
// callers to validate historical windows without consulting a process clock.
// Distinct sources are complete (tier, node) lane identities, not job ids.
// A row without a completion time or a complete lane cannot supply support.
func ValidateSupport(beneficiary LaneIdentity, rows []OutcomeRow) ([]OutcomeRow, error) {
	var end time.Time
	eligible := make([]OutcomeRow, 0, len(rows))
	for _, row := range rows {
		if row.Lane == beneficiary || row.CompletedAt.IsZero() || row.Lane.LaneTier == "" || row.Lane.NodeID == "" {
			continue
		}
		eligible = append(eligible, row)
		if row.CompletedAt.After(end) {
			end = row.CompletedAt
		}
	}
	start := end.Add(-Window)
	sources := make(map[LaneIdentity]struct{})
	support := make([]OutcomeRow, 0, len(eligible))
	for _, row := range eligible {
		if row.CompletedAt.Before(start) {
			continue
		}
		support = append(support, row)
		sources[row.Lane] = struct{}{}
	}
	if len(support) < MinOccurrences || len(sources) < MinDistinctSources {
		return nil, ErrInsufficientSupport
	}
	return support, nil
}
