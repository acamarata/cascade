package review

import (
	"context"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// JobReviewPolicySource identifies the consequence policy used for job reviews.
const JobReviewPolicySource = "internal/review.ConsequenceClass@1"

// ErrUnknownRiskClass rejects job risks outside the closed risk vocabulary.
var ErrUnknownRiskClass = cascade.New(cascade.KindInvalidInput, "internal/review: unknown job risk class")

// ErrNoProviderRegistry reports that provider families cannot be queried.
var ErrNoProviderRegistry = cascade.New(cascade.KindUnavailable, "internal/review: no provider registry")

// JobReviewPolicy adapts job risk gates to the review consequence policy.
type JobReviewPolicy struct {
	Class       ConsequenceClass
	ReviewGates int
	Source      string
	Checklist   []string
}

var _ jobs.ReviewPolicySource = JobReviewPolicy{}

// NewJobReviewPolicy maps a known job risk to consequence rules and review gates.
func NewJobReviewPolicy(rc jobs.RiskClass) (JobReviewPolicy, error) {
	var class ConsequenceClass
	switch rc {
	case jobs.RiskClassLow:
		class = ConsequenceLow
	case jobs.RiskClassNormal:
		class = ConsequenceNormal
	case jobs.RiskClassHigh:
		class = ConsequenceHigh
	case jobs.RiskClassCritical:
		class = ConsequenceCritical
	default:
		return JobReviewPolicy{}, cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownRiskClass, "%q", rc)
	}
	gates, err := jobs.GateSetForRiskClass(rc)
	if err != nil {
		return JobReviewPolicy{}, err
	}
	p := JobReviewPolicy{Class: class, Source: JobReviewPolicySource, Checklist: ChecklistV1Dimensions()}
	for _, gate := range gates {
		if gate == jobs.GateCodeReview || gate == jobs.GateAdversarialReview {
			p.ReviewGates++
		}
	}
	return p, nil
}

// RequiresDistinctFamily delegates to the consequence policy, failing closed.
func (p JobReviewPolicy) RequiresDistinctFamily() bool {
	return p.Class.RequiresDistinctFamily()
}

// HoldsWithoutDistinctFamily reports whether missing distinctness holds review.
func (p JobReviewPolicy) HoldsWithoutDistinctFamily() bool {
	return p.Class.HoldsWithoutDistinctFamily()
}

// CheckAvailable applies the jobs family gate to currently registered drivers.
func (p JobReviewPolicy) CheckAvailable(ctx context.Context, reg provider.ProviderRegistryReader, author jobs.FamilyRef) (degraded bool, err error) {
	families, err := AvailableFamilies(ctx, reg)
	if err != nil {
		return false, err
	}
	eligible := make([]jobs.FamilyRef, 0, len(families))
	for _, family := range families {
		eligible = append(eligible, jobs.FamilyRef{Family: family, Resolved: true})
	}
	return jobs.RequireDistinctFamily(p, author, eligible)
}

// ProviderFamily resolves an account name to its driver, failing closed.
func ProviderFamily(ctx context.Context, reg provider.ProviderRegistryReader, providerName string) jobs.FamilyRef {
	f := resolveFamily(ctx, reg, provider.Selection{Provider: providerName})
	return jobs.FamilyRef{Family: f.family, Resolved: f.resolved}
}

// AvailableFamilies returns distinct drivers in registry order.
func AvailableFamilies(ctx context.Context, reg provider.ProviderRegistryReader) ([]string, error) {
	if reg == nil {
		return nil, ErrNoProviderRegistry
	}
	return eligibleFamilies(ctx, reg)
}
