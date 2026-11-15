package swarm

import (
	"errors"
	"testing"
	"time"
)

// TestStaleBaseDetected_HELM13 verifies the types and errors required by ticket p2-helm-13.
func TestStaleBaseDetected_HELM13(t *testing.T) {
	now := time.Now()
	sr := StaleRun{
		RunName:      "run-123",
		Reason:       "stale-base",
		AttemptCount: 1,
		Timestamp:    now,
	}

	if sr.RunName != "run-123" {
		t.Errorf("expected RunName 'run-123', got %q", sr.RunName)
	}
	if sr.Reason != "stale-base" {
		t.Errorf("expected Reason 'stale-base', got %q", sr.Reason)
	}
	if sr.AttemptCount != 1 {
		t.Errorf("expected AttemptCount 1, got %d", sr.AttemptCount)
	}
	if !sr.Timestamp.Equal(now) {
		t.Errorf("expected Timestamp %v, got %v", now, sr.Timestamp)
	}

	hc := DefaultHealConfig()
	if !hc.DryRun {
		t.Errorf("expected HealConfig DryRun to default to true")
	}
	if hc.CurrentMain != "" {
		t.Errorf("expected default HealConfig CurrentMain to be empty, got %q", hc.CurrentMain)
	}

	if ErrNoCurrentMain == nil {
		t.Errorf("ErrNoCurrentMain must be defined")
	}
	if !errors.Is(ErrNoCurrentMain, ErrNoCurrentMain) {
		t.Errorf("ErrNoCurrentMain must match itself via errors.Is")
	}

	if ErrEmptyRunName == nil {
		t.Errorf("ErrEmptyRunName must be defined")
	}
	if !errors.Is(ErrEmptyRunName, ErrEmptyRunName) {
		t.Errorf("ErrEmptyRunName must match itself via errors.Is")
	}
}

// TestStaleBaseDetected_HELM14 verifies input validation for DetectStaleRuns.
func TestStaleBaseDetected_HELM14(t *testing.T) {
	_, err := DetectStaleRuns(nil, "")
	if !errors.Is(err, ErrNoCurrentMain) {
		t.Errorf("expected ErrNoCurrentMain when currentMain is empty, got %v", err)
	}

	runs := []StaleRun{{RunName: "", Reason: "stale-base"}}
	_, err = DetectStaleRuns(runs, "main")
	if !errors.Is(err, ErrEmptyRunName) {
		t.Errorf("expected ErrEmptyRunName for empty RunName, got %v", err)
	}
}

// TestStaleBaseDetected verifies detection filtering for stale runs.
func TestStaleBaseDetected(t *testing.T) {
	now := time.Now()
	runs := []StaleRun{
		{RunName: "run-1", Reason: "stale-base", Timestamp: now},
		{RunName: "run-2", Reason: "supervisor-refused", Timestamp: now},
		{RunName: "run-3", Reason: "no-file-change", Timestamp: now},
		{RunName: "run-4", Reason: "other-reason", Timestamp: now},
		{RunName: "run-5", Reason: "stale-base", Timestamp: now},
	}

	filtered, err := DetectStaleRuns(runs, "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(filtered) != 4 {
		t.Fatalf("expected 4 filtered runs, got %d", len(filtered))
	}

	names := map[string]bool{}
	for _, r := range filtered {
		names[r.RunName] = true
	}
	if !names["run-1"] || !names["run-2"] || !names["run-3"] || !names["run-5"] {
		t.Errorf("missing expected runs in filtered output")
	}
	if names["run-4"] {
		t.Errorf("run-4 should not be included")
	}
}

// TestHealRenamesNeverDeletes verifies that ArchiveRun produces the correct
// renamed string and never deletes the original run.
func TestHealRenamesNeverDeletes(t *testing.T) {
	fixedTime := time.Date(2026, 11, 16, 10, 30, 45, 0, time.UTC)
	clock := func() time.Time { return fixedTime }

	run := StaleRun{
		RunName: "test-run-abc",
		Reason:  "stale-base",
	}

	renamed, err := ArchiveRun(run, clock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "test-run-abc.stale-20261116T103045Z"
	if renamed != expected {
		t.Errorf("expected renamed string %q, got %q", expected, renamed)
	}

	emptyRun := StaleRun{RunName: ""}
	_, err = ArchiveRun(emptyRun, clock)
	if !errors.Is(err, ErrEmptyRunName) {
		t.Errorf("expected ErrEmptyRunName for empty RunName, got %v", err)
	}
}

// TestTicketsReopenedOnCurrentMain verifies that ReopenOnCurrentMain reopens
// tickets on the current main branch and returns ErrNoCurrentMain when empty.
func TestTicketsReopenedOnCurrentMain(t *testing.T) {
	run := StaleRun{
		RunName:      "run-456",
		Reason:       "stale-base",
		AttemptCount: 2,
		Timestamp:    time.Now(),
	}

	_, err := ReopenOnCurrentMain(run, "")
	if !errors.Is(err, ErrNoCurrentMain) {
		t.Errorf("expected ErrNoCurrentMain for empty currentMain, got %v", err)
	}

	res, err := ReopenOnCurrentMain(run, "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(res.ReopenedTickets) == 0 {
		t.Fatalf("expected ReopenedTickets to be populated, got empty slice")
	}

	found := false
	for _, ticket := range res.ReopenedTickets {
		if ticket == run.RunName {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected ReopenedTickets to contain %q, got %v", run.RunName, res.ReopenedTickets)
	}
}

// TestAttemptCountCarried verifies that ReopenOnCurrentMain carries attempt
// counts into the HealResult and uses ArchiveRun for the renamed run.
func TestAttemptCountCarried(t *testing.T) {
	run := StaleRun{
		RunName:      "run-789",
		Reason:       "stale-base",
		AttemptCount: 7,
		Timestamp:    time.Now(),
	}

	res, err := ReopenOnCurrentMain(run, "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.CarriedAttempts != run.AttemptCount {
		t.Errorf("expected CarriedAttempts %d, got %d", run.AttemptCount, res.CarriedAttempts)
	}

	if res.RenamedTo == "" {
		t.Errorf("expected RenamedTo to be populated")
	}

	// Verify RenamedTo matches ArchiveRun format
	expectedPrefix := run.RunName + ".stale-"
	if len(res.RenamedTo) <= len(expectedPrefix) || res.RenamedTo[:len(expectedPrefix)] != expectedPrefix {
		t.Errorf("expected RenamedTo to start with %q, got %q", expectedPrefix, res.RenamedTo)
	}
}
