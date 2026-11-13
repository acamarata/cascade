package docket

import (
	"errors"
	"testing"
	"time"
)

// TestRollupProducesOneSummaryPerTicket verifies that RollupEvents groups events
// by TicketID, counts all kinds, and tracks first/last timestamps correctly.
func TestRollupProducesOneSummaryPerTicket(t *testing.T) {
	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

	// Three unique T1 events across different kinds
	events := []RetentionEvent{
		{TicketID: "T1", At: now.Add(-2 * time.Hour), Kind: "created"},
		{TicketID: "T1", At: now.Add(-1 * time.Hour), Kind: "updated"},
		{TicketID: "T1", At: now, Kind: "closed"},
		{TicketID: "T2", At: now.Add(-30 * time.Minute), Kind: "created"},
	}

	summaries, err := RollupEvents(events)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(summaries) != 2 {
		t.Fatalf("expected 2 summaries, got %d", len(summaries))
	}

	// First summary should be T1
	if summaries[0].TicketID != "T1" {
		t.Errorf("expected first summary TicketID T1, got %s", summaries[0].TicketID)
	}
	if summaries[0].Count != 3 {
		t.Errorf("expected T1 count 3, got %d", summaries[0].Count)
	}
	if !summaries[0].FirstAt.Equal(now.Add(-2 * time.Hour)) {
		t.Errorf("expected T1 FirstAt %v, got %v", now.Add(-2*time.Hour), summaries[0].FirstAt)
	}
	if !summaries[0].LastAt.Equal(now) {
		t.Errorf("expected T1 LastAt %v, got %v", now, summaries[0].LastAt)
	}

	// Second summary should be T2
	if summaries[1].TicketID != "T2" {
		t.Errorf("expected second summary TicketID T2, got %s", summaries[1].TicketID)
	}
	if summaries[1].Count != 1 {
		t.Errorf("expected T2 count 1, got %d", summaries[1].Count)
	}

	// Test duplicate detection
	duplicateEvents := []RetentionEvent{
		{TicketID: "T1", At: now, Kind: "created"},
		{TicketID: "T1", At: now, Kind: "created"},
	}
	_, err = RollupEvents(duplicateEvents)
	if !errors.Is(err, ErrEventRetention) {
		t.Errorf("expected ErrEventRetention for duplicates, got %v", err)
	}

	// Test empty ID validation
	emptyIDEvents := []RetentionEvent{
		{TicketID: "", At: now, Kind: "created"},
	}
	_, err = RollupEvents(emptyIDEvents)
	if !errors.Is(err, ErrEventRetention) {
		t.Errorf("expected ErrEventRetention for empty ID, got %v", err)
	}

	// Verify input is not mutated
	originalEvents := []RetentionEvent{
		{TicketID: "T1", At: now, Kind: "created"},
	}
	eventsCopy := make([]RetentionEvent, len(originalEvents))
	copy(eventsCopy, originalEvents)
	_, _ = RollupEvents(originalEvents)
	for i := range originalEvents {
		if originalEvents[i] != eventsCopy[i] {
			t.Errorf("input was mutated at index %d", i)
		}
	}
}

// TestRollupEmptyEventsReturnsNil verifies that both nil and empty inputs
// produce nil output and nil error.
func TestRollupEmptyEventsReturnsNil(t *testing.T) {
	// Test nil input
	summaries, err := RollupEvents(nil)
	if summaries != nil {
		t.Errorf("expected nil summaries for nil input, got %v", summaries)
	}
	if err != nil {
		t.Errorf("expected nil error for nil input, got %v", err)
	}

	// Test empty slice input
	summaries, err = RollupEvents([]RetentionEvent{})
	if summaries != nil {
		t.Errorf("expected nil summaries for empty input, got %v", summaries)
	}
	if err != nil {
		t.Errorf("expected nil error for empty input, got %v", err)
	}

	// Test trim with valid now and cutoff
	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	cutoff := 90 * 24 * time.Hour

	result, err := TrimEvents(nil, now, cutoff)
	if result != nil {
		t.Errorf("expected nil result for nil input to TrimEvents, got %v", result)
	}
	if err != nil {
		t.Errorf("expected nil error for nil input to TrimEvents, got %v", err)
	}

	result, err = TrimEvents([]RetentionEvent{}, now, cutoff)
	if result != nil {
		t.Errorf("expected nil result for empty input to TrimEvents, got %v", result)
	}
	if err != nil {
		t.Errorf("expected nil error for empty input to TrimEvents, got %v", err)
	}
}
