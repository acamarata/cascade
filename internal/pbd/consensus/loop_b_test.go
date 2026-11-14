package consensus

import (
	"context"
	"fmt"
	"testing"
)

type mockReviewer struct {
	id      string
	family  string
	ce      int
	reviews map[int]Review
	errs    map[int]error
}

func (m *mockReviewer) ID() string     { return m.id }
func (m *mockReviewer) Family() string { return m.family }
func (m *mockReviewer) CE() int        { return m.ce }
func (m *mockReviewer) Review(_ context.Context, _ string, round int) (Review, error) {
	if m.errs != nil {
		if err, ok := m.errs[round]; ok {
			return Review{}, err
		}
	}
	if m.reviews != nil {
		if rev, ok := m.reviews[round]; ok {
			return rev, nil
		}
	}
	return Review{Approve: true}, nil
}

// TestConsensusFirstRound verifies immediate agreement with no fixer call.
func TestConsensusFirstRound(t *testing.T) {
	a := &mockReviewer{id: "A", family: "x", ce: 90, reviews: map[int]Review{
		1: {Approve: true},
	}}
	b := &mockReviewer{id: "B", family: "y", ce: 88, reviews: map[int]Review{
		1: {Approve: true},
	}}

	fixCalled := false
	fixer := func(_ context.Context, subj string, _ []Finding) (string, error) {
		fixCalled = true
		return subj, nil
	}

	v, err := Run(context.Background(), "subj", [][]Reviewer{{a, b}}, fixer, DefaultPolicy())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !v.Agreed {
		t.Fatal("expected agreed")
	}
	if v.Rounds != 1 {
		t.Fatalf("expected 1 round, got %d", v.Rounds)
	}
	if v.Subject != "subj" {
		t.Fatalf("expected subject unchanged, got %q", v.Subject)
	}
	if fixCalled {
		t.Fatal("fixer should not be called")
	}
}

// TestConsensusFixThenAgree verifies fixer invocation and second-round agreement.
func TestConsensusFixThenAgree(t *testing.T) {
	a := &mockReviewer{id: "A", family: "x", ce: 90, reviews: map[int]Review{
		1: {Approve: true},
		2: {Approve: true},
	}}
	b := &mockReviewer{id: "B", family: "y", ce: 88, reviews: map[int]Review{
		1: {Approve: false, Findings: []Finding{{Reviewer: "B", Severity: "blocking", Text: "missing test"}}},
		2: {Approve: true},
	}}

	var fixFindings []Finding
	fixer := func(_ context.Context, _ string, blocking []Finding) (string, error) {
		fixFindings = blocking
		return "fixed-subj", nil
	}

	v, err := Run(context.Background(), "subj", [][]Reviewer{{a, b}}, fixer, DefaultPolicy())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !v.Agreed {
		t.Fatal("expected agreed")
	}
	if v.Rounds != 2 {
		t.Fatalf("expected 2 rounds, got %d", v.Rounds)
	}
	if v.Subject != "fixed-subj" {
		t.Fatalf("expected fixed subject, got %q", v.Subject)
	}
	if len(fixFindings) != 1 || fixFindings[0].Text != "missing test" {
		t.Fatalf("unexpected fix findings: %v", fixFindings)
	}
}

// TestConsensusEscalates verifies rung escalation and last-rung reuse.
func TestConsensusEscalates(t *testing.T) {
	a := &mockReviewer{id: "A", family: "x", ce: 90, reviews: map[int]Review{
		1: {Approve: false, Findings: []Finding{{Reviewer: "A", Severity: "blocking", Text: "bad"}}},
	}}
	b := &mockReviewer{id: "B", family: "y", ce: 88, reviews: map[int]Review{
		1: {Approve: false, Findings: []Finding{{Reviewer: "B", Severity: "blocking", Text: "bad"}}},
	}}
	c := &mockReviewer{id: "C", family: "z", ce: 90, reviews: map[int]Review{
		2: {Approve: true},
	}}
	d := &mockReviewer{id: "D", family: "w", ce: 90, reviews: map[int]Review{
		2: {Approve: true},
	}}

	fixer := func(_ context.Context, _ string, _ []Finding) (string, error) {
		return "fixed", nil
	}

	ladder := [][]Reviewer{{a, b}, {c, d}}
	v, err := Run(context.Background(), "subj", ladder, fixer, DefaultPolicy())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !v.Agreed {
		t.Fatal("expected agreed after escalation")
	}
	if v.Rounds != 2 {
		t.Fatalf("expected 2 rounds, got %d", v.Rounds)
	}
}

// TestConsensusGivesUp verifies max rounds exhaustion without agreement.
func TestConsensusGivesUp(t *testing.T) {
	a := &mockReviewer{id: "A", family: "x", ce: 90}
	b := &mockReviewer{id: "B", family: "y", ce: 88}

	// Always block on every round.
	a.reviews = make(map[int]Review)
	b.reviews = make(map[int]Review)
	for i := 1; i <= 3; i++ {
		a.reviews[i] = Review{Approve: false, Findings: []Finding{{Reviewer: "A", Severity: "blocking", Text: fmt.Sprintf("issue-%d", i)}}}
		b.reviews[i] = Review{Approve: false, Findings: []Finding{{Reviewer: "B", Severity: "blocking", Text: fmt.Sprintf("issue-b-%d", i)}}}
	}

	fixer := func(_ context.Context, _ string, _ []Finding) (string, error) {
		return "fixed", nil
	}

	v, err := Run(context.Background(), "subj", [][]Reviewer{{a, b}}, fixer, DefaultPolicy())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Agreed {
		t.Fatal("expected not agreed")
	}
	if v.Rounds != 3 {
		t.Fatalf("expected 3 rounds, got %d", v.Rounds)
	}
	if len(v.Open) == 0 {
		t.Fatal("expected open findings")
	}
	expectedReason := "no consensus after 3 rounds"
	if v.Reason != expectedReason {
		t.Fatalf("expected reason %q, got %q", expectedReason, v.Reason)
	}
}
