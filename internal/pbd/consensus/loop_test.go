package consensus

import (
	"context"
	"errors"
	"testing"
)

type fakeReviewer struct {
	id     string
	family string
	ce     int
	called bool
}

func (f *fakeReviewer) ID() string     { return f.id }
func (f *fakeReviewer) Family() string { return f.family }
func (f *fakeReviewer) CE() int        { return f.ce }
func (f *fakeReviewer) Review(_ context.Context, subject string, round int) (Review, error) {
	f.called = true
	return Review{Approve: true}, nil
}

func TestConsensusWeakPanel(t *testing.T) {
	p := DefaultPolicy() // MinReviewers: 2, MinCE: 85, MaxRounds: 3

	tests := []struct {
		name string
		rung []Reviewer
	}{
		{
			name: "one reviewer",
			rung: []Reviewer{
				&fakeReviewer{id: "r1", family: "f1", ce: 90},
			},
		},
		{
			name: "two of same family",
			rung: []Reviewer{
				&fakeReviewer{id: "r1", family: "f1", ce: 90},
				&fakeReviewer{id: "r2", family: "f1", ce: 90},
			},
		},
		{
			name: "CE 84",
			rung: []Reviewer{
				&fakeReviewer{id: "r1", family: "f1", ce: 90},
				&fakeReviewer{id: "r2", family: "f2", ce: 84},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ladder := [][]Reviewer{tt.rung}
			_, err := Run(context.Background(), "subject", ladder, nil, p)
			if !errors.Is(err, ErrWeakPanel) {
				t.Fatalf("expected ErrWeakPanel, got %v", err)
			}
			for _, r := range tt.rung {
				if fr, ok := r.(*fakeReviewer); ok {
					if fr.called {
						t.Errorf("Review was called before ErrWeakPanel was returned")
					}
				}
			}
		})
	}
}
