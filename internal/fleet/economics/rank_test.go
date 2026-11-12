package economics

import (
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"math"
	"math/rand"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/pkg/cascade"
)

func rankFixture() (RankRequest, Candidate) {
	r, c := predicateFixture()
	r.Tables, r.Jitter = NewTables(), rand.NewSource(7)
	c.Roles, c.LaneID = r.Roles, "a"
	c.Identity = topology.ModelIdentity{CanonicalID: "candidate", Family: "candidate-family"}
	c.Provider = "provider"
	return r, c
}

type rankGolden struct {
	Name                          string
	Mode                          Mode
	Quality, SP, Failure, EWMA429 float64
	LatencyMS                     int `json:"latency_ms"`
	Producer                      bool
	ZeroScarcity                  bool `json:"zero_scarcity"`
	Contributions                 []float64
	Utility                       float64
	Order                         []string
}

// TestRankUtilityGolden checks independently stored six-term values and orders.
func TestRankUtilityGolden(t *testing.T) {
	data, err := os.ReadFile("testdata/rank_goldens.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []rankGolden
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		t.Run(row.Name, func(t *testing.T) {
			r, a := rankFixture()
			r.Mode = row.Mode
			if row.Producer {
				r.Producer = &topology.ModelIdentity{CanonicalID: "producer", Family: "producer-family"}
			}
			if row.ZeroScarcity {
				r.Tables, err = ApplyOverrides(r.Tables, Overrides{Weights: map[string]map[string]float64{"build": {"scarcity": 0}}})
				if err != nil {
					t.Fatal(err)
				}
			}
			a.ExpectedQuality, a.SP.ShadowPrice = row.Quality, row.SP
			a.LatencyP50, a.PredictedFailure, a.EWMA429 = time.Duration(row.LatencyMS)*time.Millisecond, row.Failure, row.EWMA429
			b := a
			b.LaneID = "b"
			if row.ZeroScarcity {
				b.SP.ShadowPrice = 1
			}
			got, err := Rank(r, []Candidate{b, a})
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Order) != 2 {
				t.Fatal(got)
			}
			for i, ranked := range got.Order {
				assertRankGolden(t, ranked, row, i)
			}
		})
	}
}

func assertRankGolden(t *testing.T, ranked Ranked, row rankGolden, index int) {
	t.Helper()
	if string(ranked.Candidate.LaneID) != row.Order[index] || !ranked.Eligible || len(ranked.Terms) != 6 {
		t.Fatal(ranked)
	}
	if math.Abs(ranked.Utility-row.Utility) > 1e-12 {
		t.Fatalf("utility=%g want=%g", ranked.Utility, row.Utility)
	}
	for j, term := range ranked.Terms {
		if math.Abs(term.Contribution-row.Contributions[j]) > 1e-12 {
			t.Fatal(term, row.Contributions)
		}
	}
}

// TestRankTieBreakLaneID checks ties independent of input order and seed.
func TestRankTieBreakLaneID(t *testing.T) {
	r, a := rankFixture()
	b := a
	b.LaneID = "b"
	for _, seed := range []int64{1, 99} {
		r.Jitter = rand.NewSource(seed)
		got, err := Rank(r, []Candidate{b, a})
		if err != nil || got.Order[0].Candidate.LaneID != "a" {
			t.Fatal(got, err)
		}
	}
}

// TestRankSixthTerm proves rate-limit risk changes the winner.
func TestRankSixthTerm(t *testing.T) {
	r, a := rankFixture()
	b := a
	b.LaneID = "b"
	a.EWMA429 = 0.2
	got, err := Rank(r, []Candidate{a, b})
	if err != nil || got.Order[0].Candidate.LaneID != "b" {
		t.Fatal(got, err)
	}
	if got.Order[1].Terms[5].Contribution != -0.4 {
		t.Fatal(got)
	}
}

// TestEligibleNineOrdered checks all verdicts, including after a failure.
func TestEligibleNineOrdered(t *testing.T) {
	r, a := rankFixture()
	b := a
	b.LaneID = "0"
	b.ModelAvailable = false
	got, err := Rank(r, []Candidate{b, a})
	if err != nil || !got.Order[0].Eligible || got.Order[1].Eligible {
		t.Fatal(got, err)
	}
	for _, ranked := range got.Order {
		if len(ranked.Verdicts) != 9 {
			t.Fatal(ranked)
		}
		for i, p := range EligibilityPredicates() {
			if ranked.Verdicts[i].Predicate != p.Name {
				t.Fatal(ranked)
			}
		}
	}
	if len(got.Order[1].Terms) != 0 {
		t.Fatal("ineligible utility evaluated")
	}
}

