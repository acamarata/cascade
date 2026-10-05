package learn

// Purpose: ClassifyChange's tighten-only rule, op shapes and refusals, and
//   the link between the registry bound and internal/runtime's static
//   default.
// SPORT: learn/classify_test (P1-LRN-01).

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// emptyEnv is the injected process environment for runtime.Load.
func emptyEnv() runtime.LoadOptions {
	return runtime.LoadOptions{
		Getenv:  func(string) string { return "" },
		Environ: func() []string { return nil },
		Warn:    func(string, ...interface{}) {},
	}
}

// loadConfig runs runtime.Load over a config.toml under t.TempDir().
func loadConfig(t *testing.T, toml string) (*runtime.Config, error) {
	t.Helper()
	opts := emptyEnv()
	opts.Path = filepath.Join(t.TempDir(), "config.toml")
	if toml != "" {
		writeFile(t, opts.Path, toml)
	}
	return runtime.Load(context.Background(), opts)
}

func TestClassifyChangeTightenOnly(t *testing.T) {
	isolateHome(t)
	set := func(p FieldPath, v string) Change { return Change{Op: OpSet, Path: p, Value: v} }
	cases := []struct {
		target  string
		ch      Change
		tier    ConfigTier
		loosens bool
	}{
		{"ci.local_timeout", set("ci.local.timeout_seconds", "60"), TierSafe, false},
		{"ci.local_timeout", set("ci.local.timeout_seconds", "120"), TierSafe, false},
		{"ci.local_timeout", set("ci.local.timeout_seconds", "300"), TierSafe, false},
		{"ci.local_timeout", set("ci.local.timeout_seconds", "301"), TierSecurity, true},
		{"ci.local_timeout", set("ci.local.timeout_seconds", "59"), TierSecurity, true},
		{"ci.local_timeout", set("ci.local.timeout_seconds", "1e9"), TierSecurity, true},
		{"retrieval.fusion_k", set("retrieval.fusion.k", "1"), TierSafe, false},
		{"retrieval.fusion_k", set("retrieval.fusion.k", "200"), TierSafe, false},
		{"retrieval.fusion_k", set("retrieval.fusion.k", "0"), TierSecurity, true},
		{"retrieval.fusion_k", set("retrieval.fusion.k", "201"), TierSecurity, true},
		{"retrieval.fusion_weights", set("retrieval.fusion.weights.fts5", "-0.1"), TierSecurity, true},
		{"retrieval.fusion_k", Change{Op: OpAddStep, Path: "retrieval.fusion.k", Value: "5"}, TierSecurity, true},
		{"ci.local_steps", Change{Op: OpAddStep, Path: "ci.local.build", Value: `"cascade context generate --check"`}, TierBehavioral, false},
		{"ci.steps", Change{Op: OpAddStep, Path: "ci.local.lint", Value: `"cascade context generate --check"`}, TierBehavioral, false},
		{"ci.local_steps", set("ci.local.build", `["go build"]`), TierSecurity, true},
		{"ci.local_steps", Change{Op: OpReorder, Path: "ci.local.test", Value: `["a"]`}, TierSecurity, true},
		{"conductor.lane_order", Change{Op: OpReorder, Path: "conductor.lane_order", Value: `["lane-b", "lane-a"]`}, TierSafe, false},
		{"conductor.lane_order", set("conductor.lane_order", `["lane-a"]`), TierSecurity, true},
		{"egress.rules", set("egress.allow", `"*"`), TierSecurity, false},
	}
	for _, tc := range cases {
		cls, err := ClassifyChange(tc.target, tc.ch)
		if err != nil || cls.Tier != tc.tier || cls.LoosensBound != tc.loosens {
			t.Errorf("ClassifyChange(%s, %+v) = (%+v, %v), want tier %s loosens %v", tc.target, tc.ch, cls, err, tc.tier, tc.loosens)
		}
	}
	assertStaticDefaultIsLooseEnd(t)
	assertClassifyRefusals(t)
}

// assertStaticDefaultIsLooseEnd ties each limit bound's loose end to the
// loader's shipped default, so a default change without the registry fails.
func assertStaticDefaultIsLooseEnd(t *testing.T) {
	t.Helper()
	cfg, err := loadConfig(t, "")
	if err != nil {
		t.Fatalf("runtime.Load(empty) = %v", err)
	}
	limits := 0
	for _, tg := range Targets() {
		if tg.Direction == DirectionNotALimit {
			continue
		}
		limits++
		if tg.ID != "ci.local_timeout" || tg.Direction != DirectionLowerIsStricter {
			t.Fatalf("limit target %s has no static-default check", tg.ID)
		}
		if float64(cfg.CILocal.TimeoutSeconds) != tg.Bound.Max {
			t.Fatalf("ci.local_timeout loose end %v != loader default %d", tg.Bound.Max, cfg.CILocal.TimeoutSeconds)
		}
	}
	if limits != 1 {
		t.Fatalf("checked %d limit targets, want 1", limits)
	}
	k, ok := defaultRegistry.lookup("retrieval.fusion_k")
	if !ok || float64(cfg.FusionK()) < k.Bound.Min || float64(cfg.FusionK()) > k.Bound.Max {
		t.Fatalf("fusion.k default %d outside its bound", cfg.FusionK())
	}
}

