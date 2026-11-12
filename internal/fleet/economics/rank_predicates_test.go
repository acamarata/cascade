package economics

import (
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func predicateFixture() (RankRequest, Candidate) {
	req := RankRequest{Mode: ModeBuild, TaskClass: "code", Sensitivity: provider.SensitivityPublic,
		Roles: []topology.Role{topology.RoleCoder}, Modalities: []string{"text"}, Tools: []string{"shell"},
		Envelope: func(Candidate) (bool, string) { return true, "within envelope" }}
	cand := Candidate{
		LaneClass: topology.LaneClassWorkerDedicated, BasePrice: 1.1, ModelAvailable: true,
		WorkspacePermitted: true, Health: topology.LaneHealthAvailable,
		Modalities: []string{"text"}, Tools: []string{"shell"},
		Ceiling: topology.Ceiling{Tier: provider.SensitivityPublic, Reason: topology.CeilingBillingCap}}
	return req, cand
}

// TestDataClassPredicateCeilingErr checks unreadable ceilings and the request API.
func TestDataClassPredicateCeilingErr(t *testing.T) {
	r, c := predicateFixture()
	p := EligibilityPredicates()[3]
	if v := p.Eval(r, c); !v.Pass {
		t.Fatal(v)
	}
	c.CeilingErr = cascade.New(cascade.KindUnavailable, "unreadable")
	if v := p.Eval(r, c); v.Pass || v.Reason != "ceiling unavailable: unavailable" {
		t.Fatal(v)
	}
	c.CeilingErr, c.Ceiling = nil, topology.Ceiling{}
	if v := p.Eval(r, c); v.Pass || v.Reason == "" {
		t.Fatal(v)
	}
	f, err := parser.ParseFile(token.NewFileSet(), "rank_types.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "RankRequest" {
			return true
		}
		for _, field := range ts.Type.(*ast.StructType).Fields.List {
			for _, name := range field.Names {
				if name.Name == "Tier" {
					t.Error("RankRequest has Tier")
				}
				if name.Name == "Sensitivity" {
					found = true
				}
			}
		}
		return false
	})
	if !found {
		t.Fatal("missing Sensitivity")
	}
}

// TestCrunchPremiumAndReserveBars checks inclusive premium and reserve boundaries.
func TestCrunchPremiumAndReserveBars(t *testing.T) {
	for _, mode := range Modes() {
		for _, reserve := range []bool{false, true} {
			for _, floor := range []bool{false, true} {
				for _, allow := range []bool{false, true} {
					r, c := predicateFixture()
					r.Mode, r.AllowReserve = mode, allow
					c.AtOrBelowReserve, c.BelowAbsoluteFloor = reserve, floor
					want := allow || (!floor && (!reserve || mode == ModeIncident))
					v := EligibilityPredicates()[8].Eval(r, c)
					if v.Pass != want || (!want && v.Reason == "") {
						t.Fatalf("%s reserve=%t floor=%t allow=%t: %+v", mode, reserve, floor, allow, v)
					}
				}
			}
		}
	}
	for _, price := range []float64{4.99, 5, 12} {
		for _, allow := range []bool{false, true} {
			r, c := predicateFixture()
			r.Mode, r.AllowPremium, c.BasePrice = ModeCrunch, allow, price
			v := EligibilityPredicates()[8].Eval(r, c)
			if want := price < 5 || allow; v.Pass != want || (!want && v.Reason == "") {
				t.Fatal(price, allow, v)
			}
		}
	}
}

// TestPremiumPermitted checks explicit authorization in every request context.
func TestPremiumPermitted(t *testing.T) {
	for _, task := range []string{"code", "verify", "distill", "batch"} {
		for _, mode := range []Mode{ModeBuild, ModeVerify, ModeCrunch} {
			r, c := predicateFixture()
			r.TaskClass, r.Mode, c.BasePrice = task, mode, 5
			r.AllowPremium = true
			r.Premium = &PremiumPermit{AllowedRoles: r.Roles, MaxCalls: 2, CallsUsed: 1}
			if v := PremiumPermitted(r, c); !v.Pass || !v.Applicable {
				t.Fatal(v)
			}
			for _, change := range []func(*RankRequest){
				func(r *RankRequest) { r.AllowPremium = false },
				func(r *RankRequest) { r.Premium = nil },
				func(r *RankRequest) { r.Premium = &PremiumPermit{MaxCalls: 2} },
				func(r *RankRequest) { r.Premium = &PremiumPermit{AllowedRoles: r.Roles, MaxCalls: 1, CallsUsed: 1} },
			} {
				bad := r
				change(&bad)
				if v := PremiumPermitted(bad, c); v.Pass || v.Reason != ErrEnvelopeExceeded.Error() {
					t.Fatal(v)
				}
			}
		}
	}
	kind, ok := cascade.KindOf(ErrEnvelopeExceeded)
	if !ok || kind != cascade.KindPolicyDenied || ErrEnvelopeExceeded.Error() != "policy-denied: economics: envelope exceeded" {
		t.Fatal(ErrEnvelopeExceeded)
	}
	r, c := predicateFixture()
	if v := PremiumPermitted(r, c); !v.Pass || v.Applicable {
		t.Fatal(v)
	}
}

