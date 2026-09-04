// Package policy (visibility.go): Purpose: the TEAM carrier — the rule
//
//	that decides whether one already-resolved corpus record may be carried
//	to the team, and at what reach a grant lets it travel. The
//	shared-visibility classes themselves are NOT defined here: they are
//	consumed from internal/retrieval/corpus (F/S-10.T4), which owns the
//	enum, its ordering by reach, and the record/corpus resolution. This
//	file adds only the capability- and grant-facing layer on top.
//
// Inputs: a corpus.Record as returned by corpus.Store.Query — i.e. one
//
//	whose privacy, visibility and TRUST values are already RESOLVED
//	against its corpus, so this file reads a decided classification rather
//	than re-deriving one and possibly disagreeing with the store.
//
// Outputs: the carrier's propagated TRUST/visibility/privacy values, an
//
//	eligibility verdict, and the effective reach of a grant applied to the
//	record.
//
// Constraints: a grant NARROWS, never widens. EffectiveClass returns the
//
//	narrower of the record's own class and the grant's, so a team-class
//	grant over a private record yields private. Untrusted content is never
//	team-shareable, whatever its visibility class says: that is the
//	untrusted-tag propagation this file asserts, and corpus.resolveTrust
//	is what guarantees the tag arrived intact. Personal-tier material is
//	never team-shareable either. Every unreadable value denies.
//
// SPORT: internal/policy TeamCarrier/ADDED (P1-E09-W2-S17-T1).
package policy

import (
	"github.com/acamarata/cascade/internal/retrieval/corpus"
)

// visibilityReach orders the corpus classes from narrowest to widest
// reach, using corpus's own constants rather than a second enum. A class
// this table does not name — including a class a future amendment adds to
// corpus — ranks 0, below private, so narrowing against it yields private.
// Drift therefore fails CLOSED: an unranked class can never be the wider
// side of a comparison and can never satisfy the team check.
var visibilityReach = map[corpus.VisibilityClass]int{
	corpus.VisibilityPrivate:    1,
	corpus.VisibilityScopeLocal: 2,
	corpus.VisibilityShared:     3,
	corpus.VisibilityTeam:       4,
}

// reachOf returns v's rank, or 0 for a class that is not ranked or not
// valid. Both checks are present deliberately: Valid is corpus's own
// judgement, the table is this package's, and a value must satisfy both to
// count as reachable.
func reachOf(v corpus.VisibilityClass) int {
	if !v.Valid() {
		return 0
	}
	return visibilityReach[v]
}

// narrowerVisibility returns the narrower of a and b: the single choke
// point for the rule that a grant may reduce reach and never raise it.
// An unranked or invalid value on either side collapses the result to
// corpus.VisibilityPrivate rather than yielding the other side, so a
// malformed grant cannot pass a record's own class through unchanged and a
// malformed record cannot inherit a grant's.
func narrowerVisibility(a, b corpus.VisibilityClass) corpus.VisibilityClass {
	ra, rb := reachOf(a), reachOf(b)
	if ra == 0 || rb == 0 {
		return corpus.VisibilityPrivate
	}
	if ra <= rb {
		return a
	}
	return b
}

// TeamCarrier wraps one resolved corpus record and answers the sharing
// questions the grant model asks of it. It holds the record by value, so a
// carrier cannot be mutated out from under a decision that was already
// made from it.
//
// The record must come from corpus.Store.Query (or be an equivalently
// resolved value): NewTeamCarrier validates it, but it cannot re-derive a
// record's classification against a corpus it was not given, which is why
// the resolution stays corpus's job.
type TeamCarrier struct {
	record corpus.Record
}

// NewTeamCarrier wraps rec after validating it. A record that is not fully
// classified on all three axes is refused outright rather than being
// carried with a value this package would then have to guess at.
func NewTeamCarrier(rec corpus.Record) (TeamCarrier, error) {
	if err := rec.Validate(); err != nil {
		return TeamCarrier{}, err
	}
	return TeamCarrier{record: rec}, nil
}

// Record returns the wrapped record. The copy the caller receives cannot
// affect the carrier's own decisions.
func (c TeamCarrier) Record() corpus.Record { return c.record }

