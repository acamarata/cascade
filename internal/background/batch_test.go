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
func (f *fakeModel) Call(_ context.Context, _ Rule, _ []BatchFailureEvent) error {
	f.calls++
	return nil
}

// TestHandleBatchNoMatchingRules verifies that zero matching rules returns
// BatchTicket{}, nil and makes no model calls. Nil/empty groups or rules are
// no-ops, and no wildcard or case folding is applied.
func TestHandleBatchNoMatchingRules(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name  string
		rules []Rule
		group []BatchFailureEvent
	}{
		{"nil group and nil rules", nil, nil},
		{"empty group and empty rules", []Rule{}, []BatchFailureEvent{}},
		{
			"no kind match",
			[]Rule{{ID: "r1", Kind: "alpha"}},
			[]BatchFailureEvent{{Name: "e1", Kind: "beta"}},
		},
		{
			"case sensitive no fold",
			[]Rule{{ID: "r1", Kind: "Alpha"}},
			[]BatchFailureEvent{{Name: "e1", Kind: "alpha"}},
		},
		{
			"no wildcard",
			[]Rule{{ID: "r1", Kind: "*"}},
			[]BatchFailureEvent{{Name: "e1", Kind: "alpha"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &fakeModel{}
			got, err := HandleBatch(ctx, m, tc.rules, tc.group)
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
}