// TestEligibleEachPredicateFailsAlone isolates each of the nine ordered gates.
func TestEligibleEachPredicateFailsAlone(t *testing.T) {
	names := []string{"model-available", "task-class", "modality-tools", "data-class", "workspace-permission", "lane-health", "quota-reservable", "delegation-envelope", "mode-permits"}
	changes := []func(*RankRequest, *Candidate){
		func(_ *RankRequest, c *Candidate) { c.ModelAvailable = false },
		func(_ *RankRequest, c *Candidate) { c.TaskClasses = []string{"classify"} },
		func(_ *RankRequest, c *Candidate) { c.Tools = nil },
		func(r *RankRequest, _ *Candidate) { r.Sensitivity = provider.SensitivityInternal },
		func(_ *RankRequest, c *Candidate) { c.WorkspacePermitted = false },
		func(_ *RankRequest, c *Candidate) { c.Health = topology.LaneHealthQuarantined },
		func(_ *RankRequest, c *Candidate) { c.Reservable = ErrQuotaUnavailable },
		func(r *RankRequest, _ *Candidate) {
			r.Envelope = func(Candidate) (bool, string) { return false, "budget exhausted" }
		},
		func(r *RankRequest, _ *Candidate) { r.Mode = Mode("invalid") },
	}
	for i, change := range changes {
		t.Run(names[i], func(t *testing.T) {
			r, c := predicateFixture()
			predicates := EligibilityPredicates()
			if len(predicates) != 9 {
				t.Fatal(len(predicates))
			}
			for j, p := range predicates {
				if p.Name != names[j] || !p.Eval(r, c).Pass {
					t.Fatal(p.Name)
				}
			}
			change(&r, &c)
			for j, p := range predicates {
				v := p.Eval(r, c)
				if v.Predicate != names[j] || !v.Applicable || v.Pass != (i != j) || (!v.Pass && v.Reason == "") {
					t.Fatalf("%d: %+v", j, v)
				}
			}
		})
	}
}

// TestEnvelopePredicateNilNotApplicable checks the optional injected gate.
func TestEnvelopePredicateNilNotApplicable(t *testing.T) {
	r, c := predicateFixture()
	p := EligibilityPredicates()[7]
	if v := p.Eval(r, c); !v.Applicable || !v.Pass {
		t.Fatal(v)
	}
	r.Envelope = nil
	if v := p.Eval(r, c); v.Applicable || v.Reason == "" {
		t.Fatal(v)
	}
	r.Envelope = func(Candidate) (bool, string) { return false, "caller refused" }
	if v := p.Eval(r, c); !v.Applicable || v.Pass || v.Reason != "caller refused" {
		t.Fatal(v)
	}
}

// TestRankTransientDemotionIsUnhealthy ensures demotion overrides health.
func TestRankTransientDemotionIsUnhealthy(t *testing.T) {
	r, c := predicateFixture()
	p := EligibilityPredicates()[5]
	if v := p.Eval(r, c); !v.Pass {
		t.Fatal(v)
	}
	c.TransientDemoted = true
	if v := p.Eval(r, c); v.Pass || v.Reason == "" {
		t.Fatal(v)
	}
}

// TestRankPredicatesFailClosed pairs malformed inputs with admitted siblings.
func TestRankPredicatesFailClosed(t *testing.T) {
	for _, row := range []struct {
		index  int
		change func(*RankRequest, *Candidate)
	}{
		{1, func(r *RankRequest, _ *Candidate) { r.TaskClass = "unknown" }},
		{1, func(_ *RankRequest, c *Candidate) { c.LaneClass = "unknown" }},
		{1, func(_ *RankRequest, c *Candidate) { c.TaskClasses = []string{} }},
		{1, func(_ *RankRequest, c *Candidate) { c.LaneClass = topology.LaneClassUnranked }},
		{2, func(_ *RankRequest, c *Candidate) { c.Modalities = nil }},
		{3, func(r *RankRequest, _ *Candidate) { r.Sensitivity = 255 }},
		{5, func(_ *RankRequest, c *Candidate) { c.Health = "future" }},
		{8, func(_ *RankRequest, c *Candidate) { c.BasePrice = math.NaN() }},
		{8, func(_ *RankRequest, c *Candidate) { c.BasePrice = -1 }},
	} {
		r, c := predicateFixture()
		p := EligibilityPredicates()[row.index]
		if v := p.Eval(r, c); !v.Pass {
			t.Fatal(v)
		}
		row.change(&r, &c)
		if v := p.Eval(r, c); v.Pass || v.Reason == "" {
			t.Fatal(v)
		}
	}
	r, c := predicateFixture()
	c.LaneClass = topology.LaneClassUnranked
	for _, class := range topology.EligibleTaskClasses(c.LaneClass) {
		r.TaskClass = string(class)
		if v := EligibilityPredicates()[1].Eval(r, c); !v.Pass {
			t.Fatal(v)
		}
	}
}
