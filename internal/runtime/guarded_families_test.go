package runtime

// Purpose: the guarded family registry and the FieldSpec rules: registration
// makes Keys known, an Extract error is the invalid marker (never a
// tightening, never kept by a degraded boot), each rule orders values as
// documented, Restrict keeps the tighter of default and value key by key,
// ApplyEffective writes every family and the [secrets] values back without
// mutating a shared table, and families at default leave the hash unchanged.
// These tests change package state (the registry, knownConfigKeys), so none
// is parallel.
// SPORT: runtime/boot-baseline-check tests (guarded families).

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"
)

// withFamily registers f for the test and removes it (and its keys) after.
func withFamily(t *testing.T, f GuardedFamily) {
	t.Helper()
	keys := slices.Clone(knownConfigKeys)
	RegisterGuardedFamily(f)
	t.Cleanup(func() {
		guardedFamilyRegistry.Lock()
		delete(guardedFamilyRegistry.bySection, f.Section)
		guardedFamilyRegistry.Unlock()
		knownConfigKeys = keys
	})
}

// scratchSpec models [scratch.zone]: allow grows, deny shrinks, level ranks,
// socket set-or-change, cap a limit, mode any change keeping k1, and per-entry
// posture tables.
var scratchSpec = FieldSpec{Rule: RuleTable, Fields: map[string]FieldSpec{
	"allow": {Rule: RuleSetGrows}, "deny": {Rule: RuleSetShrinks}, "socket": {Rule: RuleSetOrChange},
	"level": {Rule: RuleRank, Ranks: []interface{}{"low", "mid", "high"}},
	"cap":   {Rule: RuleCap}, "mode": {Rule: RuleAnyChange, Keep: []interface{}{"k1"}},
	"entries": {Rule: RuleTable, Other: &FieldSpec{Rule: RuleTable, Fields: map[string]FieldSpec{"posture": {Rule: RuleAnyChange}}}},
}}

func scratchFamily(extract func(map[string]interface{}) (json.RawMessage, error)) GuardedFamily {
	return scratchSpec.Bind(GuardedFamily{Section: "scratch.zone", Keys: []string{"scratch.zone.allow"}, Extract: extract})
}

// rawZone extracts [scratch.zone] verbatim (keys mirror the TOML table).
func rawZone(extra map[string]interface{}) (json.RawMessage, error) {
	scratch, _ := extra["scratch"].(map[string]interface{})
	return json.Marshal(scratch["zone"])
}

func zone(v map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{"scratch": map[string]interface{}{"zone": v}}
}

func TestRegisterGuardedFamilyAddsKnownKeys(t *testing.T) {
	if _, err := ResolveDottedPath("scratch.zone.allow"); err == nil {
		t.Fatal("scratch.zone.allow is known before its family registered")
	}
	withFamily(t, scratchFamily(rawZone))
	if _, err := ResolveDottedPath("scratch.zone.allow"); err != nil {
		t.Fatalf("a registered family key is unknown: %v", err)
	}
	if _, err := ResolveDottedPath("scratch.zone.deny"); err == nil {
		t.Fatal("a key no family lists became known")
	}
	for name, f := range map[string]GuardedFamily{
		"duplicate":  scratchFamily(rawZone),
		"no extract": {Section: "scratch.other", Compare: scratchFamily(nil).Compare},
		"no section": {Extract: rawZone, Compare: scratchFamily(nil).Compare},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("%s registration did not panic", name)
				}
			}()
			RegisterGuardedFamily(f)
		}()
	}
}

