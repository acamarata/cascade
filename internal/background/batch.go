package background

import (
	"context"
	"errors"
	"time"
)

// ErrInvalidBatch is returned when input validation fails.
var ErrInvalidBatch = errors.New("invalid batch")

// ErrBatchModel is the sentinel error for model call failures.
var ErrBatchModel = errors.New("D0292")

// BatchFailureEvent represents a failure event in a batch.
type BatchFailureEvent struct {
	Name  string
	Stage string
	Kind  string
	At    time.Time
}

// Rule defines a matching rule for batch processing.
type Rule struct {
	ID   string
	Kind string
}

// BatchTicket is the result of a successful batch operation.
type BatchTicket struct {
	Name string
}

// Model is the interface used to process matching rules against events.
type Model interface {
	Call(ctx context.Context, r Rule, group []BatchFailureEvent) error
}

// HandleBatch validates inputs, matches rules to events by Kind, and calls
// the model once per matching rule. It returns a zero BatchTicket and nil
// error when no rules match or inputs are empty. Validation errors return
// ErrInvalidBatch without calling the model.
func HandleBatch(ctx context.Context, m Model, rules []Rule, group []BatchFailureEvent) (BatchTicket, error) {
	if ctx == nil {
		return BatchTicket{}, ErrInvalidBatch
	}
	if m == nil {
		return BatchTicket{}, ErrInvalidBatch
	}

	// Validate events: Name and Kind nonblank, names unique.
	seenNames := make(map[string]struct{}, len(group))
	for _, ev := range group {
		if ev.Name == "" || ev.Kind == "" {
			return BatchTicket{}, ErrInvalidBatch
		}
		if _, exists := seenNames[ev.Name]; exists {
			return BatchTicket{}, ErrInvalidBatch
		}
		seenNames[ev.Name] = struct{}{}
	}

	// Validate rules: ID and Kind nonblank, IDs unique.
	seenIDs := make(map[string]struct{}, len(rules))
	for _, r := range rules {
		if r.ID == "" || r.Kind == "" {
			return BatchTicket{}, ErrInvalidBatch
		}
		if _, exists := seenIDs[r.ID]; exists {
			return BatchTicket{}, ErrInvalidBatch
		}
		seenIDs[r.ID] = struct{}{}
	}

	// No-ops: nil/empty group or rules.
	if len(group) == 0 || len(rules) == 0 {
		return BatchTicket{}, nil
	}

	// Collect distinct event kinds for matching.
	kinds := make(map[string]struct{}, len(group))
	for _, ev := range group {
		kinds[ev.Kind] = struct{}{}
	}

	// Process each matching rule exactly once.
	for _, r := range rules {
		if _, ok := kinds[r.Kind]; !ok {
			continue
		}
		if err := m.Call(ctx, r, group); err != nil {
			return BatchTicket{}, ErrBatchModel
		}
	}

	return BatchTicket{}, nil
}
