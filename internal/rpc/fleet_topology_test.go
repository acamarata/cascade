package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/pkg/cascade"
)

var topoCanary = "CANARY-" + "topo"

var topoMethods = []string{"fleet.lanes.list", "fleet.lanes.show", "fleet.lanes.explain", "fleet.domains.list", "fleet.domains.show"}

type topoRig struct {
	order   []string
	seen    []string
	depsErr error
	empty   bool
}

func topoDomain() DomainDTO {
	return DomainDTO{ID: "dom_1", Kind: "subscription", BillingTier: "max", Holders: 2, Buckets: []BucketDTO{{
		Name: "requests", Window: "5h", Source: "provider", Limit: 100, RemainingFraction: 0.5, Confidence: 0.9,
		ResetAt: time.Unix(900, 0).UTC(), ObservedAt: time.Unix(800, 0).UTC()}}}
}

func topoLane(id string, retired bool) LaneDTO {
	return LaneDTO{ID: id, RuntimeProfile: "work", QuotaDomain: "dom_1", ModelID: "m1", Effort: "high", LaneClass: "interactive",
		InteractionClass: "chat", Health: "ok", HealthReason: "fine", BaseShadowPrice: 1.5, Ceiling: "internal",
		CeilingReason: "policy", Retired: retired, DiscoveredAt: time.Unix(700, 0).UTC()}
}

func (g *topoRig) deps() FleetTopologyDeps {
	known := func(id string) error {
		if g.depsErr != nil {
			return g.depsErr
		}
		if id != "ln_1" && id != "ln_2" && id != "dom_1" {
			return cascade.New(cascade.KindNotFound, "no such id "+id)
		}
		return nil
	}
	return FleetTopologyDeps{
		Lanes: func(_ context.Context, inc bool) ([]LaneDTO, error) {
			if g.depsErr != nil || g.empty {
				return nil, g.depsErr
			}
			out := []LaneDTO{topoLane("ln_1", false)}
			if inc {
				out = append(out, topoLane("ln_2", true))
			}
			return out, nil
		},
		Lane: func(_ context.Context, id string) (LaneExplainDTO, error) {
			return LaneExplainDTO{Lane: topoLane(id, false), Profile: ProfileDTO{ID: "work", Runtime: "claude", Persistent: true, Local: false},
				Domain: topoDomain()}, known(id)
		},
		Domains: func(context.Context) ([]DomainDTO, error) {
			if g.depsErr != nil || g.empty {
				return nil, g.depsErr
			}
			return []DomainDTO{topoDomain()}, nil
		},
		Domain: func(_ context.Context, id string) (DomainDTO, error) { return topoDomain(), known(id) },
		Scan: func(p []byte) error {
			g.order = append(g.order, "scan")
			if strings.Contains(string(p), topoCanary) {
				return errors.New("found " + topoCanary)
			}
			return nil
		},
	}
}

func (g *topoRig) registry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	r.SetAccessPolicy(nil)
	r.Use(func(_ string, next HandlerFunc) HandlerFunc {
		return func(ctx context.Context, p json.RawMessage) (any, error) {
			res, err := next(ctx, p)
			g.order = append(g.order, "journal")
			if raw, ok := res.(json.RawMessage); ok {
				g.seen = append(g.seen, string(raw))
			}
			if err != nil {
				g.seen = append(g.seen, err.Error())
			}
			return res, err
		}
	})
	if err := RegisterFleetTopology(r, g.deps()); err != nil {
		t.Fatal(err)
	}
	return r
}

func topoCall(ctx context.Context, r *Registry, method, params string) (any, *ErrorObject) {
	return r.Dispatch(ctx, &Request{Method: method, Params: json.RawMessage(params)})
}

func topoOwner() context.Context {
	return principal.WithCaller(context.Background(), principal.Caller{})
}

func topoSession() context.Context {
	return principal.WithCaller(context.Background(), principal.Caller{Principal: &principal.Principal{ID: "s1", Kind: principal.KindSession}})
}

