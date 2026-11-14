package main

import (
	"slices"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

func harnessBinaryCfg(paths ...interface{}) *runtime.Config {
	return &runtime.Config{Extra: map[string]interface{}{"fleet": map[string]interface{}{
		"harness_binary": paths, "discovery": map[string]interface{}{"interval": "2h"},
	}}}
}

func harnessBinaryList(t *testing.T, cfg *runtime.Config) []interface{} {
	t.Helper()
	fleet, _ := cfg.Extra["fleet"].(map[string]interface{})
	if _, ok := fleet["discovery"]; !ok {
		t.Fatal("write-back dropped an unguarded [fleet] key")
	}
	list, _ := fleet["harness_binary"].([]interface{})
	return list
}

func hasFleetLoosening(paths []runtime.LooseningPath) bool {
	for _, p := range paths {
		if p.Family == "fleet" {
			return true
		}
	}
	return false
}

func TestHarnessBinaryIsGuarded(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	if _, err := runtime.ResolveDottedPath("fleet.harness_binary"); err != nil {
		t.Fatalf("fleet.harness_binary is not a known key: %v", err)
	}
	if _, err := extractHarnessBinaryFamily(harnessBinaryCfg("/opt/h/bin", "rel/bin").Extra); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("relative entry: %v, want KindInvalidInput", err)
	}
	if raw, err := extractHarnessBinaryFamily(map[string]interface{}{}); err != nil || raw != nil {
		t.Fatalf("absent key: %s (err %v), want the default", raw, err)
	}
	base := runtime.EffectiveConfigOf(harnessBinaryCfg("/opt/h/bin"))
	cfg := harnessBinaryCfg("/opt/h/bin", "/opt/other/bin")
	eff := runtime.EffectiveConfigOf(cfg)
	if !hasFleetLoosening(runtime.CompareSecurity(base, eff)) {
		t.Fatal("an added harness binary is not a loosening of family fleet")
	}
	if !hasFleetLoosening(runtime.CompareSecurity(runtime.MostRestrictiveDefaults(), eff)) {
		t.Fatal("a harness binary over the defaults is not a loosening of family fleet")
	}
	runtime.ApplyEffective(cfg, base)
	if got := harnessBinaryList(t, cfg); !slices.Equal(got, []interface{}{"/opt/h/bin"}) {
		t.Fatalf("unaccepted entry survived write-back: %v", got)
	}
	runtime.ApplyEffective(cfg, eff)
	if got := harnessBinaryList(t, cfg); !slices.Equal(got, []interface{}{"/opt/h/bin", "/opt/other/bin"}) {
		t.Fatalf("accepted list not written back: %v", got)
	}
}
