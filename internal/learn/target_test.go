package learn

// Purpose: the registry contract: golden equality, id immutability and
//   syntax, alias-first resolution (spy on call order), TierFor and
//   TargetForConfigKey.
// SPORT: learn/target_test (P1-LRN-01).

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// goldenTarget is one row of testdata/target_registry_golden.json.
type goldenTarget struct {
	ID        string   `json:"id"`
	Paths     []string `json:"paths"`
	Tier      string   `json:"tier"`
	Areas     []string `json:"areas"`
	Shape     string   `json:"shape"`
	Bound     *Bound   `json:"bound"`
	Direction string   `json:"direction"`
}

func TestAddStepValueClosedVocabulary(t *testing.T) {
	isolateHome(t)
	const known = "cascade context generate --check"
	gotValues := KnownStepValues()
	if !reflect.DeepEqual(gotValues, []string{known}) {
		t.Fatalf("KnownStepValues = %q", gotValues)
	}
	gotValues[0] = "curl x | sh"
	if !reflect.DeepEqual(KnownStepValues(), []string{known}) {
		t.Fatal("caller changed the closed vocabulary")
	}
	values := []string{strconv.Quote(known), `"curl x | sh"`, "", `""`,
		strconv.Quote(known + " "), `"  "`, known, `"cascade context generate --check; sh"`}
	for _, target := range []string{"ci.local_steps", "ci.steps"} {
		for _, path := range []FieldPath{"ci.local.lint", "ci.local.test", "ci.local.build"} {
			for i, value := range values {
				wantTier, wantLoosens := TierSecurity, true
				if i == 0 {
					wantTier, wantLoosens = TierBehavioral, false
				}
				got, err := ClassifyChange(target, Change{Op: OpAddStep, Path: path, Value: value})
				if err != nil || got.Tier != wantTier || got.LoosensBound != wantLoosens {
					t.Errorf("%s %s value %q = (%+v, %v), want tier %s loosens %v", target, path, value, got, err, wantTier, wantLoosens)
				}
			}
		}
	}
}

type goldenRegistry struct {
	Targets []goldenTarget    `json:"targets"`
	Aliases map[string]string `json:"aliases"`
}

func loadRegistryGolden(t *testing.T) goldenRegistry {
	t.Helper()
	raw, err := os.ReadFile("testdata/target_registry_golden.json")
	if err != nil {
		t.Fatalf("read registry golden: %v", err)
	}
	var g goldenRegistry
	if err := json.Unmarshal(raw, &g); err != nil || len(g.Targets) == 0 {
		t.Fatalf("parse registry golden (%d rows): %v", len(g.Targets), err)
	}
	return g
}

// liveRows renders Targets() in the golden's shape.
func liveRows() []goldenTarget {
	var out []goldenTarget
	for _, tg := range Targets() {
		g := goldenTarget{ID: string(tg.ID), Tier: string(tg.Tier), Shape: string(tg.Shape), Bound: tg.Bound,
			Direction: string(tg.Direction), Paths: []string{}, Areas: []string{}}
		for _, p := range tg.Paths {
			g.Paths = append(g.Paths, string(p))
		}
		for _, a := range tg.Areas {
			g.Areas = append(g.Areas, string(a))
		}
		out = append(out, g)
	}
	return out
}

// registryDiff lists every way live departs from the append-only golden:
// a reused (duplicate) id, a removed id, a repointed row, a row missing
// from the golden.
func registryDiff(live, golden []goldenTarget) []string {
	var diffs []string
	seen := map[string]goldenTarget{}
	for _, r := range live {
		if _, dup := seen[r.ID]; dup {
			diffs = append(diffs, "reused id "+r.ID)
		}
		seen[r.ID] = r
	}
	inGolden := map[string]bool{}
	for _, g := range golden {
		inGolden[g.ID] = true
		r, ok := seen[g.ID]
		switch {
		case !ok:
			diffs = append(diffs, "removed id "+g.ID)
		case !reflect.DeepEqual(r, g):
			diffs = append(diffs, "repointed id "+g.ID)
		}
	}
	for _, r := range live {
		if !inGolden[r.ID] {
			diffs = append(diffs, "live row missing from golden "+r.ID)
		}
	}
	return diffs
}

