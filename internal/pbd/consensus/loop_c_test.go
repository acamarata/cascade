package consensus

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type mockReviewerC struct {
	id     string
	family string
	ce     int
	err    error
	review Review
}

func (m *mockReviewerC) ID() string     { return m.id }
func (m *mockReviewerC) Family() string { return m.family }
func (m *mockReviewerC) CE() int        { return m.ce }
func (m *mockReviewerC) Review(_ context.Context, _ string, _ int) (Review, error) {
	return m.review, m.err
}

func TestConsensusReviewerError(t *testing.T) {
	a := &mockReviewerC{id: "A", family: "f1", ce: 90, review: Review{Approve: true}}
	b := &mockReviewerC{id: "B", family: "f2", ce: 90, err: errors.New("timeout")}

	ladder := [][]Reviewer{{a, b}}
	p := Policy{MinReviewers: 2, MinCE: 85, MaxRounds: 1}

	v, err := Run(context.Background(), "sub", ladder, nil, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Agreed {
		t.Fatalf("expected Agreed=false, got true")
	}
	if v.Rounds != 1 {
		t.Fatalf("expected Rounds=1, got %d", v.Rounds)
	}
	found := false
	for _, f := range v.Open {
		if f.Reviewer == "B" && f.Severity == "blocking" && f.Text == "reviewer B: timeout" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected blocking finding 'reviewer B: timeout', got %+v", v.Open)
	}
}

func TestConsensusFixerError(t *testing.T) {
	a := &mockReviewerC{id: "A", family: "f1", ce: 90, review: Review{Approve: false, Findings: []Finding{{Reviewer: "A", Severity: "blocking", Text: "bad"}}}}
	b := &mockReviewerC{id: "B", family: "f2", ce: 90, review: Review{Approve: true}}

	ladder := [][]Reviewer{{a, b}}
	p := Policy{MinReviewers: 2, MinCE: 85, MaxRounds: 3}

	fix := func(_ context.Context, _ string, _ []Finding) (string, error) {
		return "", errors.New("fix down")
	}

	v, err := Run(context.Background(), "sub", ladder, fix, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Agreed {
		t.Fatalf("expected Agreed=false")
	}
	if v.Rounds != 1 {
		t.Fatalf("expected Rounds=1, got %d", v.Rounds)
	}
	if !strings.Contains(v.Reason, "fix down") {
		t.Fatalf("expected Reason to contain 'fix down', got %q", v.Reason)
	}
}

func TestConsensusNilFixer(t *testing.T) {
	a := &mockReviewerC{id: "A", family: "f1", ce: 90, review: Review{Approve: false, Findings: []Finding{{Reviewer: "A", Severity: "blocking", Text: "bad"}}}}
	b := &mockReviewerC{id: "B", family: "f2", ce: 90, review: Review{Approve: true}}

	ladder := [][]Reviewer{{a, b}}
	p := Policy{MinReviewers: 2, MinCE: 85, MaxRounds: 3}

	v, err := Run(context.Background(), "sub", ladder, nil, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Agreed {
		t.Fatalf("expected Agreed=false")
	}
	if v.Rounds != 1 {
		t.Fatalf("expected Rounds=1, got %d", v.Rounds)
	}
}