// TestRolePartition checks removal before evaluation, fallback and unknown roles.
func TestRolePartition(t *testing.T) {
	r, a := rankFixture()
	b := a
	b.LaneID = "b"
	b.Roles = []topology.Role{topology.RoleTester}
	b.ExpectedQuality = 100
	r.Envelope = func(c Candidate) (bool, string) {
		if c.LaneID == "b" {
			t.Fatal("off-partition evaluated")
		}
		return true, ""
	}
	got, err := Rank(r, []Candidate{b, a})
	if err != nil || len(got.Order) != 1 || got.RoleFallback {
		t.Fatal(got, err)
	}
	r.Envelope = nil
	r.Roles = []topology.Role{topology.RoleCritic}
	got, err = Rank(r, []Candidate{b, a})
	if err != nil || !got.RoleFallback || len(got.Order) != 2 {
		t.Fatal(got, err)
	}
	r.Roles = []topology.Role{"unknown"}
	got, err = Rank(r, []Candidate{a})
	if err != nil || len(got.Order) != 0 || got.RoleFallback {
		t.Fatal(got, err)
	}
}

func requireRankError(t *testing.T, err error, kind cascade.Kind, message string) {
	t.Helper()
	got, ok := cascade.KindOf(err)
	if !ok || got != kind || err.Error() != message {
		t.Fatalf("error=%v kind=%v want=%v %s", err, got, kind, message)
	}
}

// TestRankRegistryExtendSealed checks append order, duplicates and first-use sealing.
func TestRankRegistryExtendSealed(t *testing.T) {
	previous := laneRankRegistry
	laneRankRegistry = newRankRegistry()
	t.Cleanup(func() { laneRankRegistry = previous })
	p := Predicate{Name: "extension", Eval: func(RankRequest, Candidate) Verdict { return rankVerdict("extension", true, "") }}
	term := UtilityTerm{Name: "extension", Eval: func(RankRequest, Candidate, Weights) Term { return Term{Name: "extension", Contribution: 3} }}
	if err := RegisterPredicate(p); err != nil {
		t.Fatal(err)
	}
	if err := RegisterTerm(term); err != nil {
		t.Fatal(err)
	}
	requireRankError(t, RegisterPredicate(p), cascade.KindConflict, "conflict: economics: duplicate rank predicate")
	requireRankError(t, RegisterTerm(term), cascade.KindConflict, "conflict: economics: duplicate rank term")
	r, c := rankFixture()
	got, err := Rank(r, []Candidate{c})
	if err != nil || len(got.Order[0].Verdicts) != 10 || len(got.Order[0].Terms) != 7 || got.Order[0].Utility != 3 {
		t.Fatal(got, err)
	}
	requireRankError(t, RegisterPredicate(p), cascade.KindConflict, "conflict: economics: rank registry sealed")
	requireRankError(t, RegisterTerm(term), cascade.KindConflict, "conflict: economics: rank registry sealed")
}

// TestRankNilJitterRefused checks required injection and invalid mode identity.
func TestRankNilJitterRefused(t *testing.T) {
	r, c := rankFixture()
	if _, err := Rank(r, []Candidate{c}); err != nil {
		t.Fatal(err)
	}
	r.Jitter = nil
	_, err := Rank(r, []Candidate{c})
	requireRankError(t, err, cascade.KindInvalidInput, "invalid-input: economics: rank requires jitter")
	r.Jitter = rand.NewSource(1)
	r.Mode = "unknown"
	_, err = Rank(r, []Candidate{c})
	if !errors.Is(err, ErrUnknownMode) {
		t.Fatal(err)
	}
	requireRankError(t, err, cascade.KindInvalidInput, "invalid-input: economics: unknown scheduler mode \"unknown\": invalid-input: economics: unknown scheduler mode")
}

// TestLearnedPairsContractAndFallback checks real pair arguments and static fallback.
func TestLearnedPairsContractAndFallback(t *testing.T) {
	r, a := rankFixture()
	b := a
	b.LaneID = "b"
	b.ExpectedQuality = 0.01
	r.Producer = &topology.ModelIdentity{CanonicalID: "producer", Family: "other"}
	baseline, err := Rank(r, []Candidate{a, b})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	r.LearnedPairs = func(p topology.ModelIdentity, pp string, c topology.ModelIdentity, cp, task string, verify bool) (float64, bool) {
		calls++
		if p != *r.Producer || pp != "" || c != a.Identity || cp != a.Provider || task != r.TaskClass || verify {
			t.Fatal("wrong pair arguments")
		}
		return 0, false
	}
	got, err := Rank(r, []Candidate{a, b})
	if err != nil || calls != 2 || !reflect.DeepEqual(got, baseline) {
		t.Fatal(got, err, calls)
	}
	r.LearnedPairs = func(topology.ModelIdentity, string, topology.ModelIdentity, string, string, bool) (float64, bool) {
		return 0.9, true
	}
	got, err = Rank(r, []Candidate{a, b})
	if err != nil || got.Order[0].Correlation != 0.9 || got.Order[0].Utility >= baseline.Order[0].Utility {
		t.Fatal(got, err)
	}
	zero := 0.0
	r.IndependenceWeight = &zero
	got, err = Rank(r, []Candidate{a})
	if err != nil || got.Order[0].Correlation != 0.9 || got.Order[0].Terms[2].Contribution != 0 {
		t.Fatal(got, err)
	}
}

// TestEconomicsImportsNoCapacity protects the economics dependency boundary.
func TestEconomicsImportsNoCapacity(t *testing.T) {
	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".go") || strings.HasSuffix(file.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), file.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			if strings.Contains(imp.Path.Value, "/capacity") {
				t.Fatal(file.Name(), imp.Path.Value)
			}
		}
	}
}
