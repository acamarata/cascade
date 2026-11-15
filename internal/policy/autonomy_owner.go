// Package policy (autonomy_owner.go): Purpose: the autonomous profile table
// and the subject-aware profile resolver for owner-approved live agents.
//
// Inputs: a base AutonomyProfile, a Subject, and a live-approval flag.
// Outputs: an AutonomyProfile, either the autonomous table or the base
// unchanged.
// Constraints: the autonomous table is built through the same set and clamp
// path as the built-in tables, so hardVerdictFloor still holds for L4.
package policy

// autonomousProfile builds the fully autonomous profile table: L0 through
// L3 allow, L4 denies. It is not selectable via config; it is only
// returned by ProfileFor when an agent subject has a live owner approval.
func autonomousProfile() *AutonomyProfile {
	p := &AutonomyProfile{name: "autonomous"}
	p.set(L0, VerdictAllow, true)
	p.set(L1, VerdictAllow, true)
	p.set(L2, VerdictAllow, true)
	p.set(L3, VerdictAllow, true)
	p.set(L4, VerdictDeny, false)
	return p
}

// ProfileFor returns the autonomous profile table when live is true and the
// subject is an agent with a valid ID. For user and plugin subjects, for an
// invalid subject, for live=false and for a nil base it returns base
// unchanged.
func ProfileFor(base *AutonomyProfile, subject Subject, live bool) *AutonomyProfile {
	if !live {
		return base
	}
	if subject.Kind != SubjectAgent {
		return base
	}
	if subject.Validate() != nil {
		return base
	}
	return autonomousProfile()
}
