// Purpose: scope membership for retrieval. A corpus and every record in
// it carry a scope_ref, the id of the session scope that owns them. A
// session asking for content presents its own scope, the chain that scope
// resolved through, and the relationship edges declared for it. Membership
// answers exactly one question: is this owning scope inside the candidate
// set for that session. Everything outside the candidate set is denied,
// and that denial happens before any ranking, never after it.
//
// Inputs: a Membership (the session's own scope, its resolved chain, and
// its declared edges) and the scope_ref of the content being considered.
//
// Outputs: a membership decision, and the validation refusals for a
// malformed scope reference or an unrecognized edge kind.
//
// Constraints: deny-by-default. An empty membership admits nothing, not
// everything. An empty or malformed scope reference is never a wildcard
// and never matches. The edge kinds are exactly the three the session
// scope model declares; an unrecognized kind fails validation rather than
// being treated as an ordinary edge. The scope id type is deliberately
// opaque here: the resolver that produces session scope ids is a separate
// ticket, and this package must not grow a second, divergent notion of how
// a scope id is built.
//
// SPORT: internal.retrieval.corpus.ScopeRef/ADDED.

package corpus

import (
	"strings"
	"unicode"

	"github.com/acamarata/cascade/pkg/cascade"
)

// maxScopeRefLen bounds a scope id so a malformed or hostile value cannot
// become an unbounded key. The session scope resolver produces short
// composite ids; this is well above any legitimate one.
const maxScopeRefLen = 512

// ScopeRef is the id of a session scope, as produced by the session scope
// resolver. This package treats it as an opaque identifier and never
// parses structure out of it, so the resolver stays the one place that
// decides how a scope id is composed.
type ScopeRef string

// Valid reports whether s is a usable scope reference: non-empty, within
// the length bound, and free of whitespace and control characters.
//
// The whitespace rule matters more than it looks. A reference that differs
// from another only by a trailing space would be a distinct map key while
// reading as the same scope to a human, which is how a leak gets reviewed
// and approved.
func (s ScopeRef) Valid() bool {
	if s == "" || len(s) > maxScopeRefLen {
		return false
	}
	for _, r := range string(s) {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// EdgeKind is the kind of a declared relationship between two scopes.
// These are the three kinds the session scope model defines; there is no
// implicit fourth kind and no default.
type EdgeKind string

const (
	// EdgeDependsOn declares that the source scope depends on the target,
	// so the target's shared content is a legitimate candidate for it.
	EdgeDependsOn EdgeKind = "depends_on"

	// EdgeMemberOf declares that the source scope belongs to the target,
	// for example a package inside a workspace declared as a unit.
	EdgeMemberOf EdgeKind = "member_of"

	// EdgeSharesContextWith declares a deliberate symmetric context link
	// between two otherwise unrelated scopes.
	EdgeSharesContextWith EdgeKind = "shares_context_with"
)

// Valid reports whether k is one of the three declared edge kinds.
func (k EdgeKind) Valid() bool {
	switch k {
	case EdgeDependsOn, EdgeMemberOf, EdgeSharesContextWith:
		return true
	default:
		return false
	}
}

// Edge is one declared relationship from a session's scope to another
// scope. Edges are explicit data written at init or by the user; nothing
// in this package infers one.
type Edge struct {
	// Kind is the relationship kind. An unrecognized kind fails
	// validation, so an edge whose meaning is unknown never widens a
	// candidate set.
	Kind EdgeKind `json:"kind"`
	// Target is the scope this edge reaches.
	Target ScopeRef `json:"target"`
}

// Validate reports whether the edge is usable.
func (e Edge) Validate() error {
	if !e.Kind.Valid() {
		return cascade.Newf(cascade.KindInvalidInput,
			"corpus: %q is not a declared edge kind", string(e.Kind))
	}
	if !e.Target.Valid() {
		return cascade.Newf(cascade.KindInvalidInput,
			"corpus: edge %s has an invalid target scope reference", string(e.Kind))
	}
	return nil
}

// Membership is the candidate set for one session: the scope it resolved
// to, the chain that scope sits in, and the relationship edges declared
// for it. It is the whole input to every scope decision this package
// makes, so a caller cannot accidentally widen a decision by omitting a
// field.
type Membership struct {
	// Scope is the session's own resolved scope.
	Scope ScopeRef `json:"scope"`
	// Chain is the scope's resolved ancestry, outermost first. Scope
	// itself need not be repeated here; inChain covers it either way.
	Chain []ScopeRef `json:"chain,omitempty"`
	// Edges are the relationships declared for Scope.
	Edges []Edge `json:"edges,omitempty"`
}

// Validate reports whether the membership is usable. A membership whose
// own scope is malformed is refused outright rather than being quietly
// narrowed to nothing, because a caller that built a broken membership has
// a bug the caller needs told about.
func (m Membership) Validate() error {
	if !m.Scope.Valid() {
		return cascade.New(cascade.KindInvalidInput,
			"corpus: membership has an invalid scope reference")
	}
	for _, c := range m.Chain {
		if !c.Valid() {
			return cascade.New(cascade.KindInvalidInput,
				"corpus: membership chain holds an invalid scope reference")
		}
	}
	for _, e := range m.Edges {
		if err := e.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// inChain reports whether ref is the session's own scope or one of its
// resolved ancestors. An invalid ref never matches, including against an
// equally invalid membership scope.
func (m Membership) inChain(ref ScopeRef) bool {
	if !ref.Valid() {
		return false
	}
	if m.Scope == ref {
		return true
	}
	for _, c := range m.Chain {
		if c == ref {
			return true
		}
	}
	return false
}

// acrossEdge reports whether ref is the target of a declared, valid edge.
// An edge whose kind or target failed validation is skipped rather than
// honored, so a malformed edge can never be the thing that admits a record
// from another scope.
func (m Membership) acrossEdge(ref ScopeRef) bool {
	if !ref.Valid() {
		return false
	}
	for _, e := range m.Edges {
		if e.Validate() == nil && e.Target == ref {
			return true
		}
	}
	return false
}

// describeScope renders a scope reference for an error message without
// echoing a control character or an unbounded string back into a log line.
func describeScope(s ScopeRef) string {
	const limit = 64
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, string(s))
	if len(cleaned) > limit {
		return cleaned[:limit] + "..."
	}
	if cleaned == "" {
		return "(empty)"
	}
	return cleaned
}
