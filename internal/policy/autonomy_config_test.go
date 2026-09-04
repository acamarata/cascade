// Purpose: the [policy] config binding's contract — the complete 08 §3 key
// set, the strict refusals that keep a malformed or misspelled section from
// resolving to something more permissive than the operator wrote, and the
// tightening-only rule for overlays.
//
// SPORT: internal/policy Config/ADDED, ApprovalBatching/ADDED
// (P1-E09-W2-S18-T1).
package policy

import (
	"strings"
	"testing"
)

// policyTree wraps a [policy] table in the shape internal/runtime hands a
// section parser.
func policyTree(section map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{"policy": section}
}

// levelList renders level names as the decoded TOML list type.
func levelList(names ...string) []interface{} {
	out := make([]interface{}, 0, len(names))
	for _, n := range names {
		out = append(out, n)
	}
	return out
}

// TestConfigDefaults covers the absent cases: no tree, no [policy]
// section, and an empty one all resolve to the §5.15 baseline and the 08 §3
// batching defaults.
func TestConfigDefaults(t *testing.T) {
	for name, tree := range map[string]map[string]interface{}{
		"nil tree":       nil,
		"no section":     {"logging": map[string]interface{}{}},
		"empty section":  policyTree(map[string]interface{}{}),
		"foreign key":    policyTree(map[string]interface{}{"preset": "balanced"}),
		"risk_gates key": policyTree(map[string]interface{}{"risk_gates": map[string]interface{}{}}),
	} {
		cfg, err := ParseConfig(tree)
		if err != nil {
			t.Fatalf("%s: ParseConfig: %v", name, err)
		}
		if cfg.Batching.WindowSeconds != DefaultApprovalBatchWindowSeconds ||
			cfg.Batching.Cap != DefaultApprovalBatchCap {
			t.Errorf("%s: batching = %+v, want the 08 §3 defaults", name, cfg.Batching)
		}
		p, err := Resolve(cfg)
		if err != nil {
			t.Fatalf("%s: Resolve: %v", name, err)
		}
		if p.Name() != defaultProfileName {
			t.Errorf("%s: resolved profile %q, want %q", name, p.Name(), defaultProfileName)
		}
		// The default is never more permissive than the §5.15 baseline.
		base := balancedProfile()
		for lvl := L0; lvl <= L4; lvl++ {
			if maxVerdict(base.VerdictFor(lvl), p.VerdictFor(lvl)) != p.VerdictFor(lvl) {
				t.Errorf("%s: %s resolved wider than the baseline", name, lvl)
			}
		}
	}
}

// TestInvalidConfigRejection is hard requirement 2's config half: every
// malformed shape is a refusal, never a value. A refusal is what keeps the
// running profile in place; a default would silently replace it.
func TestInvalidConfigRejection(t *testing.T) {
	cases := map[string]map[string]interface{}{
		"section is not a table":  {"policy": "balanced"},
		"misspelled key":          {"autonomy_profil": "strict"},
		"profile is not a string": {"autonomy_profile": int64(3)},
		"profile is empty":        {"autonomy_profile": "   "},
		"overlay is not a list":   {"ask": "L2"},
		"overlay entry not text":  {"ask": levelListRaw(int64(2))},
		"overlay names no rung":   {"ask": levelList("L9")},
		"overlay names a word":    {"deny": levelList("everything")},
		"level named twice":       {"ask": levelList("L2"), "deny": levelList("L2")},
		"window is not an int":    {"approval_batch_window_s": "10"},
		"window is zero":          {"approval_batch_window_s": int64(0)},
		"window is negative":      {"approval_batch_window_s": int64(-1)},
		"window is absurd":        {"approval_batch_window_s": int64(1e9)},
		"cap is zero":             {"approval_batch_cap": int64(0)},
		"cap is absurd":           {"approval_batch_cap": int64(100000)},
	}
	for name, section := range cases {
		tree := policyTree(section)
		if raw, ok := section["policy"]; ok {
			tree = map[string]interface{}{"policy": raw}
		}
		if _, err := ParseConfig(tree); err == nil {
			t.Errorf("%s: ParseConfig accepted a malformed section", name)
		}
	}
}

// levelListRaw builds an overlay list holding a non-string entry.
func levelListRaw(items ...interface{}) []interface{} { return items }

// TestUnknownProfileNameIsRefused is the catastrophic case named in the
// contract: a misspelled profile name must never resolve to a working
// profile, least of all a permissive one.
func TestUnknownProfileNameIsRefused(t *testing.T) {
	for _, name := range []string{"balancd", "BALANCED", "yolo", "locked", "full-autonomy"} {
		cfg, err := ParseConfig(policyTree(map[string]interface{}{"autonomy_profile": name}))
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		p, err := Resolve(cfg)
		if err == nil {
			t.Errorf("%s: resolved to profile %q instead of being refused", name, p.Name())
			continue
		}
		if p != nil {
			t.Errorf("%s: refused AND returned a profile %+v", name, p)
		}
		if !strings.Contains(err.Error(), "is not a profile") {
			t.Errorf("%s: refusal does not name the problem: %v", name, err)
		}
	}
	// "locked" is the internal deny-everything fallback and must not be
	// selectable, so the fallback is only ever reached by failing closed.
	if _, selectable := builtinProfiles[lockedProfileName]; selectable {
		t.Error("the locked fallback is selectable from config")
	}
}