func TestFieldSpecOrdersEachRule(t *testing.T) {
	withFamily(t, scratchFamily(rawZone))
	cases := []struct {
		name         string
		before, next map[string]interface{}
		loosens      bool
	}{
		{"allow entry added", nil, map[string]interface{}{"allow": []interface{}{"a"}}, true},
		{"allow entry removed", map[string]interface{}{"allow": []interface{}{"a"}}, nil, false},
		{"deny entry added", nil, map[string]interface{}{"deny": []interface{}{"x"}}, false},
		{"deny entry removed", map[string]interface{}{"deny": []interface{}{"x"}}, nil, true},
		{"rank raised", map[string]interface{}{"level": "low"}, map[string]interface{}{"level": "high"}, true},
		{"rank lowered", map[string]interface{}{"level": "high"}, map[string]interface{}{"level": "mid"}, false},
		{"rank unknown", nil, map[string]interface{}{"level": "bogus"}, true},
		{"socket set", nil, map[string]interface{}{"socket": "/a"}, true},
		{"socket changed", map[string]interface{}{"socket": "/a"}, map[string]interface{}{"socket": "/b"}, true},
		{"socket unset", map[string]interface{}{"socket": "/a"}, nil, false},
		{"cap set", nil, map[string]interface{}{"cap": 5}, false},
		{"cap lowered", map[string]interface{}{"cap": 5}, map[string]interface{}{"cap": 3}, false},
		{"cap raised", map[string]interface{}{"cap": 5}, map[string]interface{}{"cap": 6}, true},
		{"cap removed", map[string]interface{}{"cap": 5}, nil, true},
		{"cap not a limit", map[string]interface{}{"cap": 5}, map[string]interface{}{"cap": -1}, true},
		{"cap over a bad base", map[string]interface{}{"cap": "x"}, map[string]interface{}{"cap": 1}, true},
		{"kept entry added", nil, map[string]interface{}{"mode": []interface{}{"k1"}}, true},
		{"entry posture added", nil, map[string]interface{}{"entries": map[string]interface{}{"e1": map[string]interface{}{"posture": "open"}}}, true},
		{"unmodeled key", nil, map[string]interface{}{"surprise": true}, true},
		{"wrong shape", map[string]interface{}{"allow": []interface{}{"a"}}, map[string]interface{}{"allow": "a,b"}, true},
	}
	for _, tc := range cases {
		paths := CompareSecurity(EffectiveConfigOf(&Config{Extra: zone(tc.before)}), EffectiveConfigOf(&Config{Extra: zone(tc.next)}))
		if (len(paths) > 0) != tc.loosens {
			t.Errorf("%s: paths %+v, loosening want %v", tc.name, paths, tc.loosens)
		}
	}
}

func TestFamilyExtractErrorIsInvalidNeverTighter(t *testing.T) {
	fail := false
	withFamily(t, scratchFamily(func(extra map[string]interface{}) (json.RawMessage, error) {
		if fail {
			return nil, errors.New("decoder refuses the table")
		}
		return rawZone(extra)
	}))
	tight := EffectiveConfigOf(&Config{Extra: zone(map[string]interface{}{"deny": []interface{}{"x"}})})
	fail = true
	invalid := EffectiveConfigOf(&Config{Extra: zone(map[string]interface{}{"deny": []interface{}{"x"}})})
	if !isInvalidFamily(invalid.Families["scratch.zone"]) {
		t.Fatalf("an Extract error extracted as %s, want the invalid marker", invalid.Families["scratch.zone"])
	}
	if p := CompareSecurity(tight, invalid); len(p) != 1 || p[0].Proposed != "invalid" {
		t.Fatalf("valid -> invalid: %+v, want one invalid loosening", p)
	}
	if p := CompareSecurity(MostRestrictiveDefaults(), invalid); len(p) == 0 {
		t.Fatal("an invalid family reads as at the floor")
	}
	if fb := RestrictiveFallback(invalid); fb.Families != nil {
		t.Fatalf("a degraded boot kept the invalid value: %s", fb.Families)
	}
	if ComputeSectionsHash(invalid) == ComputeSectionsHash(tight) {
		t.Fatal("an invalid family hashes like a valid one")
	}
}

func TestRestrictiveFallbackKeepsTighterPerKey(t *testing.T) {
	withFamily(t, scratchFamily(rawZone))
	onDisk := EffectiveConfigOf(&Config{Extra: zone(map[string]interface{}{
		"allow": []interface{}{"a"}, "deny": []interface{}{"x"}, "level": "high", "socket": "/a",
		"cap": 5, "mode": []interface{}{"k1", "k2"}, "entries": map[string]interface{}{"e1": map[string]interface{}{"posture": "open"}},
	})})
	fb := RestrictiveFallback(onDisk)
	if got := string(fb.Families["scratch.zone"]); got != `{"cap":5,"deny":["x"],"mode":["k1"]}` {
		t.Fatalf("degraded family value %s, want only the cap, deny and kept mode entries", got)
	}
	low := EffectiveConfigOf(&Config{Extra: zone(map[string]interface{}{"level": "low"})})
	if got := string(RestrictiveFallback(low).Families["scratch.zone"]); got != `{"level":"low"}` {
		t.Fatalf("a rank at the default floor dropped: %s", got)
	}
	bad := EffectiveConfigOf(&Config{Extra: zone(map[string]interface{}{"cap": "x", "mode": []interface{}{"k2"}})})
	if fb := RestrictiveFallback(bad); fb.Families != nil {
		t.Fatalf("a cap that is no limit, or an entry Keep does not list, survived: %s", fb.Families)
	}
}