// assertClassifyRefusals: unknown op, invalid or foreign path, unreadable
// value and unknown target each refuse.
func assertClassifyRefusals(t *testing.T) {
	t.Helper()
	cases := []struct {
		target string
		ch     Change
		kind   cascade.Kind
		msg    string
	}{
		{"ci.local_timeout", Change{Op: "delete", Path: "ci.local.timeout_seconds", Value: "60"},
			cascade.KindInvalidInput, "learn: change op must be set, add_step or reorder"},
		{"ci.local_timeout", Change{Op: "", Path: "ci.local.timeout_seconds", Value: "60"},
			cascade.KindInvalidInput, "learn: change op must be set, add_step or reorder"},
		{"ci.local_timeout", Change{Op: OpSet, Path: "ci.local.build", Value: "60"},
			cascade.KindInvalidInput, "learn: change path is outside the resolved target's paths"},
		{"retrieval.weights", Change{Op: OpSet, Path: "secrets.keychain_backend", Value: "1"},
			cascade.KindInvalidInput, "learn: change path is outside the resolved target's paths"},
		{"ci.local_timeout", Change{Op: OpSet, Path: "ci.local", Value: "60"},
			cascade.KindInvalidInput, "learn: change path is outside the resolved target's paths"},
		{"ci.local_timeout", Change{Op: OpSet, Path: "ci..local", Value: "60"}, cascade.KindInvalidInput, msgBadSegment},
		{"ci.local_timeout", Change{Op: OpSet, Path: "ci.local.timeout_seconds", Value: "nan"},
			cascade.KindInvalidInput, "learn: change value must be a TOML decimal number"},
		{"ci.local_timeout", Change{Op: OpSet, Path: "ci.local.timeout_seconds", Value: "0x10"},
			cascade.KindInvalidInput, "learn: change value must be a TOML decimal number"},
		{"ci.local_timeout", Change{Op: OpSet, Path: "ci.local.timeout_seconds", Value: "1e999"},
			cascade.KindInvalidInput, "learn: change value must be a finite number"},
		{"conductor.lane_order", Change{Op: OpReorder, Path: "conductor.lane_order", Value: "lane-a"},
			cascade.KindInvalidInput, "learn: change value must be a TOML array of lane names"},
		{"no.such", Change{Op: OpSet, Path: "a.b", Value: "1"}, cascade.KindNotFound, "learn: unknown learned-config target or alias"},
	}
	for _, tc := range cases {
		cls, err := ClassifyChange(tc.target, tc.ch)
		if cls.Tier != "" {
			t.Fatalf("ClassifyChange(%s, %+v) returned a tier beside its error", tc.target, tc.ch)
		}
		wantErr(t, err, tc.kind, tc.msg)
	}
	if loosens, err := loosensEnvelope(Target{Shape: "free"}, Change{Op: OpSet}); err != nil || !loosens {
		t.Fatal("an unknown shape must loosen (fail closed)")
	}
	if out, err := outsideBound(nil, "5"); err != nil || !out {
		t.Fatal("a numeric change with no bound must be outside it")
	}
}

// writeFile writes data to path with owner-only permissions.
func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// loadableFixture is a config.toml carrying one learnable path with a
// valid value, and a check that the loaded config holds it.
type loadableFixture struct {
	toml  string
	check func(c *runtime.Config) bool
}

// loadableFixtures covers every safe and behavioral path in the registry.
var loadableFixtures = map[FieldPath]loadableFixture{
	"retrieval.fusion.weights.*": {"[retrieval.fusion.weights]\nfts5 = 0.5\n",
		func(c *runtime.Config) bool { return c.FusionWeights()["fts5"] == 0.5 }},
	"retrieval.fusion.k": {"[retrieval.fusion]\nk = 120\n", func(c *runtime.Config) bool { return c.FusionK() == 120 }},
	"ci.local.timeout_seconds": {"[ci.local]\ntimeout_seconds = 120\n",
		func(c *runtime.Config) bool { return c.CILocal.TimeoutSeconds == 120 }},
	"conductor.lane_order": {"[conductor]\nlane_order = [\"lane-a\", \"lane-b\"]\n", func(c *runtime.Config) bool {
		sec, ok := c.Extra["conductor"].(map[string]interface{})
		order, isList := sec["lane_order"].([]interface{})
		return ok && isList && len(order) == 2
	}},
	"ci.local.lint":  {"[ci.local]\nlint = [\"go vet ./...\"]\n", func(c *runtime.Config) bool { return len(c.CILocal.Lint) == 1 }},
	"ci.local.test":  {"[ci.local]\ntest = [\"go test ./...\"]\n", func(c *runtime.Config) bool { return len(c.CILocal.Test) == 1 }},
	"ci.local.build": {"[ci.local]\nbuild = [\"go build ./...\"]\n", func(c *runtime.Config) bool { return len(c.CILocal.Build) == 1 }},
}