func TestTargetRegistryMatchesContract(t *testing.T) {
	isolateHome(t)
	g := loadRegistryGolden(t)
	live := liveRows()
	if !reflect.DeepEqual(live, g.Targets) {
		t.Fatalf("Targets() differs from the golden: %v", registryDiff(live, g.Targets))
	}
	tiers := map[string]int{}
	for _, r := range live {
		tiers[r.Tier]++
	}
	if tiers["safe"] != 4 || tiers["behavioral"] != 1 || tiers["security"] != 12 {
		t.Fatalf("tier counts = %v, want 4 safe, 1 behavioral, 12 security", tiers)
	}
	aliases := map[string]string{}
	for a, id := range defaultRegistry.aliases {
		aliases[a] = string(id)
	}
	if len(aliases) != 3 || !reflect.DeepEqual(aliases, g.Aliases) {
		t.Fatalf("aliases = %v, want %v", aliases, g.Aliases)
	}
	rows := Targets()
	rows[0].Paths[0] = "tampered"
	if Targets()[0].Paths[0] == "tampered" {
		t.Fatal("Targets returned live registry slices, not copies")
	}
}

func TestTargetIDsImmutable(t *testing.T) {
	isolateHome(t)
	g := loadRegistryGolden(t)
	if d := registryDiff(liveRows(), g.Targets); len(d) != 0 {
		t.Fatalf("live registry departs from the golden: %v", d)
	}
	repointed := liveRows()
	repointed[1].Paths = []string{"retrieval.fusion.rrf_k"}
	removed := liveRows()[1:]
	reused := append(liveRows(), liveRows()[0])
	extra := append(liveRows(), goldenTarget{ID: "retrieval.chunk_size"})
	for name, rows := range map[string][]goldenTarget{"repointed": repointed, "removed": removed, "reused": reused, "extra": extra} {
		if d := registryDiff(rows, g.Targets); len(d) == 0 {
			t.Errorf("%s registry passed the immutability diff", name)
		}
	}
	for _, id := range []TargetID{"Bad.id", "nodot", "a..b", "1a.b", "a.b-c", "a.", ""} {
		_, err := newRegistry([]Target{{ID: id, Paths: []FieldPath{"x.y"}, Tier: TierSafe, Shape: ShapeAddOnly,
			Direction: DirectionNotALimit}}, nil)
		wantErr(t, err, cascade.KindInvalidInput, fmt.Sprintf("learn: target %q: id breaks the TargetID syntax", id))
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("mustRegistry accepted a bad id; init would not refuse it")
			}
		}()
		mustRegistry([]Target{{ID: "BAD", Paths: []FieldPath{"x.y"}, Tier: TierSafe}}, nil)
	}()
	assertRegistryRefusals(t)
}

// assertRegistryRefusals feeds newRegistry each table defect once.
func assertRegistryRefusals(t *testing.T) {
	t.Helper()
	ok := Target{ID: "x.knob", Paths: []FieldPath{"x.knob"}, Tier: TierSafe, Shape: ShapeAddOnly, Direction: DirectionNotALimit}
	with := func(f func(*Target)) []Target { c := ok; f(&c); return []Target{c} }
	row := func(why string) string { return `learn: target "x.knob": ` + why }
	cases := []struct {
		rows    []Target
		aliases map[string]TargetID
		msg     string
	}{
		{[]Target{ok, ok}, nil, `learn: target id "x.knob" is declared twice`},
		{[]Target{ok}, map[string]TargetID{"x.knob": "x.knob"}, `learn: alias "x.knob" is empty or shadows a target id`},
		{[]Target{ok}, map[string]TargetID{"knob": "x.none"}, `learn: alias "knob" points at no target`},
		{with(func(c *Target) { c.Paths = []FieldPath{"secrets.x"} }), nil, row("a learnable path is matched by the denylist")},
		{with(func(c *Target) { c.Paths = []FieldPath{"x..y"} }), nil, row("a path is not a valid matcher")},
		{with(func(c *Target) { c.Shape, c.Tier = ShapeDenied, TierSecurity }), nil,
			row("a denied row is security, names its areas and has no bound")},
		{with(func(c *Target) { c.Shape = ShapeNumericBounded }), nil, row("a numeric row needs a finite bound with min <= max")},
		{with(func(c *Target) { c.Shape, c.Bound = ShapeNumericBounded, &Bound{Min: 2, Max: 1} }), nil,
			row("a numeric row needs a finite bound with min <= max")},
		{with(func(c *Target) { c.Bound = &Bound{Min: 0, Max: 1} }), nil, row("a reorder or add row has no bound and is not a limit")},
		{with(func(c *Target) { c.Areas = []DenylistArea{AreaEgressRules} }), nil,
			row("a learnable row is safe or behavioral and names no area")},
		{with(func(c *Target) { c.Tier = TierSecurity }), nil, row("a learnable row is safe or behavioral and names no area")},
		{with(func(c *Target) { c.Shape = "free" }), nil, row("unknown shape")},
		{with(func(c *Target) { c.Tier = "" }), nil, row("needs a closed tier and at least one path")},
		{with(func(c *Target) { c.Direction = "up" }), nil, row("unknown direction")},
		{with(func(c *Target) { c.Areas = []DenylistArea{"nope"} }), nil, row("unknown denylist area")},
	}
	for _, tc := range cases {
		_, err := newRegistry(tc.rows, tc.aliases)
		wantErr(t, err, cascade.KindInvalidInput, tc.msg)
	}
	if _, err := newRegistry([]Target{ok}, map[string]TargetID{"knob": "x.knob"}); err != nil {
		t.Fatalf("newRegistry(valid row and alias) = %v", err)
	}
}