func TestApplyEffectiveWritesFamiliesAndSecretsBack(t *testing.T) {
	withFamily(t, scratchFamily(rawZone))
	shared := map[string]interface{}{"allow": []interface{}{"a"}, "deny": []interface{}{"x"}, "note": "kept",
		"entries": map[string]interface{}{"e1": map[string]interface{}{"posture": "open", "label": "kept"}}}
	ttl := 10 * time.Second
	floor, conf := 3.0, 0.9
	cfg := &Config{Extra: map[string]interface{}{
		"scratch": map[string]interface{}{"zone": shared},
		"secrets": map[string]interface{}{"clipboard_ttl": "60s", "entropy_floor": 5.0, "confidence_threshold": 0.95},
	}}
	eff := RestrictiveFallback(EffectiveConfigOf(cfg))
	eff.Secrets.ClipboardTTL, eff.Secrets.EntropyFloor, eff.Secrets.ConfidenceThreshold = &ttl, &floor, &conf
	ApplyEffective(cfg, eff)
	got := cfg.Extra["scratch"].(map[string]interface{})["zone"].(map[string]interface{})
	want := map[string]interface{}{"deny": []interface{}{"x"}, "note": "kept",
		"entries": map[string]interface{}{"e1": map[string]interface{}{"label": "kept"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("written-back [scratch.zone] = %v, want %v", got, want)
	}
	if _, ok := shared["allow"]; !ok {
		t.Fatal("ApplyEffective mutated a table another holder shares")
	}
	sec := cfg.Extra["secrets"].(map[string]interface{})
	if sec["clipboard_ttl"] != "10s" || sec["entropy_floor"] != 3.0 || sec["confidence_threshold"] != 0.9 {
		t.Fatalf("written-back [secrets] = %v, want the admitted ttl and thresholds", sec)
	}
	if after := EffectiveConfigOf(cfg).Secrets; ComputeSectionsHash(EffectiveConfig{Secrets: after}) != ComputeSectionsHash(EffectiveConfig{Secrets: eff.Secrets}) {
		t.Fatalf("the written-back [secrets] extracts as %+v, not the admitted %+v", after, eff.Secrets)
	}
}

func TestFamiliesAtDefaultKeepTheHash(t *testing.T) {
	withFamily(t, scratchFamily(rawZone))
	plain := EffectiveConfigOf(&Config{Extra: map[string]interface{}{"policy": map[string]interface{}{"autonomy_profile": "strict"}}})
	empty := EffectiveConfigOf(&Config{Extra: map[string]interface{}{"policy": map[string]interface{}{"autonomy_profile": "strict"},
		"scratch": map[string]interface{}{"zone": map[string]interface{}{}}}})
	if plain.Families != nil || empty.Families != nil || ComputeSectionsHash(plain) != ComputeSectionsHash(empty) {
		t.Fatalf("families at default changed the view: %s / %s", plain.Families, empty.Families)
	}
	legacy := plain
	legacy.Families = map[string]json.RawMessage{}
	if ComputeSectionsHash(legacy) != ComputeSectionsHash(plain) {
		t.Fatal("an empty Families map hashes differently from none")
	}
	snap, _ := json.Marshal(plain)
	var back EffectiveConfig
	if err := json.Unmarshal(snap, &back); err != nil || ComputeSectionsHash(back) != ComputeSectionsHash(plain) {
		t.Fatalf("a snapshot round trip changed the hash (err %v)", err)
	}
	set := EffectiveConfigOf(&Config{Extra: zone(map[string]interface{}{"deny": []interface{}{"x"}})})
	if ComputeSectionsHash(set) == ComputeSectionsHash(EffectiveConfigOf(&Config{})) {
		t.Fatal("a family value off its default does not change the hash")
	}
}

func TestClipboardTTLParse(t *testing.T) {
	for raw, want := range map[interface{}]time.Duration{
		"45s": 45 * time.Second, int64(12): 12 * time.Second, "30s": 0, int64(30): 0,
		"soon": invalidClipboardTTL, "0s": invalidClipboardTTL, "-5s": invalidClipboardTTL, int64(-1): invalidClipboardTTL, true: invalidClipboardTTL,
	} {
		got := clipboardTTLAt(map[string]interface{}{"secrets": map[string]interface{}{"clipboard_ttl": raw}})
		if (want == 0) != (got == nil) || (got != nil && *got != want) {
			t.Errorf("clipboard_ttl %v -> %v, want %v (0: the default, absent)", raw, got, want)
		}
	}
	bad := invalidClipboardTTL
	if _, err := SecretsClipboardTTL(EffectiveConfig{Secrets: SecretsSection{ClipboardTTL: &bad}}); err == nil {
		t.Fatal("an invalid clipboard_ttl was read as a duration")
	}
	if d, err := SecretsClipboardTTL(EffectiveConfig{}); err != nil || d != DefaultSecretsClipboardTTL {
		t.Fatalf("absent clipboard_ttl reads %v (err %v), want the default", d, err)
	}
}
