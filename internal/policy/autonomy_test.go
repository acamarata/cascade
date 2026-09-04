// Purpose: the autonomy profile's own contract — the built-in table
// asserted against the 06-FORGE-SPEC §5.15 SENTENCE rather than against a
// second copy of the table, the fail-closed behaviour of the Verdict enum
// and of a missing profile, and the two hardcoded ceilings that no profile
// or overlay can raise.
//
// SPORT: internal/policy AutonomyProfile/ADDED, Verdict/ADDED
// (P1-E09-W2-S18-T1).
package policy

import (
	"strings"
	"testing"
)

// allowAllProfile is the most permissive profile the type can express: it
// writes the slots DIRECTLY, bypassing set()'s clamp, so every assertion
// made against it exercises the ceilings in SlotFor rather than a table
// that was already sanitised on the way in. No config path can produce it;
// it exists so the tests can prove that even if one could, the ceilings
// still hold.
func allowAllProfile() *AutonomyProfile {
	p := &AutonomyProfile{name: "allow-all"}
	for lvl := L0; lvl <= L4; lvl++ {
		p.slots[lvl] = Slot{Verdict: VerdictAllow, AutoAdvance: true, Source: SourceProfileDefault}
	}
	return p
}

// TestAutonomyProfileMatchesSpecLadder is the §5.15 assertion: the
// built-in baseline profile resolves exactly what the spec sentence says,
// rung by rung, including L3's never-auto and L4's deny.
func TestAutonomyProfileMatchesSpecLadder(t *testing.T) {
	p := balancedProfile()
	rungs := parseSpecLadder(t)
	if len(rungs) != 5 {
		t.Fatalf("the spec ladder parsed %d rungs, want 5", len(rungs))
	}
	for _, rung := range rungs {
		level, err := parseRiskLevelName(rung.name)
		if err != nil {
			t.Fatalf("spec rung %q does not name a level: %v", rung.name, err)
		}
		want, err := parseVerdictName(rung.disposition)
		if err != nil {
			t.Fatalf("spec rung %q has disposition %q: %v", rung.name, rung.disposition, err)
		}
		slot := p.SlotFor(level)
		if slot.Verdict != want {
			t.Errorf("%s: verdict = %s, the spec sentence says %s", level, slot.Verdict, want)
		}
		// §5.15's auto-advance clause: L0/L1 only, allow-tier only.
		wantAuto := want == VerdictAllow && (level == L0 || level == L1)
		if slot.AutoAdvance != wantAuto {
			t.Errorf("%s: auto-advance = %v, the spec permits %v", level, slot.AutoAdvance, wantAuto)
		}
	}
	// The L3 "never auto" qualifier the ladder parser strips off, asserted
	// against the sentence itself so a spec edit that dropped it is caught.
	if !strings.Contains(specLadderVerbatim, "never auto") {
		t.Fatal("specLadderVerbatim no longer carries the L3 never-auto qualifier")
	}
	if p.AllowsAutoAdvance(L3) {
		t.Error("L3 auto-advanced, but the spec says never auto")
	}
}

// parseVerdictName maps the ladder sentence's disposition word onto the
// Verdict enum. It is the only place the prose vocabulary and the enum
// meet on this side, mirroring types_test.go's specDescriptionToClass.
func parseVerdictName(word string) (Verdict, error) {
	for v := VerdictAllow; v <= VerdictDeny; v++ {
		if verdictNames[v] == word {
			return v, nil
		}
	}
	return VerdictDeny, newConfigError("policy", "%q is not a verdict", sanitize(word))
}

// TestAutoAdvanceCeiling asserts the §5.15 ceiling directly: no rung above
// L1 ever auto-advances, under any built-in profile, and an Ask or Deny
// slot never does either.
func TestAutoAdvanceCeiling(t *testing.T) {
	for name, factory := range builtinProfiles {
		p := factory()
		for lvl := L0; lvl <= L4; lvl++ {
			slot := p.SlotFor(lvl)
			if slot.AutoAdvance && (lvl > L1 || slot.Verdict != VerdictAllow) {
				t.Errorf("profile %s: %s auto-advances at verdict %s", name, lvl, slot.Verdict)
			}
			if got := p.AllowsAutoAdvance(lvl); got != slot.AutoAdvance {
				t.Errorf("profile %s: AllowsAutoAdvance(%s) = %v, slot says %v", name, lvl, got, slot.AutoAdvance)
			}
		}
	}
	// A hand-built profile that tries to auto-advance everywhere is
	// clamped: the ceiling is not a property of the built-in tables, it is
	// enforced on every slot that resolves.
	greedy := allowAllProfile()
	for lvl := L2; lvl <= L4; lvl++ {
		if greedy.AllowsAutoAdvance(lvl) {
			t.Errorf("an allow-everything profile auto-advanced at %s", lvl)
		}
	}
}

