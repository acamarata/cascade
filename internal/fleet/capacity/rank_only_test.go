package capacity

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func rankedFixture(t *testing.T) (*fx, *[]economics.Candidate) {
	t.Helper()
	dir := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(key, dir)
	}
	f := newFx(t)
	f.seedJob("rank-job", provider.SensitivityInternal)
	cs := []economics.Candidate{
		{LaneID: "lane-t1", DomainID: "rank-domain", LaneClass: topology.LaneClassLocal,
			Health: topology.LaneHealthAvailable, ModelAvailable: true, WorkspacePermitted: true,
			Ceiling: topology.Ceiling{Tier: provider.SensitivityLocalOnly, Reason: topology.CeilingLocalEndpoint}, ExpectedQuality: .9},
		{LaneID: "lane-t2", DomainID: "other-domain", LaneClass: topology.LaneClassLocal,
			Health: topology.LaneHealthAvailable, ModelAvailable: true, WorkspacePermitted: true,
			Ceiling: topology.Ceiling{Tier: provider.SensitivityLocalOnly, Reason: topology.CeilingLocalEndpoint}, ExpectedQuality: .5},
	}
	f.deps.Selector = NewRankSelector(func(context.Context, SelectInput) ([]economics.Candidate, error) { return cs, nil })
	f.rebuild()
	return f, &cs
}

func TestResolveTakesRankedDomain(t *testing.T) {
	f, cs := rankedFixture(t)
	chooser := topology.NewChooser(f.clock, 1, topology.BillingPrivacyGate,
		func(context.Context, topology.Credential, topology.QuotaDomain) (topology.Ceiling, error) {
			return topology.Ceiling{Tier: provider.SensitivityInternal, Reason: topology.CeilingLaneType}, nil
		}, nil)
	chooser.SetTopology(nil, []topology.QuotaDomain{{ID: "rank-domain"}, {ID: "other-domain"}}, nil,
		[]topology.Credential{{ID: "rank-cred", QuotaDomainRef: "rank-domain", Health: topology.CredentialOK},
			{ID: "other-cred", QuotaDomainRef: "other-domain", Health: topology.CredentialOK}})
	var credential topology.CredentialID
	source := f.deps.Selector.(*RankSelector).source
	f.deps.Selector = NewRankSelector(source, func() (LearnedInputs, bool, error) {
		return LearnedInputs{Refine: func(ctx context.Context, _ string, tier provider.SensitivityTier, r economics.Ranking) (economics.Ranked, error) {
			selected := r.Order[0]
			var err error
			credential, err = chooser.Choose(ctx, topology.Lane{QuotaDomainRef: selected.Candidate.DomainID}, tier.String())
			return selected, err
		}}, true, nil
	})
	f.deps.DomainOf = func(context.Context, conductor.LaneID) (string, error) {
		panic("ranked selection must not resolve a sibling")
	}
	f.rebuild()
	for i, domain := range []string{"rank-domain", "other-domain"} {
		l, err := f.fleet.Lease(t.Context(), request(), dispatchFor("rank-job"), i+1)
		if err != nil {
			t.Fatal(err)
		}
		r, ok, err := f.fleet.ReservationByExecution(t.Context(), l.ExecutionID)
		if err != nil || !ok || r.DomainID != domain {
			t.Fatalf("reservation = %+v, %v, %v; want %s", r, ok, err, domain)
		}
		if credential != []topology.CredentialID{"rank-cred", "other-cred"}[i] {
			t.Fatalf("credential = %s for domain %s", credential, domain)
		}
		(*cs)[0].Health = topology.LaneHealth("rate_limited")
	}
}

func TestResolveNilCandidateSourceFailsClosed(t *testing.T) {
	f, _ := rankedFixture(t)
	good := f.deps.Selector
	for _, source := range []CandidateSource{nil, func(context.Context, SelectInput) ([]economics.Candidate, error) {
		return nil, cascade.New(cascade.KindUnavailable, "candidate read failed")
	}} {
		f.deps.Selector = NewRankSelector(source)
		f.rebuild()
		_, err := f.fleet.Lease(t.Context(), request(), dispatchFor("rank-job"), 1)
		requireSentinel(t, err, ErrNoCapableTier, ErrNoCapableTier.Msg)
		f.zeroRows()
	}
	f.deps.Selector = good
	f.rebuild()
	if _, err := f.fleet.Lease(t.Context(), request(), dispatchFor("rank-job"), 1); err != nil {
		t.Fatal(err)
	}
}

