// Purpose: adapt Fleet's filtered snapshot to the single economics ranker.
// Inputs: candidate source and normalized request. Outputs: ranked candidates.
// Constraints: no reservation, execution or decision writes.

package capacity

import (
	"context"
	"math/rand"
	"slices"
	"strings"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/pkg/cascade"
)

// CandidateSource reads the candidate facts for one selection. Errors refuse
// selection, including when the source also returns partial candidates.
type CandidateSource func(context.Context, SelectInput) ([]economics.Candidate, error)

// RankSelector selects lanes through the economics ranker.
type RankSelector struct {
	source  CandidateSource
	learned LearnedInputReader
}

// NewRankSelector constructs the economics.Rank adapter. A nil source refuses
// every selection rather than falling back to the tier policy.
func NewRankSelector(source CandidateSource, learned ...LearnedInputReader) *RankSelector {
	s := &RankSelector{source: source}
	if len(learned) > 0 {
		s.learned = learned[0]
	}
	return s
}

// Select implements Selector using the first eligible ranked candidate.
func (s *RankSelector) Select(ctx context.Context, in SelectInput) (Selection, error) {
	sel, _, err := s.SelectRanked(ctx, in)
	return sel, err
}

// SelectRanked returns the selection and the exact facts used to rank it.
func (s *RankSelector) SelectRanked(ctx context.Context, in SelectInput) (Selection, economics.Ranked, error) {
	ranking, hooks, err := s.rankInputs(ctx, in)
	if err != nil {
		return Selection{}, economics.Ranked{}, err
	}
	if hooks.Refine != nil {
		r, err := hooks.Refine(ctx, string(in.Request.TaskClass), joinedTier(in.Request), ranking)
		if err != nil {
			return Selection{}, economics.Ranked{}, err
		}
		for _, candidate := range ranking.Order {
			if candidate.Eligible && candidate.Candidate.LaneID == r.Candidate.LaneID && candidate.Candidate.DomainID == r.Candidate.DomainID {
				return rankedSelection(in, candidate), candidate, nil
			}
		}
		return Selection{}, economics.Ranked{}, ErrNoCapableTier
	}
	for _, r := range ranking.Order {
		if r.Eligible {
			return rankedSelection(in, r), r, nil
		}
	}
	return Selection{}, economics.Ranked{}, ErrNoCapableTier
}

// Rank reads candidates once and delegates all ordering to economics.Rank.
func (s *RankSelector) Rank(ctx context.Context, in SelectInput) (economics.Ranking, error) {
	r, _, err := s.rankInputs(ctx, in)
	return r, err
}

func rankedLane(c economics.Candidate) conductor.LaneID {
	if c.RegistryLane != "" {
		return conductor.LaneID(c.RegistryLane)
	}
	return conductor.LaneID(c.LaneID)
}

func rankedSelection(in SelectInput, r economics.Ranked) Selection {
	lane := rankedLane(r.Candidate)
	return Selection{Lane: lane, Score: r.Utility, Tier: TierSelection{Tier: in.Snapshot.Lanes[lane].Tier}}
}

func (s *RankSelector) rankInputs(ctx context.Context, in SelectInput) (economics.Ranking, LearnedInputs, error) {
	if s == nil || s.source == nil {
		return economics.Ranking{}, LearnedInputs{}, refuse(ErrNoCapableTier, nil, "nil candidate source")
	}
	var hooks LearnedInputs
	if s.learned != nil {
		read, bound, err := s.learned()
		if err != nil {
			return economics.Ranking{}, hooks, err
		}
		if bound {
			hooks = read
		}
	}
	cs, err := s.source(ctx, in)
	if err != nil {
		return economics.Ranking{}, hooks, refuse(ErrNoCapableTier, err, "read candidates")
	}
	ranking, err := rankCandidates(in, cs, hooks)
	return ranking, hooks, err
}

func rankCandidates(in SelectInput, cs []economics.Candidate, hooks LearnedInputs) (economics.Ranking, error) {
	cs = slices.Clone(cs)
	ci := strings.HasPrefix(in.Request.Intent, "ci:")
	for i := range cs {
		if !slices.Contains(in.Eligible, rankedLane(cs[i])) {
			cs[i].Reservable = cascade.New(cascade.KindPolicyDenied, "lane refused by fleet floors")
		}
		if ci {
			cs[i].ModelAvailable = true
			cs[i].ExpectedQuality, cs[i].PredictedFailure = 0, 0
			cs[i].LatencyP50, cs[i].EWMA429 = 0, 0
		}
	}
	r := in.Request
	producer, independence := r.Producer, r.IndependenceWeight
	if ci {
		zero := 0.0
		producer, independence = nil, &zero
	}
	ranking, err := economics.Rank(economics.RankRequest{Mode: economics.ModeBuild, TaskClass: string(r.TaskClass),
		Sensitivity: joinedTier(r), Roles: r.Roles, Producer: producer, IndependenceWeight: independence,
		LearnedPairs: hooks.Pairs, AllowReserve: !r.ReserveTier0, Tables: economics.NewTables(), Jitter: rand.NewSource(0)}, cs)
	if ci {
		markCINotApplicable(&ranking)
	}
	return ranking, err
}

func markCINotApplicable(r *economics.Ranking) {
	for i := range r.Order {
		for j := range r.Order[i].Verdicts {
			v := &r.Order[i].Verdicts[j]
			if v.Predicate == "model-available" {
				v.Applicable, v.Reason = false, "CI has no model requirement"
			}
		}
	}
}

// RankOnly runs validation, snapshot and floors through Rank. It does not
// place work, mint lease identifiers or write any ledger rows.
func (f *Fleet) RankOnly(ctx context.Context, req ResourceRequest) (economics.Ranking, error) {
	req, err := ValidateResourceRequest(req, req.DataClass)
	if err != nil {
		return economics.Ranking{}, err
	}
	read, err := f.deps.Snapshot(ctx)
	if err != nil {
		return economics.Ranking{}, refuse(ErrNoCapableTier, err, "read the capacity snapshot")
	}
	snap := isolate(read)
	p := leasePlan{req: req, tier: joinedTier(req)}
	eligible, err := f.eligibleLanes(snap, p)
	if err != nil {
		return economics.Ranking{}, err
	}
	ranker, ok := f.deps.Selector.(interface {
		Rank(context.Context, SelectInput) (economics.Ranking, error)
	})
	if !ok {
		return economics.Ranking{}, refuse(ErrNoCapableTier, nil, "selector does not support Rank")
	}
	return ranker.Rank(ctx, SelectInput{Snapshot: snap, Eligible: eligible, Request: req,
		Scorer: f.deps.Scorer(globalScorerKey), Attempt: 1})
}
