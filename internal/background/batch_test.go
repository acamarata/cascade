package background

import (
	"context"
	"testing"
)

// fakeModel records calls for testing.
type fakeModel struct {
	calls int
}

// Call satisfies the Model interface.
func (f *fakeModel) Call(ctx context.Context, r Rule, group []BatchFailureEvent) error {
	f.calls++
	return nil
}

// TestHandleBatchNoMatchingRules verifies that zero matching rules returns
// BatchTicket{}, nil and makes no model calls. Nil/empty groups or rules are
// no-ops, and no wildcard or case folding is applied.
func TestHandleBatchNoMatchingRules(t *testing.T) {
	ctx := context.Background()

	t.Run("nil group and nil rules", func(t *testing.T) {
		m := &fakeModel{}
		got, err := HandleBatch(ctx, m, nil, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != (BatchTicket{}) {
			t.Fatalf("expected zero BatchTicket, got %+v", got)
		}
		if m.calls != 0 {
			t.Fatalf("expected 0 calls, got %d", m.calls)
		}
	})

	t.Run("empty group and empty rules", func(t *testing.T) {
		m := &fakeModel{}
		got, err := HandleBatch(ctx, m, []Rule{}, []BatchFailureEvent{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != (BatchTicket{}) {
			t.Fatalf("expected zero BatchTicket, got %+v", got)
		}
		if m.calls != 0 {
			t.Fatalf("expected 0 calls, got %d", m.calls)
		}
	})

	t.Run("no kind match", func(t *testing.T) {
		m := &fakeModel{}
		rules := []Rule{{ID: "r1", Kind: "alpha"}}
		group := []BatchFailureEvent{{Name: "e1", Kind: "beta"}}
		got, err := HandleBatch(ctx, m, rules, group)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != (BatchTicket{}) {
			t.Fatalf("expected zero BatchTicket, got %+v", got)
		}
		if m.calls != 0 {
			t.Fatalf("expected 0 calls, got %d", m.calls)
		}
	})

	t.Run("case sensitive no fold", func(t *testing.T) {
		m := &fakeModel{}
		rules := []Rule{{ID: "r1", Kind: "Alpha"}}
		group := []BatchFailureEvent{{Name: "e1", Kind: "alpha"}}
		got, err := HandleBatch(ctx, m, rules, group)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != (BatchTicket{}) {
			t.Fatalf("expected zero BatchTicket, got %+v", got)
		}
		if m.calls != 0 {
			t.Fatalf("expected 0 calls, got %d", m.calls)
		}
	})

	t.Run("no wildcard", func(t *testing.T) {
		m := &fakeModel{}
		rules := []Rule{{ID: "r1", Kind: "*"}}
		group := []BatchFailureEvent{{Name: "e1", Kind: "alpha"}}
		got, err := HandleBatch(ctx, m, rules, group)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != (BatchTicket{}) {
			t.Fatalf("expected zero BatchTicket, got %+v", got)
		}
		if m.calls != 0 {
			t.Fatalf("expected 0 calls, got %d", m.calls)
		}
	})
}