func TestDecisionRecordCarriesRankedCandidate(t *testing.T) {
	f, _ := rankedFixture(t)
	req := request()
	req.DataClass = new(provider.SensitivityInternal)
	r, err := f.fleet.RankOnly(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	l, err := f.fleet.Lease(t.Context(), request(), dispatchFor("rank-job"), 1)
	if err != nil {
		t.Fatal(err)
	}
	d := f.decisionOf(l.DecisionID)
	if d.LaneID != l.LaneID || d.ScoreAtSelection != r.Order[0].Utility {
		t.Fatalf("decision = %+v, ranking = %+v", d, r)
	}
}

func TestCIIntentRanking(t *testing.T) {
	f, cs := rankedFixture(t)
	(*cs)[0].ModelAvailable, (*cs)[1].ModelAvailable = false, false
	(*cs)[0].SP.ShadowPrice = 5
	(*cs)[1].ExpectedQuality = -100
	_, err := f.fleet.Lease(t.Context(), request(), dispatchFor("rank-job"), 1)
	requireSentinel(t, err, ErrNoCapableTier, ErrNoCapableTier.Msg)
	f.zeroRows()
	req := request()
	req.Intent = "ci:unit"
	d := dispatchFor("rank-job")
	d.Placement = &PlacementNeed{Capabilities: []string{"toolchain:go"}}
	l, err := f.fleet.Lease(t.Context(), req, d, 1)
	if err != nil || l.LaneID != "lane-t2" || l.NodeID != "node-worker" {
		t.Fatalf("CI lease = %+v, %v", l, err)
	}
}

func TestRankOnlyZeroReservations(t *testing.T) {
	f, cs := rankedFixture(t)
	(*cs)[1].Reservable = cascade.New(cascade.KindUnavailable, "quota exhausted")
	req := request()
	req.DataClass = new(provider.SensitivityInternal)
	for range 2 {
		r, err := f.fleet.RankOnly(t.Context(), req)
		if err != nil || len(r.Order) != 2 || !r.Order[0].Eligible || r.Order[1].Eligible || len(r.Order[1].Verdicts) != 9 {
			t.Fatalf("ranking = %+v, %v", r, err)
		}
		f.zeroRows()
	}
	if _, err := f.fleet.Lease(t.Context(), request(), dispatchFor("rank-job"), 1); err != nil {
		t.Fatal(err)
	}
	before := []int{f.count("jobs_reservation"), f.count("jobs_execution"), f.count("test_decision")}
	if _, err := f.fleet.RankOnly(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	for i, table := range []string{"jobs_reservation", "jobs_execution", "test_decision"} {
		if after := f.count(table); after != before[i] {
			t.Fatalf("%s: %d -> %d", table, before[i], after)
		}
	}
}

type rankedRecorder struct {
	*sqlDecisions
	got economics.Ranked
}

func (r *rankedRecorder) RecordRanked(ctx context.Context, q DecisionExecer, d DecisionRecord, selected economics.Ranked) error {
	r.got = selected
	return r.Record(ctx, q, d)
}

func TestRankedRecorderFacts(t *testing.T) {
	f, cs := rankedFixture(t)
	(*cs)[0].SP.ShadowPrice, (*cs)[0].PredictedFailure = .1, .2
	recorder := &rankedRecorder{sqlDecisions: f.decisions}
	f.deps.Decisions = recorder
	f.rebuild()
	l, err := f.fleet.Lease(t.Context(), request(), dispatchFor("rank-job"), 1)
	if err != nil {
		t.Fatal(err)
	}
	d := f.decisionOf(l.DecisionID)
	if recorder.got.Candidate.DomainID != "rank-domain" || recorder.got.Candidate.SP.ShadowPrice != .1 || recorder.got.Candidate.PredictedFailure != .2 || d.ScoreAtSelection != recorder.got.Utility {
		t.Fatalf("recorder got %+v, decision %+v", recorder.got, d)
	}
}

func TestRankOnlyRefusals(t *testing.T) {
	for _, kind := range []string{"validation", "snapshot", "floor", "selector", "empty-domain", "no-candidates"} {
		t.Run(kind, func(t *testing.T) {
			f, cs := rankedFixture(t)
			req := request()
			req.DataClass = new(provider.SensitivityInternal)
			switch kind {
			case "validation":
				req.Intent = ""
			case "snapshot":
				f.deps.Snapshot = func(context.Context) (FleetSnapshot, error) {
					return FleetSnapshot{}, cascade.New(cascade.KindUnavailable, "snapshot failed")
				}
			case "floor":
				f.snap.Lanes = nil
			case "selector":
				f.deps.Selector = NewTierPolicySelector(newTestPolicy(stubEstimator{}, f.clock))
			case "empty-domain":
				(*cs)[0].DomainID = ""
			case "no-candidates":
				*cs = nil
			}
			f.rebuild()
			if kind == "empty-domain" || kind == "no-candidates" {
				_, err := f.fleet.Lease(t.Context(), req, dispatchFor("rank-job"), 1)
				want := ErrNoCapableTier
				if kind == "empty-domain" {
					want = ErrNoQuotaDomain
				}
				requireSentinel(t, err, want, want.Msg)
			} else if _, err := f.fleet.RankOnly(t.Context(), req); err == nil {
				t.Fatal("RankOnly admitted invalid input or failed read")
			}
			f.zeroRows()
		})
	}
}

func TestRankSelectorLearnedRefusals(t *testing.T) {
	for _, state := range []string{"unbound", "read-error", "refine-error", "outside-ranking"} {
		t.Run(state, func(t *testing.T) {
			f, cs := rankedFixture(t)
			(*cs)[0].RegistryLane, (*cs)[0].LaneID = "lane-t1", "topology-lane"
			source := f.deps.Selector.(*RankSelector).source
			failure := cascade.New(cascade.KindUnavailable, "learned read failed")
			f.deps.Selector = NewRankSelector(source, func() (LearnedInputs, bool, error) {
				if state == "read-error" {
					return LearnedInputs{}, false, failure
				}
				return LearnedInputs{Refine: func(context.Context, string, provider.SensitivityTier, economics.Ranking) (economics.Ranked, error) {
					if state == "refine-error" {
						return economics.Ranked{}, failure
					}
					return economics.Ranked{Eligible: true, Candidate: economics.Candidate{LaneID: "invented", DomainID: "sibling"}}, nil
				}}, state != "unbound", nil
			})
			f.rebuild()
			l, err := f.fleet.Lease(t.Context(), request(), dispatchFor("rank-job"), 1)
			if state == "unbound" {
				if err != nil || l.LaneID != "lane-t1" {
					t.Fatalf("lease = %+v, %v", l, err)
				}
			} else {
				requireSentinel(t, err, ErrNoCapableTier, ErrNoCapableTier.Msg)
				f.zeroRows()
			}
		})
	}
}