type topoEnv struct {
	Version string          `json:"version"`
	Data    json.RawMessage `json:"data"`
}

func topoOK(t *testing.T, r *Registry, method, params string) topoEnv {
	t.Helper()
	res, eo := topoCall(topoOwner(), r, method, params)
	if eo != nil {
		t.Fatalf("%s: %v", method, eo)
	}
	raw, ok := res.(json.RawMessage)
	if !ok {
		t.Fatalf("%s: result is %T", method, res)
	}
	var e topoEnv
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	return e
}

func topoRefused(t *testing.T, r *Registry, method, params string) {
	t.Helper()
	for _, ctx := range []context.Context{topoSession(), context.Background()} {
		if _, eo := topoCall(ctx, r, method, params); eo == nil || eo.Code != cascade.RPCCodePermissionDenied {
			t.Fatalf("%s: want permission denied, got %v", method, eo)
		}
	}
}

func TestFleetLanesRPC(t *testing.T) {
	g := &topoRig{}
	r := g.registry(t)
	var lanes []LaneDTO
	e := topoOK(t, r, "fleet.lanes.list", `{}`)
	if e.Version != "1" || json.Unmarshal(e.Data, &lanes) != nil || len(lanes) != 1 || lanes[0].ID != "ln_1" {
		t.Fatalf("list false: %+v %v", e, lanes)
	}
	e = topoOK(t, r, "fleet.lanes.list", `{"include_retired":true}`)
	lanes = nil
	if json.Unmarshal(e.Data, &lanes) != nil || len(lanes) != 2 || !lanes[1].Retired || lanes[0].ModelID != "m1" {
		t.Fatalf("list true: %v", lanes)
	}
	var lane LaneDTO
	e = topoOK(t, r, "fleet.lanes.show", `{"id":"ln_1"}`)
	if e.Version != "1" || json.Unmarshal(e.Data, &lane) != nil || lane != topoLane("ln_1", false) {
		t.Fatalf("show: %v", lane)
	}
	e = topoOK(t, r, "fleet.lanes.explain", `{"id":"ln_1"}`)
	var ex LaneExplainDTO
	if e.Version != "1" || json.Unmarshal(e.Data, &ex) != nil || ex.Lane.ID != "ln_1" || ex.Profile.ID != "work" {
		t.Fatalf("explain: %v", ex)
	}
	for _, m := range []string{"fleet.lanes.list", "fleet.lanes.show", "fleet.lanes.explain"} {
		topoRefused(t, r, m, `{"id":"ln_1"}`)
	}
	for _, m := range []string{"fleet.lanes.show", "fleet.lanes.explain"} {
		_, eo := topoCall(topoOwner(), r, m, `{"id":"nope"}`)
		if eo == nil || eo.Code != cascade.RPCCodeNotFound {
			t.Fatalf("%s unknown id: %v", m, eo)
		}
		if _, eo := topoCall(topoOwner(), r, m, `{"id":""}`); eo == nil || eo.Code != cascade.RPCCodeInvalidInput {
			t.Fatalf("%s empty id: %v", m, eo)
		}
		if _, eo := topoCall(topoOwner(), r, m, `{bad`); eo == nil || eo.Code != cascade.RPCCodeInvalidInput {
			t.Fatalf("%s malformed: %v", m, eo)
		}
	}
}

