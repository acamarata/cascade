// Package crunch provides status reconciliation and fix ticket generation.
package crunch

import "testing"

// TestComputeStatusCountsReconciles verifies that done and green counts
// reconcile, repeated not_run lines are counted once per pass, and red
// checks yield fix tickets.
func TestComputeStatusCountsReconciles(t *testing.T) {
	results := []CheckResult{
		{Name: "build", State: "done"},
		{Name: "compile", State: "done"},
		{Name: "lint", State: "green"},
		{Name: "test", State: "red", Reason: "failure"},
		{Name: "deploy", State: "not_run"},
		{Name: "deploy", State: "not_run"}, // repeated, should count once
	}

	summary := ComputeStatusCounts(results)

	if summary.DoneCount != 2 {
		t.Errorf("DoneCount = %d, want 2", summary.DoneCount)
	}
	if summary.GreenCount != 1 {
		t.Errorf("GreenCount = %d, want 1", summary.GreenCount)
	}
	if summary.NotRunCount != 1 {
		t.Errorf("NotRunCount = %d, want 1", summary.NotRunCount)
	}
	if summary.DiscrepancyReason == "" {
		t.Error("DiscrepancyReason should not be empty when DoneCount != GreenCount")
	}

	var failed []CheckResult
	for _, r := range results {
		if r.State == "red" {
			failed = append(failed, r)
		}
	}

	tickets := YieldFixTickets(failed)
	if len(tickets) != 1 {
		t.Fatalf("len(tickets) = %d, want 1", len(tickets))
	}
	if tickets[0].Title == "" {
		t.Error("ticket Title should not be empty")
	}
	if tickets[0].Body == "" {
		t.Error("ticket Body should not be empty")
	}
}
