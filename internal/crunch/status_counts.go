// Package crunch provides status reconciliation and fix ticket generation.
package crunch

import "fmt"

// CheckResult represents the result of a single check.
type CheckResult struct {
	// Name is the name of the check.
	Name string
	// State is one of: done, green, red, not_run.
	State string
	// Reason provides additional context for the state.
	Reason string
}

// StatusSummary holds the aggregated counts of check states.
type StatusSummary struct {
	// DoneCount is the number of checks with state "done".
	DoneCount int
	// GreenCount is the number of checks with state "green".
	GreenCount int
	// NotRunCount is the number of unique checks with state "not_run".
	NotRunCount int
	// DiscrepancyReason explains why DoneCount differs from GreenCount.
	DiscrepancyReason string
}

// ComputeStatusCounts tallies the states in results and returns a summary.
// Repeated not_run lines are counted once per pass (unique by Name).
// If DoneCount != GreenCount, DiscrepancyReason is set to explain the difference.
func ComputeStatusCounts(results []CheckResult) StatusSummary {
	var s StatusSummary
	notRunSeen := make(map[string]bool)

	for _, r := range results {
		switch r.State {
		case "done":
			s.DoneCount++
		case "green":
			s.GreenCount++
		case "not_run":
			if !notRunSeen[r.Name] {
				notRunSeen[r.Name] = true
				s.NotRunCount++
			}
		}
	}

	if s.DoneCount != s.GreenCount {
		s.DiscrepancyReason = fmt.Sprintf("DoneCount (%d) != GreenCount (%d): some completed checks did not turn green", s.DoneCount, s.GreenCount)
	}

	return s
}
