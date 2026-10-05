package capacity

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/providers/registry"
)

func TestRegistryTierProjectionMap(t *testing.T) {
	rows := map[string]struct {
		in  registry.Tier
		out Tier
	}{
		"TierStrongest": {registry.TierStrongest, TierZero},
		"TierStrong":    {registry.TierStrong, TierZero},
		"TierMid":       {registry.TierMid, TierOne},
		"TierCheap":     {registry.TierCheap, TierTwo},
		"TierCheapest":  {registry.TierCheapest, TierTwo},
		"TierFree":      {registry.TierFree, TierTwo},
	}
	for name, row := range rows {
		t.Run(name, func(t *testing.T) {
			if got := registryTier(row.in); got != row.out {
				t.Fatalf("registryTier(%q) = %q, want %q", row.in, got, row.out)
			}
		})
	}
	for _, in := range []registry.Tier{"", "future-tier"} {
		if got := registryTier(in); got != "" {
			t.Errorf("unknown %q mapped to %q", in, got)
		}
	}
	declared := registryTierNames(t)
	if len(declared) != len(rows) {
		t.Fatalf("declared tiers %v differ from %v", declared, rows)
	}
	for _, name := range declared {
		if _, ok := rows[name]; !ok {
			t.Errorf("registry constant %s has no table row", name)
		}
	}
}

// registryTierNames detects additions to the registry's closed vocabulary.
func registryTierNames(t *testing.T) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "../../providers/registry/schema.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		isTier := false
		for _, spec := range gen.Specs {
			v := spec.(*ast.ValueSpec)
			if v.Type != nil || len(v.Values) != 0 {
				id, ok := v.Type.(*ast.Ident)
				isTier = ok && id.Name == "Tier"
			}
			if isTier {
				for _, n := range v.Names {
					names = append(names, n.Name)
				}
			}
		}
	}
	if len(names) == 0 {
		t.Fatal("no registry tier constants found")
	}
	return names
}

func TestProjectTiersFromSnapshot(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	lanes := map[conductor.LaneID]ProviderSlot{
		"a":        {Tier: TierZero, State: StateExhausted, ProfileRef: "busy", UpdatedAt: now.Add(time.Hour)},
		"b":        {Tier: TierZero, State: StateAvailable, ProfileRef: "first", UpdatedAt: now},
		"c":        {Tier: TierZero, State: StateAvailable, ProfileRef: "second", UpdatedAt: now},
		"d":        {Tier: TierOne, State: StateUnknown, ProfileRef: "unknown", UpdatedAt: now},
		"e":        {Tier: TierOne, State: StateConstrained, ProfileRef: "limited", UpdatedAt: now},
		"f":        {Tier: TierTwo, State: StateAuthRequired, ProfileRef: "auth", UpdatedAt: now},
		"g":        {Tier: TierTwo, State: StateExhausted, ProfileRef: "empty", UpdatedAt: now},
		"unmapped": {State: StateAvailable, ProfileRef: "unmapped", UpdatedAt: now},
		"excluded": {Tier: TierOne, State: StateAvailable, ProfileRef: "excluded", UpdatedAt: now.Add(2 * time.Hour)},
	}
	snap := FleetSnapshot{Lanes: lanes, Providers: map[string]ProviderSlot{"legacy": {Tier: TierZero, State: StateAvailable}}}
	eligible := []conductor.LaneID{"unmapped", "g", "e", "c", "f", "b", "d", "a", "missing", "b"}
	want := map[Tier]TierSlot{
		TierZero: {Providers: []conductor.LaneID{"a", "b", "c"}, State: StateAvailable, ProfileID: "first", Source: SourceProviderStatus, ObservedAt: now.Add(time.Hour)},
		TierOne:  {Providers: []conductor.LaneID{"d", "e"}, State: StateConstrained, ProfileID: "limited", Source: SourceProviderStatus, ObservedAt: now},
		TierTwo:  {Providers: []conductor.LaneID{"f", "g"}, State: StateAuthRequired, ProfileID: "auth", Source: SourceProviderStatus, ObservedAt: now},
	}
	if got := ProjectTiers(snap, eligible); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	for _, ids := range [][]conductor.LaneID{nil, {}, {"missing"}, {"unmapped"}} {
		if got := ProjectTiers(snap, ids); len(got) != 0 {
			t.Errorf("eligible %v projected %#v", ids, got)
		}
	}
	if got := ProjectTiers(snap, []conductor.LaneID{"e"}); len(got) != 1 || got[TierOne].ProfileID != "limited" {
		t.Fatalf("empty tiers not absent: %#v", got)
	}
}