func TestFleetDomainsRPC(t *testing.T) {
	g := &topoRig{}
	r := g.registry(t)
	var doms []DomainDTO
	e := topoOK(t, r, "fleet.domains.list", ``)
	if e.Version != "1" || json.Unmarshal(e.Data, &doms) != nil || len(doms) != 1 || !reflect.DeepEqual(doms[0], topoDomain()) {
		t.Fatalf("list: %v", doms)
	}
	var d DomainDTO
	e = topoOK(t, r, "fleet.domains.show", `{"id":"dom_1"}`)
	if json.Unmarshal(e.Data, &d) != nil || !reflect.DeepEqual(d, topoDomain()) || d.Buckets[0].Source != "provider" {
		t.Fatalf("show: %v", d)
	}
	topoRefused(t, r, "fleet.domains.list", ``)
	topoRefused(t, r, "fleet.domains.show", `{"id":"dom_1"}`)
	if _, eo := topoCall(topoOwner(), r, "fleet.domains.show", `{"id":"x"}`); eo == nil || eo.Code != cascade.RPCCodeNotFound {
		t.Fatalf("unknown: %v", eo)
	}
	g.empty = true
	if e := topoOK(t, r, "fleet.domains.list", ``); string(e.Data) != "[]" {
		t.Fatalf("empty domains: %s", e.Data)
	}
	if e := topoOK(t, r, "fleet.lanes.list", ``); string(e.Data) != "[]" {
		t.Fatalf("empty lanes: %s", e.Data)
	}
	base := g.deps()
	nils := []func(*FleetTopologyDeps){
		func(d *FleetTopologyDeps) { d.Lanes = nil }, func(d *FleetTopologyDeps) { d.Lane = nil },
		func(d *FleetTopologyDeps) { d.Domains = nil }, func(d *FleetTopologyDeps) { d.Domain = nil },
		func(d *FleetTopologyDeps) { d.Scan = nil },
	}
	for i, nilOut := range nils {
		d := base
		nilOut(&d)
		reg := NewRegistry()
		err := RegisterFleetTopology(reg, d)
		if !cascade.HasKind(err, cascade.KindInvalidInput) || len(reg.Methods()) != 0 {
			t.Fatalf("nil dep %d: %v %v", i, err, reg.Methods())
		}
	}
	if !cascade.HasKind(RegisterFleetTopology(nil, base), cascade.KindInvalidInput) {
		t.Fatal("nil registry accepted")
	}
}

func jsonKeys(v any, out *[]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, c := range x {
			*out = append(*out, k)
			jsonKeys(c, out)
		}
	case []any:
		for _, c := range x {
			jsonKeys(c, out)
		}
	}
}

func TestFleetLanesExplain(t *testing.T) {
	r := (&topoRig{}).registry(t)
	e := topoOK(t, r, "fleet.lanes.explain", `{"id":"ln_1"}`)
	var ex LaneExplainDTO
	if err := json.Unmarshal(e.Data, &ex); err != nil {
		t.Fatal(err)
	}
	b := ex.Domain.Buckets
	if ex.Lane.Ceiling != "internal" || ex.Lane.CeilingReason != "policy" || ex.Profile != (ProfileDTO{ID: "work", Runtime: "claude", Persistent: true}) ||
		len(b) != 1 || b[0].Source != "provider" || b[0].Confidence != 0.9 || b[0].ObservedAt.IsZero() {
		t.Fatalf("explain: %+v", ex)
	}
	var generic any
	_ = json.Unmarshal(e.Data, &generic)
	var keys []string
	jsonKeys(generic, &keys)
	for _, k := range keys {
		if k != "base_shadow_price" && (strings.Contains(k, "price_term") || strings.Contains(k, "utility") || strings.Contains(k, "shadow")) {
			t.Fatalf("economics key %q in explain", k)
		}
	}
}

func TestFleetTopologyEnvelopeVersion(t *testing.T) {
	if FleetTopologyEnvelopeVersion != "1" {
		t.Fatal("version constant")
	}
	r := (&topoRig{}).registry(t)
	for _, m := range topoMethods {
		res, eo := topoCall(topoOwner(), r, m, `{"id":"ln_1"}`)
		if m == "fleet.domains.show" {
			res, eo = topoCall(topoOwner(), r, m, `{"id":"dom_1"}`)
		}
		if eo != nil {
			t.Fatalf("%s: %v", m, eo)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(res.(json.RawMessage), &raw); err != nil || len(raw) != 2 {
			t.Fatalf("%s: keys %v %v", m, raw, err)
		}
		if string(raw["version"]) != `"1"` || raw["data"] == nil {
			t.Fatalf("%s: %s", m, res)
		}
	}
}
