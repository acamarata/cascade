// Package consensus implements multi-reviewer consensus loops with policy enforcement.
package consensus

import (
	"context"
	"errors"
	"fmt"
)

// ensureFmtUsed keeps the fmt import referenced.
var _ = fmt.Sprintf

// ErrWeakPanel is returned when the reviewer panel violates policy constraints.
var ErrWeakPanel = errors.New("weak panel")

// Finding represents a single issue discovered during review.
type Finding struct {
	Reviewer string
	Severity string // "blocking" or "minor"
	Text     string
}

// Review holds the outcome of a single reviewer's evaluation.
type Review struct {
	Approve  bool
	Findings []Finding
}

// Reviewer is an entity that can evaluate a subject.
type Reviewer interface {
	ID() string
	Family() string
	CE() int
	Review(ctx context.Context, subject string, round int) (Review, error)
}

// Fixer attempts to resolve blocking findings and returns a new subject.
type Fixer func(ctx context.Context, subject string, blocking []Finding) (string, error)

// Policy defines the constraints for a consensus run.
type Policy struct {
	MinReviewers int
	MinCE        int
	MaxRounds    int
}

// DefaultPolicy returns the standard policy: 2 reviewers, CE 85, 3 rounds.
func DefaultPolicy() Policy {
	return Policy{
		MinReviewers: 2,
		MinCE:        85,
		MaxRounds:    3,
	}
}

// Verdict captures the final outcome of a consensus run.
type Verdict struct {
	Agreed    bool
	Rounds    int
	Subject   string
	Open      []Finding
	Reviewers []string
	Reason    string
}

// Run executes the consensus loop over the given ladder of reviewer panels.
// It returns ErrWeakPanel before any Review call if a rung fails policy checks.
func Run(ctx context.Context, subject string, ladder [][]Reviewer, fix Fixer, p Policy) (Verdict, error) {
	if len(ladder) == 0 {
		return Verdict{Subject: subject}, errors.New("empty ladder")
	}
	reviewers := collectReviewers(ladder)

	for _, rung := range ladder {
		if err := validateRung(rung, p); err != nil {
			return Verdict{Subject: subject, Reviewers: reviewers}, err
		}
	}

	return runRounds(ctx, subject, ladder, fix, p, reviewers)
}

func runRounds(ctx context.Context, subject string, ladder [][]Reviewer, fix Fixer, p Policy, reviewers []string) (Verdict, error) {
	current := subject
	var open []Finding
	var blocking []Finding

	for round := 1; round <= p.MaxRounds; round++ {
		open = nil
		blocking = nil
		allApproved := true

		rungIdx := round - 1
		if rungIdx >= len(ladder) {
			rungIdx = len(ladder) - 1
		}
		rung := ladder[rungIdx]

		for _, r := range rung {
			rev, err := r.Review(ctx, current, round)
			if err != nil {
				rev = Review{
					Approve: false,
					Findings: []Finding{{
						Reviewer: r.ID(),
						Severity: "blocking",
						Text:     fmt.Sprintf("reviewer %s: %s", r.ID(), err.Error()),
					}},
				}
			}
			if !rev.Approve {
				allApproved = false
			}
			open = append(open, rev.Findings...)
			for _, f := range rev.Findings {
				if f.Severity == "blocking" {
					blocking = append(blocking, f)
				}
			}
		}

		if allApproved && len(blocking) == 0 {
			return Verdict{
				Agreed:    true,
				Rounds:    round,
				Subject:   current,
				Reviewers: reviewers,
			}, nil
		}

		if round == p.MaxRounds {
			return Verdict{
				Agreed:    false,
				Rounds:    round,
				Subject:   current,
				Open:      open,
				Reviewers: reviewers,
				Reason:    fmt.Sprintf("no consensus after %d rounds", round),
			}, nil
		}

		if len(blocking) > 0 {
			if fix == nil {
				return Verdict{
					Agreed:    false,
					Rounds:    round,
					Subject:   current,
					Open:      open,
					Reviewers: reviewers,
					Reason:    "no fixer available",
				}, nil
			}
			fixed, err := fix(ctx, current, blocking)
			if err != nil {
				return Verdict{
					Agreed:    false,
					Rounds:    round,
					Subject:   current,
					Open:      open,
					Reviewers: reviewers,
					Reason:    err.Error(),
				}, nil
			}
			current = fixed
		}
	}

	return Verdict{
		Agreed:    false,
		Rounds:    p.MaxRounds,
		Subject:   current,
		Open:      open,
		Reviewers: reviewers,
		Reason:    fmt.Sprintf("no consensus after %d rounds", p.MaxRounds),
	}, nil
}

func validateRung(rung []Reviewer, p Policy) error {
	if len(rung) < p.MinReviewers {
		return ErrWeakPanel
	}

	families := make(map[string]bool)
	for _, r := range rung {
		if r.CE() < p.MinCE {
			return ErrWeakPanel
		}
		f := r.Family()
		if families[f] {
			return ErrWeakPanel
		}
		families[f] = true
	}

	return nil
}

func collectReviewers(ladder [][]Reviewer) []string {
	seen := make(map[string]bool)
	var out []string
	for _, rung := range ladder {
		for _, r := range rung {
			id := r.ID()
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}