func TestTargetRegistryKeysLoadable(t *testing.T) {
	isolateHome(t)
	checked := 0
	for _, tg := range Targets() {
		if tg.Tier == TierSecurity {
			continue
		}
		for _, p := range tg.Paths {
			fx, ok := loadableFixtures[p]
			if !ok {
				t.Fatalf("learnable path %s has no loader fixture", p)
			}
			cfg, err := loadConfig(t, fx.toml)
			if err != nil || !fx.check(cfg) {
				t.Fatalf("runtime.Load did not accept %s: %v", p, err)
			}
			checked++
		}
	}
	if checked != len(loadableFixtures) {
		t.Fatalf("checked %d learnable paths, fixtures cover %d", checked, len(loadableFixtures))
	}
	for _, bogus := range []string{"[retrieval.fusion]\nbogus = 1\n", "[ci.local]\nbogus = 1\n", "[retrieval.fusion]\nrrf_k = 60\n"} {
		if _, err := loadConfig(t, bogus); err == nil {
			t.Errorf("runtime.Load accepted a bogus sibling key:\n%s", bogus)
		}
	}
	for _, key := range []string{"retrieval.chunk_size", "retrieval.fusion.rrf_k"} {
		if id, ok, err := TargetForConfigKey(key); ok || err != nil {
			t.Errorf("%s is a target (%q, %v)", key, id, err)
		}
	}
}

// spyView records the order of ClassifyChange's lookups.
type spyView struct{ calls []string }

func (s *spyView) resolve(n string) (TargetID, error) {
	s.calls = append(s.calls, "resolve")
	return defaultView{}.resolve(n)
}

func (s *spyView) lookup(id TargetID) (Target, bool) {
	s.calls = append(s.calls, "lookup")
	return defaultView{}.lookup(id)
}

func (s *spyView) denyMatch(p []FieldPath) ([]DenyRule, error) {
	s.calls = append(s.calls, "deny")
	return defaultView{}.denyMatch(p)
}

func (s *spyView) tierFor(p []FieldPath) ConfigTier {
	s.calls = append(s.calls, "tier")
	return defaultView{}.tierFor(p)
}

func TestResolveTargetBeforeDenylistAndTier(t *testing.T) {
	isolateHome(t)
	if id, err := ResolveTarget("auth_rules"); err != nil || id != "auth.rules" {
		t.Fatalf("ResolveTarget(auth_rules) = (%q, %v)", id, err)
	}
	spy := &spyView{}
	cls, err := classifyWith(spy, "auth_rules", Change{Op: OpSet, Path: "policy.auth.mode", Value: `"open"`})
	if err != nil || cls.Tier != TierSecurity || !slices.Equal(cls.Areas, []DenylistArea{AreaAuthRules}) {
		t.Fatalf("alias-dodge classification = (%+v, %v), want security [auth_rules]", cls, err)
	}
	if !slices.Equal(spy.calls, []string{"resolve", "lookup", "deny", "tier"}) {
		t.Fatalf("lookup order = %v, want resolve before deny and tier", spy.calls)
	}
	if id, err := ResolveTarget("retrieval.weights"); err != nil || id != "retrieval.fusion_weights" {
		t.Fatalf("ResolveTarget(retrieval.weights) = (%q, %v)", id, err)
	}
	cls, err = ClassifyChange("retrieval.weights", Change{Op: OpSet, Path: "retrieval.fusion.weights.fts5", Value: "0.5"})
	if err != nil || cls.Tier != TierSafe || cls.Target != "retrieval.fusion_weights" {
		t.Fatalf("bounded set through alias = (%+v, %v), want safe", cls, err)
	}
	const notFound = "learn: unknown learned-config target or alias"
	for _, name := range []string{"auth.rule", "AUTH_RULES", "", "retrieval"} {
		_, err := ResolveTarget(name)
		wantErr(t, err, cascade.KindNotFound, notFound)
		spy = &spyView{}
		_, err = classifyWith(spy, name, Change{Op: OpSet, Path: "policy.auth.mode", Value: "1"})
		wantErr(t, err, cascade.KindNotFound, notFound)
		if !slices.Equal(spy.calls, []string{"resolve"}) {
			t.Fatalf("unknown target %q reached %v", name, spy.calls)
		}
	}
}
