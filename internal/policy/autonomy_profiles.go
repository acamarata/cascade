// Package policy (autonomy_profiles.go): Purpose: the built-in profile
//
//	tables — the names [policy].autonomy_profile accepts, the
//	06-FORGE-SPEC §5.15 baseline they start from, and the deny-everything
//	fallback that is reached only by failing closed. Split from autonomy.go
//	per R-14.117/Art.10.3 (the 300-line file cap); the ceilings that govern
//	these tables live next door and apply to every slot they set.
//
// Inputs: none; these are declarations plus total constructors.
// Outputs: *AutonomyProfile values.
// Constraints: every slot written here goes through clampSlot, so a table
//
//	cannot ship a slot the ceilings would have refused. No table in this
//	file allows anything above L1 without asking.
//
// SPORT: internal/policy builtinProfiles/ADDED (P1-E09-W2-S18-T1).
package policy

// lockedProfileName names the deny-everything profile the system falls
// back to whenever a profile cannot be resolved. It is not selectable from
// config: an operator asking for it by name gets the unknown-profile
// refusal like any other unrecognised name, because reaching it should
// always be the consequence of something failing closed rather than of
// something being configured.
const lockedProfileName = "locked"

// builtinProfiles is the set of names [policy].autonomy_profile accepts.
// A name outside this set is refused (autonomy_config.go); it never
// degrades to a default, because a misspelled profile name silently
// selecting a permissive table is precisely the failure this subsystem
// exists to prevent.
var builtinProfiles = map[string]func() *AutonomyProfile{
	"balanced": balancedProfile,
	"strict":   strictProfile,
	// "custom" starts from the balanced table and is expected to carry
	// overlays; overlays are tightening-only, so it can only ever be a
	// narrower balanced.
	"custom": balancedProfile,
}

// balancedProfile is the 06-FORGE-SPEC §5.15 risk ladder verbatim, and the
// baseline every other resolution starts from: L0 read-only allow · L1 safe
// local dev allow · L2 workspace mutation ask · L3 external side effect
// ask, never auto · L4 destructive/privileged deny.
//
// It is the MOST PERMISSIVE table the system has. There is no
// full-autonomy profile, by design: nothing an operator can name in config
// allows L2 or above without asking.
func balancedProfile() *AutonomyProfile {
	p := &AutonomyProfile{name: "balanced"}
	p.set(L0, VerdictAllow, true)
	p.set(L1, VerdictAllow, true)
	p.set(L2, VerdictAsk, false)
	p.set(L3, VerdictAsk, false)
	p.set(L4, VerdictDeny, false)
	return p
}

// strictProfile is the balanced table tightened one rung wherever §5.15
// leaves room: L1 asks rather than allowing, and L3 denies rather than
// asking. L0 stays allow (a read changes nothing) and L2/L4 are already at
// their §5.15 dispositions. Every slot is at least as restrictive as
// balanced's, which autonomy_test.go asserts as a property rather than as
// a second copy of this table.
func strictProfile() *AutonomyProfile {
	p := &AutonomyProfile{name: "strict"}
	p.set(L0, VerdictAllow, true)
	p.set(L1, VerdictAsk, false)
	p.set(L2, VerdictAsk, false)
	p.set(L3, VerdictDeny, false)
	p.set(L4, VerdictDeny, false)
	return p
}

// set writes one profile-default slot. It is the only writer of slots on a
// built-in table, and it stamps the source so an operator reading the
// effective view can tell a profile default from an overlay.
func (p *AutonomyProfile) set(level RiskLevel, v Verdict, autoAdvance bool) {
	lvl := safeLevel(level)
	p.slots[lvl] = clampSlot(lvl, Slot{
		Verdict:     v,
		AutoAdvance: autoAdvance,
		Source:      SourceProfileDefault,
	})
}