// TestVerdictResolutionFailsClosed covers the enum's fail-closed shape: the
// zero value is not Allow, an out-of-range value is not Allow, and both
// resolve to Deny through safeVerdict — the twin of types.go's safeLevel.
func TestVerdictResolutionFailsClosed(t *testing.T) {
	var unset Verdict
	if unset.Valid() {
		t.Fatal("the zero Verdict reports itself valid")
	}
	if got := safeVerdict(unset); got != VerdictDeny {
		t.Errorf("safeVerdict(zero) = %s, want deny", got)
	}
	if got := safeVerdict(Verdict(200)); got != VerdictDeny {
		t.Errorf("safeVerdict(out of range) = %s, want deny", got)
	}
	if got := unset.String(); got != "invalid-verdict" {
		t.Errorf("zero Verdict renders as %q", got)
	}
	// maxVerdict is the restrict-only combinator: no pair of inputs
	// produces something more permissive than either input.
	for _, a := range []Verdict{VerdictAllow, VerdictAsk, VerdictDeny, 0, Verdict(9)} {
		for _, b := range []Verdict{VerdictAllow, VerdictAsk, VerdictDeny, 0, Verdict(9)} {
			got := maxVerdict(a, b)
			if got < safeVerdict(a) || got < safeVerdict(b) {
				t.Errorf("maxVerdict(%v, %v) = %s, which is more permissive than an input", a, b, got)
			}
		}
	}
}

// TestUnsetProfileDeniesEverything is hard requirement 2's first case: a
// profile that is not there is the most restrictive behaviour, not the
// most permissive.
func TestUnsetProfileDeniesEverything(t *testing.T) {
	var missing *AutonomyProfile
	for lvl := L0; lvl <= L4; lvl++ {
		slot := missing.SlotFor(lvl)
		if slot.Verdict != VerdictDeny {
			t.Errorf("the nil profile answered %s at %s", slot.Verdict, lvl)
		}
		if slot.AutoAdvance {
			t.Errorf("the nil profile auto-advanced at %s", lvl)
		}
		if slot.Source != SourceHardcoded {
			t.Errorf("the nil profile's %s slot is sourced %s", lvl, slot.Source)
		}
	}
	if missing.Name() != lockedProfileName {
		t.Errorf("the nil profile is named %q, want %q", missing.Name(), lockedProfileName)
	}
	if missing.VerdictFor(L0) != VerdictDeny || missing.AllowsAutoAdvance(L0) {
		t.Error("the nil profile allowed an L0 read")
	}
}

// TestInvalidLevelResolvesToL4 asserts the level side of fail-closed: an
// unset or out-of-range RiskLevel reads as L4 through the SAME safeLevel
// helper types.go already owns, so a slot lookup for a forgotten field
// lands on deny.
func TestInvalidLevelResolvesToL4(t *testing.T) {
	p := balancedProfile()
	var unset RiskLevel
	if got := p.VerdictFor(unset); got != VerdictDeny {
		t.Errorf("the zero RiskLevel resolved to %s, want deny", got)
	}
	if got := p.VerdictFor(RiskLevel(99)); got != VerdictDeny {
		t.Errorf("an out-of-range RiskLevel resolved to %s, want deny", got)
	}
	if got, want := p.SlotFor(RiskLevel(0)), p.SlotFor(L4); got != want {
		t.Errorf("the zero RiskLevel resolved to %+v, want the L4 slot %+v", got, want)
	}
}

// TestStrictIsNeverWiderThanBalanced asserts the strict table as a
// PROPERTY against the baseline rather than as a second literal table: no
// rung of strict may be more permissive than the same rung of balanced.
func TestStrictIsNeverWiderThanBalanced(t *testing.T) {
	base, strict := balancedProfile(), strictProfile()
	tighter := false
	for lvl := L0; lvl <= L4; lvl++ {
		b, s := base.SlotFor(lvl), strict.SlotFor(lvl)
		if maxVerdict(b.Verdict, s.Verdict) != s.Verdict {
			t.Errorf("strict is more permissive than balanced at %s: %s vs %s", lvl, s.Verdict, b.Verdict)
		}
		if s.AutoAdvance && !b.AutoAdvance {
			t.Errorf("strict auto-advances at %s where balanced does not", lvl)
		}
		if s.Verdict != b.Verdict {
			tighter = true
		}
	}
	if !tighter {
		t.Error("strict is identical to balanced, so it tightens nothing")
	}
}

// TestClampSlotCeilings covers clampSlot directly, including the L4 floor
// no table may raise and the repair of an invalid verdict.
func TestClampSlotCeilings(t *testing.T) {
	got := clampSlot(L4, Slot{Verdict: VerdictAllow, AutoAdvance: true, Source: SourceOverlay})
	if got.Verdict != VerdictDeny || got.AutoAdvance || got.Source != SourceHardcoded {
		t.Errorf("clampSlot(L4, allow+auto) = %+v, want a hardcoded deny with no auto-advance", got)
	}
	repaired := clampSlot(L0, Slot{Source: SourceProfileDefault})
	if repaired.Verdict != VerdictDeny || repaired.Source != SourceHardcoded {
		t.Errorf("clampSlot(L0, unset verdict) = %+v, want a hardcoded deny", repaired)
	}
	kept := clampSlot(L1, Slot{Verdict: VerdictAllow, AutoAdvance: true, Source: SourceProfileDefault})
	if kept.Verdict != VerdictAllow || !kept.AutoAdvance || kept.Source != SourceProfileDefault {
		t.Errorf("clampSlot(L1, allow+auto) = %+v, want it left alone", kept)
	}
}

// TestSlotSourceNames covers the annotation's rendering, including the
// invalid value.
func TestSlotSourceNames(t *testing.T) {
	cases := map[SlotSource]string{
		SourceProfileDefault: "profile-default",
		SourceOverlay:        "overlay",
		SourceHardcoded:      "hardcoded",
		SlotSource(0):        "invalid-slot-source",
		SlotSource(7):        "invalid-slot-source",
	}
	for src, want := range cases {
		if got := src.String(); got != want {
			t.Errorf("SlotSource(%d).String() = %q, want %q", src, got, want)
		}
	}
}
