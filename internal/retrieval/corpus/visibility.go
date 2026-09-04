// Purpose: the shared-visibility classes of the scope model. A visibility
// class answers one question about a corpus or a record: how far beyond
// the scope that owns it may this content travel. The capability registry
// and grant model that turn a class into a team-sharing decision are a
// separate ticket's work and consume these classes as data; no capability
// or grant logic lives here.
//
// Inputs: a visibility string as written in a corpus definition or read
// back from storage, plus the membership decision from scope.go.
//
// Outputs: a VisibilityClass, and the reach it permits.
//
// Constraints: classes are ordered by reach, and every fail-closed reader
// resolves an unset or unrecognized class to VisibilityPrivate, the
// narrowest reach. A record whose class cannot be read is invisible, never
// universally visible. Reach is defined against the membership sets
// scope.go computes; this file never widens a membership decision, it can
// only narrow one.
//
// SPORT: internal.retrieval.corpus.VisibilityClass/ADDED.

package corpus

// VisibilityClass is how far beyond its owning scope a corpus or record
// may be surfaced.
//
// The classes are cumulative: each one permits everything the class below
// it permits, plus one more set of scopes. The zero value is not a class.
type VisibilityClass string

const (
	// VisibilityPrivate confines content to the exact scope that owns it.
	// A session in a child or parent scope of the owner does not see it.
	// This is the fail-closed resolution of any unreadable class.
	VisibilityPrivate VisibilityClass = "private"

	// VisibilityScopeLocal permits content to surface anywhere in the
	// owning scope's own chain: the session's scope and its ancestors.
	// Relationship edges do not reach it.
	VisibilityScopeLocal VisibilityClass = "scope-local"

	// VisibilityShared permits content to surface across a declared
	// relationship edge as well as within the chain. This is the class
	// that makes a deliberately linked pair of scopes able to see each
	// other's material, and it still reaches nothing that has no edge.
	VisibilityShared VisibilityClass = "shared"

	// VisibilityTeam marks content intended to be carried to everyone who
	// has the repository: the class the team carrier consumes. Its reach
	// inside this core model is the same as VisibilityShared, because
	// core carries the classification and never performs the sharing.
	// The distinction is data the grant model reads, not a wider
	// membership rule here.
	VisibilityTeam VisibilityClass = "team"
)

// Valid reports whether v is one of the four defined classes.
func (v VisibilityClass) Valid() bool {
	switch v {
	case VisibilityPrivate, VisibilityScopeLocal, VisibilityShared, VisibilityTeam:
		return true
	default:
		return false
	}
}

// String returns the stored spelling of v, or "invalid" for a value that
// is not a defined class.
func (v VisibilityClass) String() string {
	if !v.Valid() {
		return "invalid"
	}
	return string(v)
}

// visibilityRank orders the classes from narrowest to widest reach. An
// invalid class ranks below private, so combining it with anything yields
// private or narrower.
func visibilityRank(v VisibilityClass) int {
	switch v {
	case VisibilityPrivate:
		return 1
	case VisibilityScopeLocal:
		return 2
	case VisibilityShared:
		return 3
	case VisibilityTeam:
		return 4
	default:
		return 0
	}
}

// resolveVisibility returns the effective class of a record given its own
// class and its corpus's class: the NARROWER of the two, with an unset or
// unrecognized value on either side collapsing to VisibilityPrivate.
//
// A record cannot reach further than the corpus that holds it. Marking one
// record team-visible inside a private corpus does not publish it.
func resolveVisibility(record, corpusClass VisibilityClass) VisibilityClass {
	if !record.Valid() || !corpusClass.Valid() {
		return VisibilityPrivate
	}
	if visibilityRank(record) <= visibilityRank(corpusClass) {
		return record
	}
	return corpusClass
}

// reaches reports whether a record owned by owner, classified v, may
// surface for the membership m.
//
// Deny-by-default: the decision starts at "no" and only a class whose
// reach explicitly covers the relationship between m and owner turns it
// into "yes". An invalid class never reaches past the owning scope, and an
// owner that is not in m's chain or edge set is never reached at all.
func (v VisibilityClass) reaches(m Membership, owner ScopeRef) bool {
	switch v {
	case VisibilityPrivate:
		return m.Scope == owner && owner.Valid()
	case VisibilityScopeLocal:
		return m.inChain(owner)
	case VisibilityShared, VisibilityTeam:
		return m.inChain(owner) || m.acrossEdge(owner)
	default:
		return false
	}
}

// PrivacyClass is the privacy tier a corpus or record belongs to: the
// per-corpus and per-record privacy flag the model carries. Query-time
// enforcement beyond the membership decision here, and egress-time
// substitution of the values inside the content, are separate tickets'
// work; this is the flag they read.
//
// The tiers are not a reach ordering like VisibilityClass. They answer a
// different question: whose material is this, and is the asking session
// entitled to see material of that kind at all.
type PrivacyClass string

const (
	// PrivacyPersonal marks the user's own material: their personal
	// directory, their vault, their conversation store. It is served only
	// to a query that has established personal entitlement, and it is the
	// fail-closed resolution of any unreadable privacy flag.
	PrivacyPersonal PrivacyClass = "personal"

	// PrivacyGlobal marks the user-level instruction and configuration
	// tier that is not personal material.
	PrivacyGlobal PrivacyClass = "global"

	// PrivacyProject marks a project's own files.
	PrivacyProject PrivacyClass = "project"
)

// Valid reports whether p is one of the three defined tiers.
func (p PrivacyClass) Valid() bool {
	switch p {
	case PrivacyPersonal, PrivacyGlobal, PrivacyProject:
		return true
	default:
		return false
	}
}

// String returns the stored spelling of p, or "invalid" for a value that
// is not a defined tier.
func (p PrivacyClass) String() string {
	if !p.Valid() {
		return "invalid"
	}
	return string(p)
}

// resolvePrivacy returns the effective tier of a record given its own flag
// and its corpus's flag. Personal wins over everything on either side, and
// an unset or unrecognized value on either side also resolves to personal.
//
// Personal-wins is the direction that cannot leak: a personal file that
// also sits inside a project tree must not become project-visible because
// the corpus row said "project".
func resolvePrivacy(record, corpusClass PrivacyClass) PrivacyClass {
	if !record.Valid() || !corpusClass.Valid() {
		return PrivacyPersonal
	}
	if record == PrivacyPersonal || corpusClass == PrivacyPersonal {
		return PrivacyPersonal
	}
	if record == PrivacyGlobal || corpusClass == PrivacyGlobal {
		return PrivacyGlobal
	}
	return PrivacyProject
}

// permits reports whether a query holding entitlement p may see content
// classified content.
//
// Only a personal entitlement sees personal content. Every other pairing
// is decided by the membership and visibility rules, not here. An invalid
// entitlement sees nothing personal, and invalid content resolves to
// personal upstream, so an unreadable value on either side denies.
func (p PrivacyClass) permits(content PrivacyClass) bool {
	if !content.Valid() {
		return false
	}
	if content == PrivacyPersonal {
		return p == PrivacyPersonal
	}
	return p.Valid()
}
