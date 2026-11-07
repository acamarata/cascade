package economics

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"math/rand"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/pkg/provider"
)

// rankShape is contract:lane-rank: struct fields as "Name Type" in order,
// func types as their signature.
var rankShape = map[string][]string{
	"Candidate": {"LaneID topology.LaneID", "RegistryLane string", "DomainID topology.DomainID",
		"Provider string", "Identity topology.ModelIdentity", "LaneClass topology.LaneClass",
		"BasePrice float64", "SP ShadowPriceResult", "Roles []topology.Role", "TaskClasses []string",
		"Modalities []string", "Tools []string", "Ceiling topology.Ceiling", "CeilingErr error",
		"Health topology.LaneHealth", "TransientDemoted bool", "Reservable error",
		"AtOrBelowReserve bool", "BelowAbsoluteFloor bool", "WorkspacePermitted bool",
		"ModelAvailable bool", "ExpectedQuality float64", "PredictedFailure float64",
		"LatencyP50 time.Duration", "EWMA429 float64"},
	"PremiumPermit": {"AllowedRoles []topology.Role", "MaxCalls int", "CallsUsed int"},
	"EnvelopeFn":    {"func(c Candidate) (permitted bool, reason string)"},
	"LearnedPairsFn": {"func(producer topology.ModelIdentity, producerProvider string, " +
		"candidate topology.ModelIdentity, candidateProvider string, taskClass string, verify bool) " +
		"(penalty float64, ok bool)"},
	"RankRequest": {"Mode Mode", "TaskClass string", "Modalities []string", "Tools []string",
		"Sensitivity provider.SensitivityTier", "Roles []topology.Role", "Producer *topology.ModelIdentity",
		"ProducerSession string", "IndependenceWeight *float64", "AllowPremium bool", "AllowReserve bool",
		"Premium *PremiumPermit", "Envelope EnvelopeFn", "LearnedPairs LearnedPairsFn", "Tables Tables",
		"Jitter rand.Source"},
	"Verdict":     {"Predicate string", "Applicable bool", "Pass bool", "Reason string"},
	"Term":        {"Name string", "Weight float64", "Value float64", "Contribution float64"},
	"Ranked":      {"Candidate Candidate", "Eligible bool", "Verdicts []Verdict", "Terms []Term", "Correlation float64", "Utility float64"},
	"Ranking":     {"Order []Ranked", "RoleFallback bool"},
	"Predicate":   {"Name string", "Eval func(RankRequest, Candidate) Verdict"},
	"UtilityTerm": {"Name string", "Eval func(RankRequest, Candidate, Weights) Term"},
}

// parseRankShape returns each declared type's shape and whether it has a doc comment.
func parseRankShape(t *testing.T) (map[string][]string, map[string]bool) {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "rank_types.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse rank_types.go: %v", err)
	}
	got, doc := map[string][]string{}, map[string]bool{}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, s := range gd.Specs {
			ts := s.(*ast.TypeSpec)
			doc[ts.Name.Name] = (ts.Doc != nil && ts.Doc.Text() != "") || (gd.Doc != nil && gd.Doc.Text() != "")
			st, isStruct := ts.Type.(*ast.StructType)
			if !isStruct {
				got[ts.Name.Name] = []string{types.ExprString(ts.Type)}
				continue
			}
			var fields []string
			for _, fl := range st.Fields.List {
				for _, n := range fl.Names {
					fields = append(fields, n.Name+" "+types.ExprString(fl.Type))
				}
			}
			got[ts.Name.Name] = fields
		}
	}
	return got, doc
}

func TestRankTypesExactShape(t *testing.T) {
	got, doc := parseRankShape(t)
	if len(got) != len(rankShape) {
		t.Errorf("rank_types.go declares %d types, want %d: %v", len(got), len(rankShape), got)
	}
	for name, want := range rankShape {
		g, ok := got[name]
		if !ok {
			t.Errorf("type %s missing", name)
			continue
		}
		if strings.Join(g, "; ") != strings.Join(want, "; ") {
			t.Errorf("type %s shape\n got: %v\nwant: %v", name, g, want)
		}
		if !doc[name] {
			t.Errorf("type %s has no doc comment", name)
		}
	}
	for _, f := range got["RankRequest"] {
		if strings.HasPrefix(f, "Tier ") {
			t.Errorf("RankRequest has a field named Tier")
		}
	}
	if !strings.Contains(strings.Join(got["RankRequest"], ";"), "Sensitivity provider.SensitivityTier") {
		t.Errorf("RankRequest lacks Sensitivity")
	}
}