// Trust returns the record's propagated TRUST level. A record whose tag is
// not one of corpus's two levels reads as untrusted-source, matching
// corpus's own fail-closed resolution rather than inventing a third state.
func (c TeamCarrier) Trust() corpus.TrustLevel {
	if !c.record.Trust.Valid() {
		return corpus.TrustUntrustedSource
	}
	return c.record.Trust
}

// Visibility returns the record's resolved shared-visibility class, or
// corpus.VisibilityPrivate when the stored value is not a class.
func (c TeamCarrier) Visibility() corpus.VisibilityClass {
	if !c.record.Visibility.Valid() {
		return corpus.VisibilityPrivate
	}
	return c.record.Visibility
}

// Privacy returns the record's resolved privacy tier, or
// corpus.PrivacyPersonal when the stored value is not a tier — the same
// direction corpus resolves in, because personal is the tier that cannot
// leak.
func (c TeamCarrier) Privacy() corpus.PrivacyClass {
	if !c.record.Privacy.Valid() {
		return corpus.PrivacyPersonal
	}
	return c.record.Privacy
}

// EffectiveClass returns how far the record may travel once grant is
// applied: the narrower of the record's own class and the grant's
// ScopeClass.
//
// This is hard requirement 2 in one line. A grant naming
// corpus.VisibilityTeam over a private record yields private; a grant
// naming a narrower class than the record's narrows it. No argument order
// and no input value produces a class wider than the record already had.
func (c TeamCarrier) EffectiveClass(g Grant) corpus.VisibilityClass {
	return narrowerVisibility(c.Visibility(), g.ScopeClass)
}

// EligibleForTeamShare reports whether the record may be carried to the
// team on its own classification, before any grant is consulted. A nil
// return means eligible; every other return is a grant-denied refusal
// naming the axis that refused.
//
// Three independent conditions must all hold, and each is checked
// separately so the refusal says which one failed:
//
//  1. TRUST is trusted. Untrusted-source content is data, never something
//     the user vouched for, so it is not carried to anyone else however
//     its visibility class is marked. This is the untrusted-tag
//     propagation rule: corpus.resolveTrust already forced a record inside
//     an untrusted corpus down to untrusted-source, and this is the check
//     that acts on it.
//  2. The resolved visibility class is corpus.VisibilityTeam. A shared,
//     scope-local or private record is not team material; naming it in a
//     grant does not make it so.
//  3. The privacy tier is not personal. Personal material is served only
//     to a personal entitlement, and carrying it to a team would be
//     exactly the leak corpus's personal-wins resolution exists to
//     prevent.
func (c TeamCarrier) EligibleForTeamShare() error {
	if c.Trust() != corpus.TrustTrusted {
		return newGrantDenied("record %s carries trust %s and is not team-shareable",
			sanitize(c.record.ID), c.Trust())
	}
	if c.Visibility() != corpus.VisibilityTeam {
		return newGrantDenied("record %s is classified %s, not %s",
			sanitize(c.record.ID), c.Visibility(), corpus.VisibilityTeam)
	}
	if c.Privacy() == corpus.PrivacyPersonal {
		return newGrantDenied("record %s is personal-tier and is not team-shareable",
			sanitize(c.record.ID))
	}
	return nil
}

// ShareUnder applies decision to the record and returns the reach the
// record actually gets, or a refusal.
//
// The order is load-bearing and is the whole point of the type: the
// record's own eligibility is decided FIRST, on the record's
// classification alone, and only then is the grant allowed to narrow the
// result. A grant is never consulted for permission to raise a record, so
// there is no ordering in which a grant widens one.
func (c TeamCarrier) ShareUnder(d Decision) (corpus.VisibilityClass, error) {
	if !d.Granted {
		return corpus.VisibilityPrivate, newGrantDenied(
			"record %s: no granted decision to share under", sanitize(c.record.ID))
	}
	if err := c.EligibleForTeamShare(); err != nil {
		return corpus.VisibilityPrivate, err
	}
	effective := narrowerVisibility(c.Visibility(), d.ScopeClass)
	if effective != corpus.VisibilityTeam {
		return effective, newGrantDenied(
			"record %s: grant scope %s narrows the effective class to %s",
			sanitize(c.record.ID), d.ScopeClass, effective)
	}
	return effective, nil
}