// TestOverlayPrecedence covers the overlay path: a tightening overlay wins
// over the profile default, is annotated as an overlay, and clears
// auto-advance when it tightens past the allow tier.
func TestOverlayPrecedence(t *testing.T) {
	cfg, err := ParseConfig(policyTree(map[string]interface{}{
		"autonomy_profile": "balanced",
		"ask":              levelList("L1"),
		"deny":             levelList("l2", " L3 "),
	}))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	p, err := Resolve(cfg)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := map[RiskLevel]Verdict{
		L0: VerdictAllow, L1: VerdictAsk, L2: VerdictDeny, L3: VerdictDeny, L4: VerdictDeny,
	}
	for lvl, verdict := range want {
		if got := p.VerdictFor(lvl); got != verdict {
			t.Errorf("%s: verdict = %s, want %s", lvl, got, verdict)
		}
	}
	if src := p.SlotFor(L1).Source; src != SourceOverlay {
		t.Errorf("L1 source = %s, want overlay", src)
	}
	if src := p.SlotFor(L0).Source; src != SourceProfileDefault {
		t.Errorf("L0 source = %s, want profile-default", src)
	}
	if p.AllowsAutoAdvance(L1) {
		t.Error("L1 still auto-advances after an overlay tightened it to ask")
	}
}

// TestOverlayCannotWiden is hard requirement 1 at the config layer: an
// overlay may only tighten. Every widening attempt below is a refusal, and
// the refusal names the level so an operator can find it.
func TestOverlayCannotWiden(t *testing.T) {
	cases := map[string]map[string]interface{}{
		"allow an ask level":         {"autonomy_profile": "balanced", "allow": levelList("L2")},
		"allow an external effect":   {"autonomy_profile": "balanced", "allow": levelList("L3")},
		"ask a denied level":         {"autonomy_profile": "balanced", "ask": levelList("L4")},
		"allow the destructive rung": {"autonomy_profile": "balanced", "allow": levelList("L4")},
		"loosen strict back":         {"autonomy_profile": "strict", "allow": levelList("L1")},
		"loosen strict L3":           {"autonomy_profile": "strict", "ask": levelList("L3")},
		"widen under custom":         {"autonomy_profile": "custom", "allow": levelList("L2")},
	}
	for name, section := range cases {
		cfg, err := ParseConfig(policyTree(section))
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		p, err := Resolve(cfg)
		if err == nil {
			t.Errorf("%s: a widening overlay resolved to %+v", name, p.SlotFor(L2))
			continue
		}
		if !strings.Contains(err.Error(), "may only tighten") {
			t.Errorf("%s: refusal does not name the rule: %v", name, err)
		}
	}
}

// TestOverlayNoOpIsAccepted covers the boundary of the tightening rule: an
// overlay that restates a slot's existing verdict is not a widening.
func TestOverlayNoOpIsAccepted(t *testing.T) {
	cfg, err := ParseConfig(policyTree(map[string]interface{}{
		"autonomy_profile": "balanced",
		"allow":            levelList("L0"),
		"ask":              levelList("L2"),
		"deny":             levelList("L4"),
	}))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p, err := Resolve(cfg)
	if err != nil {
		t.Fatalf("a no-op overlay was refused: %v", err)
	}
	if p.VerdictFor(L0) != VerdictAllow || p.VerdictFor(L2) != VerdictAsk || p.VerdictFor(L4) != VerdictDeny {
		t.Errorf("a no-op overlay changed the table: %+v", p)
	}
	if !p.AllowsAutoAdvance(L0) {
		t.Error("a no-op allow overlay on L0 cleared auto-advance")
	}
}

// TestApprovalBatchingBinding covers the two R-14.29 numerics S-18.T3
// reads through this API: in-range values are carried, and the accessor
// reports them.
func TestApprovalBatchingBinding(t *testing.T) {
	cfg, err := ParseConfig(policyTree(map[string]interface{}{
		"approval_batch_window_s": int64(30),
		"approval_batch_cap":      5,
	}))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Batching.WindowSeconds != 30 || cfg.Batching.Cap != 5 {
		t.Errorf("batching = %+v, want {30 5}", cfg.Batching)
	}
	if cfg.Batching.WindowSeconds > maxApprovalBatchWindowSeconds || cfg.Batching.Cap > maxApprovalBatchCap {
		t.Error("an in-range value was read outside its bound")
	}
}

// TestRiskLevelNameParsing covers the level-name parser both ways round.
func TestRiskLevelNameParsing(t *testing.T) {
	for name, want := range map[string]RiskLevel{"L0": L0, "l1": L1, " L2 ": L2, "L3": L3, "l4": L4} {
		got, err := parseRiskLevelName(name)
		if err != nil || got != want {
			t.Errorf("parseRiskLevelName(%q) = %s, %v; want %s", name, got, err, want)
		}
	}
	for _, name := range []string{"", "L5", "L-1", "allow", "L0 L1"} {
		if got, err := parseRiskLevelName(name); err == nil {
			t.Errorf("parseRiskLevelName(%q) = %s with no error", name, got)
		}
	}
}
