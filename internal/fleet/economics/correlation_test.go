package economics

import (
	"encoding/json"
	"math"
	"os"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/topology"
)

type correlationGoldenRow struct {
	Name             string                  `json:"name"`
	Producer         *topology.ModelIdentity `json:"producer"`
	ProducerProvider string                  `json:"producer_provider"`
	ProducerSession  string                  `json:"producer_session"`
	Candidate        Candidate               `json:"candidate"`
	Penalty          float64                 `json:"penalty"`
	Floor            float64                 `json:"floor"`
}

type correlationGolden struct {
	Correlation []correlationGoldenRow `json:"correlation"`
	Precedence  []correlationGoldenRow `json:"precedence"`
	Floors      map[string]float64     `json:"learned_floors"`
}

func loadCorrelationGolden(t *testing.T) correlationGolden {
	t.Helper()
	data, err := os.ReadFile("testdata/rank_table.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden correlationGolden
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatal(err)
	}
	if len(golden.Correlation) != 7 || len(golden.Precedence) != 5 || len(golden.Floors) != 4 {
		t.Fatal("missing correlation, precedence or learned floor rows")
	}
	return golden
}

// TestCorrelationPenalty pins all six structural cells and the two-host row.
func TestCorrelationPenalty(t *testing.T) {
	for _, row := range loadCorrelationGolden(t).Correlation {
		t.Run(row.Name, func(t *testing.T) {
			for _, verify := range []bool{false, true} {
				got := CorrelationPenalty(row.Producer, row.ProducerProvider, row.ProducerSession,
					"code", row.Candidate, nil, verify)
				if got != row.Penalty {
					t.Fatalf("verify=%v: got %g, want %g", verify, got, row.Penalty)
				}
			}
		})
	}
}

// TestCorrelationModelIdentity distinguishes canonical identity from aliases.
func TestCorrelationModelIdentity(t *testing.T) {
	p := topology.ModelIdentity{CanonicalID: "model-a", Family: "family-a"}
	c := Candidate{LaneID: "host-b", RegistryLane: "model-alias-b", Provider: "provider-y",
		Identity: p}
	if got := CorrelationPenalty(&p, "provider-x", "host-a", "code", c, nil, false); got != .7 {
		t.Fatalf("two hosts with one canonical identity: got %g, want .7", got)
	}
	c.Identity.CanonicalID = "model-b"
	if got := CorrelationPenalty(&p, "provider-x", "host-a", "code", c, nil, false); got != .75 {
		t.Fatalf("distinct canonical identity, same family: got %g, want .75", got)
	}
	c.Identity.Family = "family-b"
	c.RegistryLane = "model-a"
	if got := CorrelationPenalty(&p, "provider-x", "host-a", "code", c, nil, false); got != .3 {
		t.Fatalf("registry alias must not supply identity: got %g, want .3", got)
	}
}

// TestCorrelationPrecedence keeps incomplete identities ahead of learned data.
func TestCorrelationPrecedence(t *testing.T) {
	known := topology.ModelIdentity{CanonicalID: "model-a", Family: "family-a"}
	rows := []struct {
		name      string
		producer  *topology.ModelIdentity
		candidate topology.ModelIdentity
		want      float64
	}{
		{"nil producer", nil, topology.ModelIdentity{}, 0},
		{"unknown producer family", &topology.ModelIdentity{}, known, 1},
		{"unknown candidate family", &known, topology.ModelIdentity{}, 1},
		{"empty canonical", &known, topology.ModelIdentity{Family: "family-b"}, .75},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			c := Candidate{Provider: "provider-y", Identity: row.candidate}
			for _, verify := range []bool{false, true} {
				got := CorrelationPenalty(row.producer, "provider-x", "", "code", c, nil, verify)
				if got != row.want {
					t.Fatalf("verify=%v: got %g, want %g", verify, got, row.want)
				}
			}
		})
	}
	for _, row := range loadCorrelationGolden(t).Precedence {
		called := false
		learned := LearnedPairsFn(func(topology.ModelIdentity, string, topology.ModelIdentity, string, string, bool) (float64, bool) {
			called = true
			return .99, true
		})
		for _, verify := range []bool{false, true} {
			got := CorrelationPenalty(row.Producer, row.ProducerProvider, row.ProducerSession,
				"code", row.Candidate, learned, verify)
			if got != row.Penalty || called {
				t.Fatalf("%s: got %g, want %g; learned called=%v", row.Name, got, row.Penalty, called)
			}
		}
	}
}

