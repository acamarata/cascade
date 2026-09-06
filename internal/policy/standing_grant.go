// Package policy (standing_grant.go): Purpose: the standing-grant schema
//
//	and its one creation path. A standing grant is an approval given once
//	and honoured repeatedly, so the guards that decide whether one may
//	exist at all run BEFORE anything is written.
//
// Inputs: a StandingGrant, an injected DenyListEngine, the I/S-17.T1
//
//	GrantStore and a Clock. All four are required.
//
// Outputs: StandingGrant, CreateStandingGrant, ErrDeniedClass.
// Constraints: FAIL CLOSED. Two guards run first and both must pass: the
//
//	action must not be deny-listed for its class, and it must not be a
//	06-FORGE-SPEC §5.14 elevation-class verb (§5.24: those are LOCAL-ONLY
//	and never standing). Either violation, a missing collaborator, or an
//	engine that could not answer, returns ErrDeniedClass and touches no
//	storage. R-21.237: rows are written through the ONE GrantStore, so the
//	evaluation stack's layer 3 reads them through the same API it reads
//	capability grants through, with no second store and no second reader.
//
// SPORT: internal/policy StandingGrant/ADDED, CreateStandingGrant/ADDED,
//
//	ErrDeniedClass/ADDED (P1-E08-W2-S16-T3).
package policy

import (
	"context"
	"errors"
	"time"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/pkg/cascade"
)

// CodeDeniedClass is the stable identifier for a refused standing grant
// (R-14.152: the taxonomy is frozen, so the reason survives as a string).
const CodeDeniedClass = "standing-grant-denied-class"

// ErrDeniedClass is the comparison target for every refusal in this file:
// a deny-listed action, an elevation-class verb, a missing collaborator,
// and a deny-list engine that could not answer.
var ErrDeniedClass = errors.New(CodeDeniedClass)

// StandingGrant is an approval that outlives the request that earned it.
//
// It names the ACTION as well as its class because both guards need both:
// the deny-list is queried by (class, action), and the §5.14 local-only
// set is a set of verbs. Capability is present because R-21.237 routes
// these rows through the one GrantStore, which addresses every row by a
// registered capability name.
type StandingGrant struct {
	// GrantID identifies this grant.
	GrantID cascade.ID
	// ActionClass is the class of work the grant covers, consumed from
	// I/S-17.T3's enum; this file defines no class of its own.
	ActionClass ActionClass
	// Action is the verb the grant covers.
	Action string
	// Capability is the registered capability the row is stored under.
	Capability string
	// Scope narrows the grant, e.g. to one repository path. It is stored
	// as a condition, so a request outside the scope does not match.
	Scope string
	// Grantee is the principal holding the grant.
	Grantee Subject
	// IssuedAt is when the grant was created, from the injected clock.
	IssuedAt time.Time
	// Exp is when it stops applying.
	Exp time.Time
}

// StandingGrantDeps are the collaborators CreateStandingGrant needs. They
// are carried as one value so a caller cannot supply half of them and get
// a grant written with one of the two guards unrun.
type StandingGrantDeps struct {
	// Grants is the I/S-17.T1 GrantStore. There is no second store.
	Grants GrantStore
	// DenyList answers the class-keyed membership question.
	DenyList DenyListEngine
	// Clock stamps IssuedAt.
	Clock Clock
}

// CreateStandingGrant runs both guards and, only if both pass, writes the
// grant through the one GrantStore.
//
// The refusal is deliberately uniform: a caller learns that the class was
// denied, not which of the two guards said so and not whether the
// deny-list engine was reachable. A grant creation path that reports the
// difference is a probe for the deny-list's contents.
func CreateStandingGrant(ctx context.Context, deps StandingGrantDeps, g StandingGrant) error {
	if deps.Grants == nil || deps.DenyList == nil || deps.Clock == nil {
		return newDeniedClass("standing grants require a grant store, a deny-list engine and a clock")
	}
	if err := g.validate(); err != nil {
		return err
	}
	if IsElevationClassVerb(g.Action) {
		return newDeniedClass("%q is an elevation-class verb and can never be standing", sanitize(g.Action))
	}
	denied, err := deps.DenyList.ContainsClass(ctx, g.ActionClass, g.Action)
	if err != nil {
		return newDeniedClass("the deny-list could not be consulted for %q", sanitize(g.Action))
	}
	if denied {
		return newDeniedClass("%q is deny-listed for class %s", sanitize(g.Action), g.ActionClass)
	}
	g.IssuedAt = deps.Clock.Now()
	return deps.Grants.Grant(ctx, g.toGrant())
}

// validate refuses a grant that could not be evaluated. An unset action
// class fails here rather than being coerced, because safeActionClass
// would make it destructive_privileged, and a grant on that class is a
// grant nobody meant to ask for.
func (g StandingGrant) validate() error {
	if !g.GrantID.Valid() {
		return newDeniedClass("standing grant has no well-formed grant id")
	}
	if !g.ActionClass.Valid() {
		return newDeniedClass("standing grant names no action class")
	}
	if g.Action == "" {
		return newDeniedClass("standing grant names no action")
	}
	if g.Exp.IsZero() {
		return newDeniedClass("standing grant for %q has no expiry", sanitize(g.Action))
	}
	return nil
}

// toGrant projects the standing grant onto the one Grant row shape.
//
// The verdict is set EXPLICITLY to allow rather than left at the zero
// value: R-21.237 gives Grant a verdict with no permissive zero, and a
// standing grant that did not say what it yields would be relying on a
// default to mean yes. The scope class is the narrowest one, because a
// standing grant authorizes a VERB and never a disclosure: widening what
// material may travel is layer 0's decision, not this row's.
func (g StandingGrant) toGrant() Grant {
	conditions := map[string]string{
		"action":            g.Action,
		"action_class":      g.ActionClass.String(),
		"standing_grant_id": g.GrantID.String(),
	}
	if g.Scope != "" {
		conditions["scope"] = g.Scope
	}
	return Grant{
		Subject:    g.Grantee,
		Capability: g.Capability,
		Conditions: conditions,
		ScopeClass: corpus.VisibilityPrivate,
		ExpiresAt:  g.Exp,
		Verdict:    VerdictAllow,
	}
}

// newDeniedClass builds a refusal wrapping ErrDeniedClass and presenting
// as KindPolicyDenied, so errors.Is finds the specific refusal and the
// rest of the system treats it like any other policy refusal.
func newDeniedClass(format string, args ...any) error {
	return cascade.Wrapf(cascade.KindPolicyDenied, ErrDeniedClass,
		"policy: "+format, args...)
}
