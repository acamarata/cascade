package background

import (
	"context"
	"slices"
)

// sortGroupNewestFirst returns a sorted copy of group with newest At first.
// Ties preserve original input order (stable sort).
func sortGroupNewestFirst(group []BatchFailureEvent) []BatchFailureEvent {
	sorted := make([]BatchFailureEvent, len(group))
	copy(sorted, group)
	slices.SortStableFunc(sorted, func(a, b BatchFailureEvent) int {
		if a.At.After(b.At) {
			return -1
		}
		if a.At.Before(b.At) {
			return 1
		}
		return 0
	})
	return sorted
}

// handleBatchCore performs matching, sorting, and model calls.
// It assumes validation has already passed.
func handleBatchCore(ctx context.Context, m Model, rules []Rule, group []BatchFailureEvent) (BatchTicket, error) {
	// Sort group newest-first for all calls.
	sorted := sortGroupNewestFirst(group)

	// Collect distinct event kinds.
	kinds := make(map[string]struct{}, len(group))
	for _, ev := range group {
		kinds[ev.Kind] = struct{}{}
	}

	// Process each matching rule exactly once in original order.
	matched := false
	for _, r := range rules {
		if _, ok := kinds[r.Kind]; !ok {
			continue
		}
		matched = true
		// Give each call a fresh copy of the sorted group.
		callGroup := make([]BatchFailureEvent, len(sorted))
		copy(callGroup, sorted)
		if err := m.Call(ctx, r, callGroup); err != nil {
			return BatchTicket{}, ErrBatchModel
		}
	}

	// Only return a ticket if at least one rule matched.
	if !matched {
		return BatchTicket{}, nil
	}

	// Ticket name comes from the newest event (first in sorted).
	return BatchTicket{Name: sorted[0].Name}, nil
}
