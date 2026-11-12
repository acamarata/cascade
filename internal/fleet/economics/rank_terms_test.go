package economics

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestUtilityTermsFormula checks all six contributions and table validation.
func TestUtilityTermsFormula(t *testing.T) {
	for _, mode := range Modes() {
		for _, row := range []struct {
			name           string
			price, latency float64
		}{{"ordinary", 3, 0.4}, {"clamped", 0, 1.5}, {"negative-latency", 1, -0.2}} {
			t.Run(string(mode)+"/"+row.name, func(t *testing.T) {
				req := RankRequest{Mode: mode, Tables: NewTables(), Producer: &topology.ModelIdentity{CanonicalID: "a", Family: "family"}}
				c := Candidate{ExpectedQuality: 0.8, SP: ShadowPriceResult{ShadowPrice: row.price},
					Identity:   topology.ModelIdentity{CanonicalID: "b", Family: "family"},
					LatencyP50: time.Duration(row.latency * float64(10*time.Second)), PredictedFailure: 0.15, EWMA429: 0.1}
				w, err := req.Tables.Weights(mode)
				if err != nil {
					t.Fatal(err)
				}
				latency := math.Max(0, math.Min(1, row.latency))
				independence := 0.25
				if mode == ModeVerify {
					independence = 1
				}
				want := utilityFormulaWant(w, row.price, latency, independence)
				got, err := EvaluateUtilityTerms(req, c)
				if err != nil {
					t.Fatal(err)
				}
				assertUtilityTerms(t, got, want)
				for i, term := range BuiltinUtilityTerms() {
					if term.Name != want[i].Name {
						t.Fatalf("term name = %q", term.Name)
					}
					assertUtilityTerms(t, []Term{term.Eval(req, c, w)}, want[i:i+1])
				}
			})
		}
	}
	t.Run("invalid-tables", testUtilityInvalidTables)
	t.Run("overrides", testUtilityWeightOverrides)
}

func utilityFormulaWant(w Weights, price, latency, independence float64) []Term {
	return []Term{
		{"quality", w.Quality, 0.8, w.Quality * 0.8},
		{"shadow_price", w.Scarcity, math.Log1p(price), -w.Scarcity * math.Log1p(price)},
		{"correlation", w.Diversity, 0.75 * independence, -w.Diversity * 0.75 * independence},
		{"latency", w.Latency, latency, -w.Latency * latency},
		{"predicted_failure", w.Reliability, 0.15, -w.Reliability * 0.15},
		{"risk", w.Reliability, 0.2 + 0.20*latency, -w.Reliability * (0.2 + 0.20*latency)},
	}
}

func assertUtilityTerms(t *testing.T, got, want []Term) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("terms = %d, want %d", len(got), len(want))
	}
	for i, g := range got {
		w := want[i]
		if g.Name != w.Name || math.Abs(g.Weight-w.Weight) > 1e-12 || math.Abs(g.Value-w.Value) > 1e-12 || math.Abs(g.Contribution-w.Contribution) > 1e-12 || !isFinite(g.Contribution) {
			t.Errorf("term %d = %+v, want %+v", i, g, w)
		}
	}
}

func testUtilityInvalidTables(t *testing.T) {
	missing := NewTables()
	delete(missing.weights, ModeBuild)
	for _, req := range []RankRequest{{Mode: ModeBuild}, {Mode: ModeBuild, Tables: missing}, {Mode: "unknown", Tables: NewTables()}} {
		terms, err := EvaluateUtilityTerms(req, Candidate{})
		kind, ok := cascade.KindOf(err)
		message := "invalid-input: economics: unknown scheduler mode \"" + string(req.Mode) + "\": invalid-input: economics: unknown scheduler mode"
		if err == nil || !ok || kind != cascade.KindInvalidInput || !errors.Is(err, ErrUnknownMode) || err.Error() != message || len(terms) != 0 {
			t.Fatalf("invalid tables: terms=%v err=%v kind=%v", terms, err, kind)
		}
	}
}

func testUtilityWeightOverrides(t *testing.T) {
	tables, err := ApplyOverrides(NewTables(), Overrides{Weights: map[string]map[string]float64{
		"build": {"quality": 7, "scarcity": 3, "diversity": 2, "latency": 4, "reliability": 5},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := EvaluateUtilityTerms(RankRequest{Mode: ModeBuild, Tables: tables}, Candidate{ExpectedQuality: 1, LatencyP50: time.Second, PredictedFailure: 0.2, EWMA429: 0.1})
	if err != nil {
		t.Fatal(err)
	}
	assertUtilityTerms(t, got, []Term{{"quality", 7, 1, 7}, {"shadow_price", 3, 0, 0}, {"correlation", 2, 0, 0}, {"latency", 4, 0.1, -0.4}, {"predicted_failure", 5, 0.2, -1}, {"risk", 5, 0.22, -1.1}})
}

// TestDefaultIndependenceWeight checks defaults, strict unknowns and overrides.
func TestDefaultIndependenceWeight(t *testing.T) {
	for _, mode := range Modes() {
		for _, roles := range [][]topology.Role{nil, {topology.RoleCoder}, {topology.RoleCoder, topology.RoleAdversary}, {"unknown"}, {""}} {
			want := 0.25
			if mode == ModeVerify || len(roles) == 2 || (len(roles) == 1 && roles[0] != topology.RoleCoder) {
				want = 1
			}
			if got := DefaultIndependenceWeight(mode, roles); got != want {
				t.Errorf("%s %v = %g, want %g", mode, roles, got, want)
			}
		}
	}
	if DefaultIndependenceWeight("unknown", nil) != 1 || DefaultIndependenceWeight("", nil) != 1 {
		t.Fatal("unknown mode must fail closed")
	}
	for _, value := range []float64{0, 0.6, 1.5} {
		req := RankRequest{Mode: ModeVerify, Roles: []topology.Role{topology.RoleAdversary}, Tables: NewTables(),
			Producer: &topology.ModelIdentity{CanonicalID: "a", Family: "family"}, IndependenceWeight: &value}
		got, err := EvaluateUtilityTerms(req, Candidate{Identity: *req.Producer})
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(got[2].Value-0.9*value) > 1e-12 {
			t.Errorf("explicit %g: %+v", value, got[2])
		}
	}
}
