package background

import (
	"context"
	"errors"
	"testing"
	"time"
)

// mockModel records calls made to it.
type mockModel struct {
	calls []mockCall
	err   error
}

type mockCall struct {
	rule  Rule
	group []BatchFailureEvent
}

func (m *mockModel) Call(ctx context.Context, r Rule, group []BatchFailureEvent) error {
	g := make([]BatchFailureEvent, len(group))
	copy(g, group)
	m.calls = append(m.calls, mockCall{rule: r, group: g})
	return m.err
}

// TestHandleBatchOneCallPerRule verifies that exactly one call is made per
// matching rule, containing all events sorted newest-first, and that
// invalid inputs return ErrInvalidBatch without calling the model.
func TestHandleBatchOneCallPerRule(t *testing.T) {
	now := time.Now()
	events := []BatchFailureEvent{
		{Name: "e1", Kind: "k1", At: now.Add(-2 * time.Hour)},
		{Name: "e2", Kind: "k2", At: now.Add(-1 * time.Hour)},
		{Name: "e3", Kind: "k1", At: now},
	}

	t.Run("single matching rule", func(t *testing.T) {
		m := &mockModel{}
		rules := []Rule{{ID: "r1", Kind: "k1"}}
		ticket, err := HandleBatch(context.Background(), m, rules, events)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(m.calls) != 1 {
			t.Fatalf("expected 1 call, got %d", len(m.calls))
		}
		if m.calls[0].rule.ID != "r1" {
			t.Errorf("expected rule r1, got %s", m.calls[0].rule.ID)
		}
		if len(m.calls[0].group) != 3 {
			t.Fatalf("expected 3 events in call, got %d", len(m.calls[0].group))
		}
		if m.calls[0].group[0].Name != "e3" {
			t.Errorf("expected first event e3, got %s", m.calls[0].group[0].Name)
		}
		if m.calls[0].group[1].Name != "e2" {
			t.Errorf("expected second event e2, got %s", m.calls[0].group[1].Name)
		}
		if m.calls[0].group[2].Name != "e1" {
			t.Errorf("expected third event e1, got %s", m.calls[0].group[2].Name)
		}
		if ticket.Name != "e3" {
			t.Errorf("expected ticket name e3, got %s", ticket.Name)
		}
	})

	t.Run("multiple matching rules", func(t *testing.T) {
		m := &mockModel{}
		rules := []Rule{
			{ID: "r1", Kind: "k1"},
			{ID: "r2", Kind: "k2"},
			{ID: "r3", Kind: "k3"},
		}
		_, err := HandleBatch(context.Background(), m, rules, events)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(m.calls) != 2 {
			t.Fatalf("expected 2 calls, got %d", len(m.calls))
		}
		if m.calls[0].rule.ID != "r1" {
			t.Errorf("expected first call rule r1, got %s", m.calls[0].rule.ID)
		}
		if m.calls[1].rule.ID != "r2" {
			t.Errorf("expected second call rule r2, got %s", m.calls[1].rule.ID)
		}
	})

	t.Run("model error", func(t *testing.T) {
		m := &mockModel{err: errors.New("fail")}
		rules := []Rule{{ID: "r1", Kind: "k1"}}
		_, err := HandleBatch(context.Background(), m, rules, events)
		if !errors.Is(err, ErrBatchModel) {
			t.Errorf("expected ErrBatchModel, got %v", err)
		}
	})

	t.Run("invalid batch", func(t *testing.T) {
		m := &mockModel{}
		badEvents := []BatchFailureEvent{
			{Name: "e1", Kind: "k1", At: now},
			{Name: "e1", Kind: "k2", At: now},
		}
		rules := []Rule{{ID: "r1", Kind: "k1"}}
		_, err := HandleBatch(context.Background(), m, rules, badEvents)
		if !errors.Is(err, ErrInvalidBatch) {
			t.Errorf("expected ErrInvalidBatch, got %v", err)
		}
		if len(m.calls) != 0 {
			t.Errorf("expected no calls for invalid batch, got %d", len(m.calls))
		}
	})

	t.Run("input slices unchanged", func(t *testing.T) {
		m := &mockModel{}
		rules := []Rule{{ID: "r1", Kind: "k1"}}
		origEvents := make([]BatchFailureEvent, len(events))
		copy(origEvents, events)
		_, _ = HandleBatch(context.Background(), m, rules, events)
		for i := range events {
			if events[i] != origEvents[i] {
				t.Errorf("input events mutated at index %d", i)
			}
		}
	})
}

// TestHandleBatchNewestEventLeads verifies that the newest event provides
// the BatchTicket name even if its Kind does not match any rule, and that
// ties in timestamps retain the original input order.
func TestHandleBatchNewestEventLeads(t *testing.T) {
	now := time.Now()

	t.Run("newest event unmatched kind", func(t *testing.T) {
		events := []BatchFailureEvent{
			{Name: "ev-old", Kind: "match", At: now.Add(-time.Hour)},
			{Name: "ev-new", Kind: "unmatched", At: now},
		}
		m := &mockModel{}
		rules := []Rule{{ID: "r1", Kind: "match"}}
		ticket, err := HandleBatch(context.Background(), m, rules, events)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ticket.Name != "ev-new" {
			t.Errorf("expected ticket name ev-new, got %s", ticket.Name)
		}
		if len(m.calls) != 1 {
			t.Fatalf("expected 1 call, got %d", len(m.calls))
		}
		if m.calls[0].group[0].Name != "ev-new" {
			t.Errorf("expected sorted group to lead with ev-new, got %s", m.calls[0].group[0].Name)
		}
	})

	t.Run("equal timestamps retain original order", func(t *testing.T) {
		events := []BatchFailureEvent{
			{Name: "a", Kind: "k", At: now},
			{Name: "b", Kind: "k", At: now},
			{Name: "c", Kind: "k", At: now},
		}
		m := &mockModel{}
		rules := []Rule{{ID: "r1", Kind: "k"}}
		_, err := HandleBatch(context.Background(), m, rules, events)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(m.calls) != 1 {
			t.Fatalf("expected 1 call, got %d", len(m.calls))
		}
		if m.calls[0].group[0].Name != "a" || m.calls[0].group[1].Name != "b" || m.calls[0].group[2].Name != "c" {
			t.Errorf("stable sort failed: got %v", m.calls[0].group)
		}
	})

	t.Run("zero timestamps are valid", func(t *testing.T) {
		events := []BatchFailureEvent{
			{Name: "z1", Kind: "k"},
			{Name: "z2", Kind: "k"},
		}
		m := &mockModel{}
		rules := []Rule{{ID: "r1", Kind: "k"}}
		ticket, err := HandleBatch(context.Background(), m, rules, events)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ticket.Name != "z1" {
			t.Errorf("expected ticket name z1 for zero times, got %s", ticket.Name)
		}
	})
}