// fixtureRequest references every exported rank type so each name is used.
func fixtureRequest() (RankRequest, Predicate, UtilityTerm) {
	producer := topology.ModelIdentity{CanonicalID: "m-a", Family: "fam-a"}
	w := 0.5
	req := RankRequest{
		Mode: ModeCrunch, TaskClass: "build", Sensitivity: provider.SensitivityTier(1),
		Producer: &producer, IndependenceWeight: &w,
		Premium:  &PremiumPermit{AllowedRoles: []topology.Role{}, MaxCalls: 2},
		Envelope: EnvelopeFn(func(Candidate) (bool, string) { return true, "" }),
		LearnedPairs: LearnedPairsFn(func(topology.ModelIdentity, string, topology.ModelIdentity, string, string, bool) (float64, bool) {
			return 0, false
		}),
		Tables: Tables{}, Jitter: rand.NewSource(1),
	}
	pred := Predicate{Name: "health", Eval: func(RankRequest, Candidate) Verdict {
		return Verdict{Predicate: "health", Applicable: true, Pass: true}
	}}
	term := UtilityTerm{Name: "quality", Eval: func(RankRequest, Candidate, Weights) Term { return Term{Name: "quality"} }}
	return req, pred, term
}

func fixtureRanking() Ranking {
	terms := []Term{{Name: "quality", Weight: 1, Value: 0.8, Contribution: 0.8}, {Name: "price", Weight: 0.5, Value: -0.2, Contribution: -0.1}}
	good := Ranked{Candidate: Candidate{LaneID: "lane-a"}, Eligible: true, Terms: terms, Correlation: 0.1, Utility: 0.6,
		Verdicts: []Verdict{{Predicate: "health", Applicable: true, Pass: true}, {Predicate: "ceiling", Applicable: true, Pass: true}}}
	bad := Ranked{Candidate: Candidate{LaneID: "lane-b", CeilingErr: errors.New("unknown lane")}, Terms: terms, Utility: 0.7,
		Verdicts: []Verdict{{Predicate: "health", Applicable: true, Pass: true}, {Predicate: "ceiling", Applicable: true, Reason: "ceiling read failed"}}}
	return Ranking{Order: []Ranked{good, bad}}
}

func TestExplainEligibleAndIneligible(t *testing.T) {
	req, pred, term := fixtureRequest()
	if v := pred.Eval(req, Candidate{}); !v.Pass || term.Eval(req, Candidate{}, Weights{}).Name != "quality" {
		t.Fatalf("fixture evals wrong: %+v", v)
	}
	rk := fixtureRanking()
	bad := ExplainCandidate(rk.Order[1])
	want := []string{"lane lane-b eligible=no", "health: pass", "ceiling: fail ceiling read failed",
		"quality w=1 v=0.8 c=0.8", "price w=0.5 v=-0.2 c=-0.1", "correlation=0 utility=0.7"}
	if bad != strings.Join(want, "\n") {
		t.Errorf("ExplainCandidate:\n got %q\nwant %q", bad, strings.Join(want, "\n"))
	}
	good := ExplainCandidate(rk.Order[0])
	if !strings.HasPrefix(good, "lane lane-a eligible=yes\n") || good != ExplainCandidate(rk.Order[0]) {
		t.Errorf("eligible explain wrong or unstable: %q", good)
	}
	out := ExplainRanking(rk)
	wantAll := "ranking candidates=2 role_fallback=false\n" + good + "\n\n" + bad
	if out == "" || out != wantAll || out != ExplainRanking(rk) {
		t.Errorf("ExplainRanking:\n got %q\nwant %q", out, wantAll)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasSuffix(line, " ") {
			t.Errorf("trailing space in %q", line)
		}
	}
	na := verdictLine(Verdict{Predicate: "premium", Reason: "no permit"})
	if na != "premium: n/a no permit" {
		t.Errorf("n/a verdict = %q", na)
	}
}

func TestExplainEmptyRanking(t *testing.T) {
	if got := ExplainRanking(Ranking{}); got != "ranking candidates=0 role_fallback=false" {
		t.Errorf("empty ranking = %q", got)
	}
}
