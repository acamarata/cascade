package economics

import (
	"math"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/pkg/provider"
)

const integrationReserve = 0.10

// integrationLane builds a candidate the way the capacity selector does:
// the base price from topology, the mode multiplier from the live tables
// and the shadow price and reserve signals from ShadowPrice itself.
func integrationLane(t *testing.T, tables Tables, id string, remaining float64) Candidate {
	t.Helper()
	class := topology.LaneClassWorkerDedicated
	base, err := topology.BaseShadowPrice(class)
	if err != nil {
		t.Fatal(err)
	}
	mult, err := tables.Multiplier(class, ModeBuild)
	if err != nil {
		t.Fatal(err)
	}
	sp := ShadowPrice(ShadowPriceInput{LaneID: topology.LaneID(id), BasePrice: base, ModeMultiplier: mult,
		Reserve: integrationReserve, RemainingFraction: remaining})
	return Candidate{LaneID: topology.LaneID(id), Provider: "provider-x", LaneClass: class, BasePrice: base, SP: sp,
		Identity: topology.ModelIdentity{CanonicalID: "model-a", Family: "family-a"},
		Roles:    []topology.Role{topology.RoleCoder}, Modalities: []string{"text"}, Tools: []string{"shell"},
		Ceiling:        topology.Ceiling{Tier: provider.SensitivityInternal, Reason: topology.CeilingBillingCap},
		Health:         topology.LaneHealthAvailable,
		ModelAvailable: true, WorkspacePermitted: true,
		AtOrBelowReserve: sp.BelowHardReserve, BelowAbsoluteFloor: sp.BelowAbsoluteFloor,
		ExpectedQuality: 0.8, PredictedFailure: 0.1,
	}
}

// integrationCandidates returns two eligible lanes that differ only in
// model identity and three lanes that each fail exactly one predicate.
func integrationCandidates(t *testing.T, tables Tables) ([]Candidate, map[string]string) {
	t.Helper()
	same := integrationLane(t, tables, "lane-same", 0.9)
	diverse := integrationLane(t, tables, "lane-diverse", 0.9)
	diverse.Identity = topology.ModelIdentity{CanonicalID: "model-z", Family: "family-z"}
	demoted := integrationLane(t, tables, "lane-demoted", 0.9)
	demoted.TransientDemoted = true
	public := integrationLane(t, tables, "lane-public", 0.9)
	public.Ceiling.Tier = provider.SensitivityPublic
	reserve := integrationLane(t, tables, "lane-reserve", integrationReserve/2)
	fails := map[string]string{"lane-demoted": "lane-health", "lane-public": "data-class", "lane-reserve": "mode-permits"}
	return []Candidate{reserve, same, public, demoted, diverse}, fails
}

// TestRankIntegration drives Rank through the production request shape:
// live tables, the sealed builtin registry, real shadow prices, the
// topology data-class gate, structural correlation and the explain view.
func TestRankIntegration(t *testing.T) {
	previous := laneRankRegistry
	laneRankRegistry = newRankRegistry()
	t.Cleanup(func() { laneRankRegistry = previous })
	fixture, _ := rankFixture()
	tables := NewTables()
	cands, fails := integrationCandidates(t, tables)
	req := RankRequest{Mode: ModeBuild, TaskClass: "code", Sensitivity: provider.SensitivityInternal,
		Roles: []topology.Role{topology.RoleCoder}, Modalities: []string{"text"}, Tools: []string{"shell"},
		Producer: &topology.ModelIdentity{CanonicalID: "model-a", Family: "family-a"}, ProducerSession: "producer-lane",
		Tables: tables, Jitter: fixture.Jitter}
	got, err := Rank(req, cands)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"lane-diverse", "lane-same", "lane-demoted", "lane-public", "lane-reserve"}
	if got.RoleFallback || len(got.Order) != len(want) {
		t.Fatalf("fallback=%v order=%d", got.RoleFallback, len(got.Order))
	}
	for i, ranked := range got.Order {
		if string(ranked.Candidate.LaneID) != want[i] {
			t.Fatalf("position %d: got %s, want %s", i, ranked.Candidate.LaneID, want[i])
		}
		assertIntegrationVerdicts(t, ranked, fails[want[i]])
		if ranked.Eligible {
			assertIntegrationTerms(t, req, ranked)
		}
	}
	explain := ExplainRanking(got)
	for _, line := range []string{"lane lane-diverse eligible=yes", "lane lane-reserve eligible=no",
		"mode-permits: fail at or below hard reserve", "lane-health: fail lane unhealthy",
		"data-class: fail data class exceeds lane ceiling"} {
		if !strings.Contains(explain, line) {
			t.Errorf("explain lacks %q:\n%s", line, explain)
		}
	}
	if err := RegisterPredicate(Predicate{Name: "late", Eval: envelopeVerdict}); err == nil {
		t.Fatal("registry accepted a predicate after Rank sealed it")
	}
}

// assertIntegrationVerdicts checks the nine builtin verdicts in order and
// that only the expected predicate (if any) failed.
func assertIntegrationVerdicts(t *testing.T, ranked Ranked, failing string) {
	t.Helper()
	builtins := EligibilityPredicates()
	if len(ranked.Verdicts) != len(builtins) || len(builtins) != 9 {
		t.Fatalf("%s: %d verdicts", ranked.Candidate.LaneID, len(ranked.Verdicts))
	}
	for i, v := range ranked.Verdicts {
		if v.Predicate != builtins[i].Name {
			t.Fatalf("%s verdict %d: got %s, want %s", ranked.Candidate.LaneID, i, v.Predicate, builtins[i].Name)
		}
		if failed := v.Applicable && !v.Pass; failed != (v.Predicate == failing) {
			t.Fatalf("%s: %s failed=%v (%s)", ranked.Candidate.LaneID, v.Predicate, failed, v.Reason)
		}
	}
	if ranked.Eligible != (failing == "") {
		t.Fatalf("%s: eligible=%v", ranked.Candidate.LaneID, ranked.Eligible)
	}
}

// assertIntegrationTerms recomputes the shadow-price and correlation terms
// from their public functions and checks the utility is the term sum.
func assertIntegrationTerms(t *testing.T, req RankRequest, ranked Ranked) {
	t.Helper()
	c := ranked.Candidate
	if len(ranked.Terms) != 6 {
		t.Fatalf("%s: %d terms", c.LaneID, len(ranked.Terms))
	}
	wantSP := c.BasePrice * c.SP.Pressure * c.SP.ModeMultiplier * c.SP.ReserveBarrier
	if c.SP.ShadowPrice != wantSP || ranked.Terms[1].Value != math.Log1p(wantSP) {
		t.Fatalf("%s: shadow price %g term %g", c.LaneID, c.SP.ShadowPrice, ranked.Terms[1].Value)
	}
	wantCorr := CorrelationPenalty(req.Producer, "", req.ProducerSession, req.TaskClass, c, nil, false)
	if ranked.Correlation != wantCorr || wantCorr == 0 {
		t.Fatalf("%s: correlation %g want %g", c.LaneID, ranked.Correlation, wantCorr)
	}
	sum := 0.0
	for _, term := range ranked.Terms {
		sum += term.Contribution
	}
	if math.Abs(sum-ranked.Utility) > 1e-12 {
		t.Fatalf("%s: utility %g, terms sum %g", c.LaneID, ranked.Utility, sum)
	}
}
