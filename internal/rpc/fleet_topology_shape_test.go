package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestFleetTopologyDTOShape(t *testing.T) {
	want := map[string][]string{
		"LaneDTO": {"ID string", "RuntimeProfile string", "QuotaDomain string", "ModelID string", "Effort string", "LaneClass string",
			"InteractionClass string", "Health string", "HealthReason string", "BaseShadowPrice float64", "Ceiling string",
			"CeilingReason string", "Retired bool", "DiscoveredAt time.Time"},
		"BucketDTO": {"Name string", "Window string", "Source string", "Limit int64", "RemainingFraction float64", "Confidence float64",
			"ResetAt time.Time", "ObservedAt time.Time"},
		"DomainDTO":      {"ID string", "Kind string", "BillingTier string", "Quarantined bool", "Holders int", "Buckets []BucketDTO"},
		"ProfileDTO":     {"ID string", "Runtime string", "Persistent bool", "Local bool"},
		"LaneExplainDTO": {"Lane LaneDTO", "Profile ProfileDTO", "Domain DomainDTO"},
		"FleetTopologyDeps": {"Lanes func(context.Context, bool) ([]LaneDTO, error)", "Lane func(context.Context, string) (LaneExplainDTO, error)",
			"Domains func(context.Context) ([]DomainDTO, error)", "Domain func(context.Context, string) (DomainDTO, error)",
			"Scan func([]byte) error"},
	}
	f, err := parser.ParseFile(token.NewFileSet(), "fleet_topology_dto.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, sp := range gd.Specs {
			ts := sp.(*ast.TypeSpec)
			st := ts.Type.(*ast.StructType)
			for _, fld := range st.Fields.List {
				for _, n := range fld.Names {
					got[ts.Name.Name] = append(got[ts.Name.Name], n.Name+" "+topoTypeString(fld.Type))
				}
			}
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dto shape drift:\n got %v\nwant %v", got, want)
	}
	for _, v := range []any{LaneDTO{}, BucketDTO{}, DomainDTO{}, ProfileDTO{}, LaneExplainDTO{}} {
		topoCredentialFree(t, reflect.TypeOf(v))
	}
}

func topoTypeString(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return topoTypeString(x.X) + "." + x.Sel.Name
	case *ast.ArrayType:
		return "[]" + topoTypeString(x.Elt)
	case *ast.FuncType:
		var in, out []string
		for _, p := range x.Params.List {
			for range max(1, len(p.Names)) {
				in = append(in, topoTypeString(p.Type))
			}
		}
		for _, p := range x.Results.List {
			out = append(out, topoTypeString(p.Type))
		}
		res := strings.Join(out, ", ")
		if len(out) > 1 {
			res = "(" + res + ")"
		}
		return "func(" + strings.Join(in, ", ") + ") " + res
	}
	return "?"
}

func topoCredentialFree(t *testing.T, ty reflect.Type) {
	t.Helper()
	bad := []string{"secret", "password", "token", "apikey", "credential", "privatekey", "accesskey", "bearer", "authorization", "keyref"}
	for i := range ty.NumField() {
		f := ty.Field(i)
		name := strings.ToLower(f.Name + " " + strings.ReplaceAll(f.Tag.Get("json"), "_", ""))
		for _, b := range bad {
			if strings.Contains(name, b) {
				t.Fatalf("%s.%s looks like a credential field", ty.Name(), f.Name)
			}
		}
	}
}

func TestProviderErrorsSanitizedAtIngress(t *testing.T) {
	for _, kind := range []cascade.Kind{cascade.KindNotFound, cascade.KindTimeout} {
		g := &topoRig{depsErr: cascade.New(kind, "upstream said "+topoCanary)}
		r := g.registry(t)
		for _, m := range topoMethods {
			_, eo := topoCall(topoOwner(), r, m, `{"id":"dom_1"}`)
			if eo == nil || eo.Code != cascade.NewRPCError(cascade.New(kind, "x")).Code {
				t.Fatalf("%s: kind lost: %v", m, eo)
			}
			b, _ := json.Marshal(eo)
			if strings.Contains(string(b), topoCanary) {
				t.Fatalf("%s: canary leaked: %s", m, b)
			}
		}
	}
	g := &topoRig{depsErr: errors.New("plain " + topoCanary)}
	r := g.registry(t)
	_, eo := topoCall(topoOwner(), r, "fleet.domains.list", ``)
	if eo == nil || eo.Code != cascade.RPCCodeUnavailable || strings.Contains(eo.Message, topoCanary) {
		t.Fatalf("plain error: %v", eo)
	}
}

func TestCredentialDetectorRunsBeforeJournal(t *testing.T) {
	g := &topoRig{}
	r := g.registry(t)
	topoOK(t, r, "fleet.domains.list", ``)
	if !reflect.DeepEqual(g.order, []string{"scan", "journal"}) {
		t.Fatalf("order: %v", g.order)
	}
	d := topoDomain()
	d.Kind = topoCanary
	deps := g.deps()
	deps.Domains = func(context.Context) ([]DomainDTO, error) { return []DomainDTO{d}, nil }
	r2 := NewRegistry()
	r2.SetAccessPolicy(nil)
	g.order, g.seen = nil, nil
	r2.Use(func(_ string, next HandlerFunc) HandlerFunc {
		return func(ctx context.Context, p json.RawMessage) (any, error) {
			res, err := next(ctx, p)
			g.order = append(g.order, "journal")
			g.seen = append(g.seen, toString(res), toString(err))
			return res, err
		}
	})
	if err := RegisterFleetTopology(r2, deps); err != nil {
		t.Fatal(err)
	}
	_, eo := topoCall(topoOwner(), r2, "fleet.domains.list", ``)
	if eo == nil || eo.Code != cascade.RPCCodeIntegrity {
		t.Fatalf("want integrity, got %v", eo)
	}
	b, _ := json.Marshal(eo)
	if strings.Contains(string(b), topoCanary) || strings.Contains(strings.Join(g.seen, "|"), topoCanary) {
		t.Fatalf("canary leaked: %s %v", b, g.seen)
	}
	if !reflect.DeepEqual(g.order, []string{"scan", "journal"}) {
		t.Fatalf("order: %v", g.order)
	}
}

func toString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case json.RawMessage:
		return string(x)
	case error:
		return x.Error()
	}
	return ""
}