// TestCorrelationDefaults retains family and diversity policy for unranked lanes.
func TestCorrelationDefaults(t *testing.T) {
	p := topology.ModelIdentity{CanonicalID: "model-a", Family: "family-a"}
	c := Candidate{Provider: "provider-y", LaneClass: topology.LaneClassUnranked,
		Identity: topology.ModelIdentity{CanonicalID: "model-b"}}
	if got := CorrelationPenalty(&p, "provider-x", "", "code", c, nil, true); got != 1 {
		t.Fatalf("unknown unranked family: got %g, want 1", got)
	}
	c.Identity.Family = "family-b"
	if got := CorrelationPenalty(&p, "provider-x", "", "code", c, nil, true); got != .3 {
		t.Fatalf("known unranked family: got %g, want .3", got)
	}
	weights, err := UtilityWeights(ModeVerify)
	if err != nil || weights.Diversity <= 0 {
		t.Fatalf("verify diversity weight=%g, err=%v", weights.Diversity, err)
	}
}

// TestCorrelationLearnedFloors checks replacement, missing data and lower bounds.
func TestCorrelationLearnedFloors(t *testing.T) {
	golden := loadCorrelationGolden(t)
	wantFloors := map[string]float64{"same_model": .8, "same_family": .5, "same_provider": .35, "different_provider": .15}
	for key, want := range wantFloors {
		if golden.Floors[key] != want {
			t.Fatalf("floor %s: got %g, want %g", key, golden.Floors[key], want)
		}
	}
	for _, row := range golden.Correlation {
		t.Run(row.Name, func(t *testing.T) { checkCorrelationLearned(t, row) })
	}
}

func checkCorrelationLearned(t *testing.T, row correlationGoldenRow) {
	t.Helper()
	for _, value := range []float64{-1, 0, row.Floor, .85, .98, 2, math.NaN(), math.Inf(1), math.Inf(-1)} {
		for _, ok := range []bool{false, true} {
			called := false
			learned := LearnedPairsFn(func(p topology.ModelIdentity, pp string, c topology.ModelIdentity, cp, task string, verify bool) (float64, bool) {
				called = true
				if p != *row.Producer || pp != row.ProducerProvider || c != row.Candidate.Identity || cp != row.Candidate.Provider || task != "code" || verify {
					t.Fatal("learned lookup received different inputs")
				}
				return value, ok
			})
			want := row.Penalty
			sameSession := row.ProducerSession != "" && row.ProducerSession == string(row.Candidate.LaneID)
			if ok && !sameSession {
				want = math.Min(1, math.Max(row.Floor, value))
				if math.IsNaN(value) || math.IsInf(value, 0) {
					want = 1
				}
			}
			got := CorrelationPenalty(row.Producer, row.ProducerProvider, row.ProducerSession, "code", row.Candidate, learned, false)
			if got != want {
				t.Fatalf("learned=%g ok=%v: got %g, want %g", value, ok, got, want)
			}
			if !called && row.Name != "same-session" {
				t.Fatal("learned lookup was skipped")
			}
		}
	}
}

// TestCorrelationLearnedNeverWeakensVerify covers verification and adversary roles.
func TestCorrelationLearnedNeverWeakensVerify(t *testing.T) {
	for _, row := range loadCorrelationGolden(t).Correlation {
		for _, adversary := range []bool{false, true} {
			c := row.Candidate
			verify := !adversary
			if adversary {
				c.Roles = []topology.Role{topology.RoleCoder, topology.RoleAdversary}
			}
			for _, value := range []float64{0, .95} {
				learned := LearnedPairsFn(func(topology.ModelIdentity, string, topology.ModelIdentity, string, string, bool) (float64, bool) {
					return value, true
				})
				got := CorrelationPenalty(row.Producer, row.ProducerProvider, row.ProducerSession, "code", c, learned, verify)
				want := math.Max(row.Floor, math.Max(row.Penalty, value))
				if got != want {
					t.Fatalf("%s adversary=%v learned=%g: got %g, want %g", row.Name, adversary, value, got, want)
				}
			}
		}
	}
}
