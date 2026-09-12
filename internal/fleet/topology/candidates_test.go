package topology

import (
	"context"
	"testing"
)

// TestCandidatesSkipsRetiredLanes asserts a retired lane (R-21.124) never
// reaches the eligible set, even when its domain and account are
// otherwise fully eligible.
func TestCandidatesSkipsRetiredLanes(t *testing.T) {
	acct, dom, lane, cred := baseTopology()
	retiredAt := newTestClock().Now()
	lane.RetiredAt = &retiredAt
	c := NewChooser(newTestClock(), 1, alwaysAllow, nil)
	c.SetTopology([]Account{acct}, []QuotaDomain{dom}, []Lane{lane}, []Credential{cred})

	_, err := c.Candidates(context.Background(), ChooseRequest{ModelID: "model-x"})
	if err == nil {
		t.Fatal("expected no eligible candidates for a fully-retired topology")
	}
}

// TestCandidatesRefusesUnreservableEstimate asserts a request whose
// estimate exceeds a bucket's known Limit is refused (can_reserve),
// exercising bucket.go's Reservable through the eligibility predicate.
func TestCandidatesRefusesUnreservableEstimate(t *testing.T) {
	acct, dom, lane, cred := baseTopology()
	c := NewChooser(newTestClock(), 1, alwaysAllow, nil)
	c.SetTopology([]Account{acct}, []QuotaDomain{dom}, []Lane{lane}, []Credential{cred})

	_, err := c.Candidates(context.Background(), ChooseRequest{
		ModelID:   "model-x",
		Estimates: map[string]int64{DimensionRPM: 100000}, // exceeds the 1000 limit
	})
	if err == nil {
		t.Fatal("expected an over-estimate request to be refused")
	}
}

// TestCandidatesRefusesUnknownDimensionEstimate asserts a request naming a
// dimension the domain has no bucket for is refused, never silently
// ignored.
func TestCandidatesRefusesUnknownDimensionEstimate(t *testing.T) {
	acct, dom, lane, cred := baseTopology()
	c := NewChooser(newTestClock(), 1, alwaysAllow, nil)
	c.SetTopology([]Account{acct}, []QuotaDomain{dom}, []Lane{lane}, []Credential{cred})

	_, err := c.Candidates(context.Background(), ChooseRequest{
		ModelID:   "model-x",
		Estimates: map[string]int64{DimensionTPM: 1},
	})
	if err == nil {
		t.Fatal("expected a request naming an unmodeled dimension to be refused")
	}
}