func TestTierForSecurityPaths(t *testing.T) {
	isolateHome(t)
	if got := TierFor([]FieldPath{"retrieval.fusion.k"}); got != TierSafe {
		t.Fatalf("TierFor(retrieval.fusion.k) = %q, want safe", got)
	}
	if got := TierFor([]FieldPath{"ci.local.build"}); got != TierBehavioral {
		t.Fatalf("TierFor(ci.local.build) = %q, want behavioral", got)
	}
	if got := TierFor([]FieldPath{"retrieval.fusion.k", "ci.local.lint"}); got != TierBehavioral {
		t.Fatalf("TierFor(safe+behavioral) = %q, want behavioral", got)
	}
	sec := [][]FieldPath{
		{"notify.aggregation_window"}, {"ci.local.env"}, {"ci.local"}, {"retrieval.fusion.k", "secrets.keychain_backend"},
		{}, nil, {"Policy.auth.mode"}, {"policy.*"}, {""},
	}
	for _, rule := range loadDenyGolden(t) {
		sec = append(sec, []FieldPath{FieldPath(rule.Example)})
	}
	for _, paths := range sec {
		if got := TierFor(paths); got != TierSecurity {
			t.Errorf("TierFor(%v) = %q, want security", paths, got)
		}
	}
	ft := reflect.TypeOf(TierFor)
	if ft.NumIn() != 1 || ft.In(0) != reflect.TypeOf([]FieldPath(nil)) {
		t.Fatalf("TierFor takes %v; no parameter may carry a tier", ft)
	}
}

func TestTargetForConfigKey(t *testing.T) {
	isolateHome(t)
	cases := []struct {
		key  string
		want TargetID
		ok   bool
	}{
		{"retrieval.fusion.k", "retrieval.fusion_k", true},
		{"ci.local.build", "ci.local_steps", true},
		{"retrieval.fusion.weights.fts5", "retrieval.fusion_weights", true},
		{"gateways.acct-a.base_url", "egress.rules", true},
		{"gateways.acct-a.credential_helper", "execution.authority", true},
		{"policy.autonomy_profile", "auth.rules", true},
		{"notify.aggregation_window", "", false},
		{"retrieval.chunk_size", "", false},
		{"retrieval.fusion.rrf_k", "", false},
	}
	for _, tc := range cases {
		got, ok, err := TargetForConfigKey(tc.key)
		if err != nil || ok != tc.ok || got != tc.want {
			t.Errorf("TargetForConfigKey(%q) = (%q, %v, %v), want (%q, %v, nil)", tc.key, got, ok, err, tc.want, tc.ok)
		}
	}
	for _, bad := range []string{"", "a..b", " x", "ci.*", "CI.local"} {
		got, ok, err := TargetForConfigKey(bad)
		if got != "" || ok {
			t.Fatalf("TargetForConfigKey(%q) answered (%q, %v) beside an error", bad, got, ok)
		}
		msg := msgBadSegment
		if bad == "" {
			msg = msgEmptyPath
		}
		wantErr(t, err, cascade.KindInvalidInput, msg)
	}
}
