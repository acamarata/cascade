// Package policy (grant.go): Purpose: the grant model — which subjects
//
//	(users, agents, plugins) hold which capabilities, under what
//	conditions, at what reach, until when — and the fail-closed Check that
//	every permission decision in Epic I funnels through.
//
// Inputs: a Grant on write; a CheckRequest (subject, capability, request
//
//	attributes) on read. Persistence goes through the B-layer
//	provider.Store abstraction, in the `policy` storage domain
//	(storage.DomainPolicy, the eleventh DomainID, registered by this
//	ticket per R-16.51) — never direct SQLite.
//
// Outputs: a Decision, or one of three typed refusals: capability-not-found,
//
//	subject-unknown, grant-denied. A refusal is always accompanied by the
//	zero Decision, whose Granted field is false, so a caller that ignores
//	the error still denies.
//
// Constraints: FAIL CLOSED with no exceptions. An unknown capability, an
//
//	unparseable stored grant, a missing or malformed subject, an expired
//	grant, an unsatisfied condition, a malformed scope class: each denies,
//	and none degrades into a match-all. There is NO grant cache: Check
//	reads the store on every call, so a Revoke takes effect on the very
//	next decision. Expiry is compared against an injected Clock (Art.7.3),
//	never a bare time.Now.
//
// SPORT: internal/policy Subject/ADDED, Grant/ADDED, GrantStore/ADDED
//
//	(P1-E09-W2-S17-T1). The B-layer implementation is grant_store.go.
package policy

import (
	"context"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The stable identifier strings for this file's refusals, in the A-T7
// spelling the contract names (R-14.152: the fourteen-Kind taxonomy is
// frozen, so a contract identifier lives on as a stable string).
const (
	// CodeGrantDenied marks every refusal of a permission: absent,
	// revoked, expired, condition-unsatisfied and undecodable all collapse
	// to this one code, so a caller cannot learn from the refusal which of
	// those it was.
	CodeGrantDenied = "grant-denied"
	// CodeSubjectUnknown marks a request whose subject is missing or
	// malformed — a request that names nobody, which can never be granted
	// anything.
	CodeSubjectUnknown = "subject-unknown"
)

// maxSubjectIDLen bounds a subject identifier, for the same reason
// maxCapabilityNameLen bounds a capability name.
const maxSubjectIDLen = 128

// grantKeyPrefix namespaces grant rows inside the `policy` domain, leaving
// room for the domain's other tenants (standing grants, deny-list
// patterns, autonomy-profile state, the classifier cache).
const grantKeyPrefix = "grant/"

// SubjectKind is what sort of principal a Subject names. The zero value is
// not a kind, so a Subject whose kind was never set fails validation.
type SubjectKind string

const (
	// SubjectUser is a human principal.
	SubjectUser SubjectKind = "user"
	// SubjectAgent is an agent or lane acting on the user's behalf.
	SubjectAgent SubjectKind = "agent"
	// SubjectPlugin is a plugin, whose permission checks O/S-31 routes
	// through this same store.
	SubjectPlugin SubjectKind = "plugin"
)

// Valid reports whether k is one of the three defined kinds.
func (k SubjectKind) Valid() bool {
	return k == SubjectUser || k == SubjectAgent || k == SubjectPlugin
}

// String returns k's stored spelling, or "invalid" for an undefined value.
// It never invents a spelling, because a plausible-looking one is how an
// unknown value round-trips as a real one.
func (k SubjectKind) String() string {
	if !k.Valid() {
		return "invalid"
	}
	return string(k)
}

// Subject is the principal a grant is held by.
type Subject struct {
	Kind SubjectKind `json:"kind"`
	ID   string      `json:"id"`
}

// Validate refuses a subject that names nobody. Both fields are required;
// there is no anonymous subject and no wildcard subject.
func (s Subject) Validate() error {
	if !s.Kind.Valid() {
		return newSubjectUnknown("%q is not a subject kind", sanitize(string(s.Kind)))
	}
	if s.ID == "" {
		return newSubjectUnknown("subject of kind %s has no id", s.Kind)
	}
	if len(s.ID) > maxSubjectIDLen {
		return newSubjectUnknown("subject id is %d bytes, over the %d-byte limit",
			len(s.ID), maxSubjectIDLen)
	}
	if !validSubjectID(s.ID) {
		return newSubjectUnknown("%q is not a well-formed subject id", sanitize(s.ID))
	}
	return nil
}

// String renders the subject as "kind:id" for messages and keys.
func (s Subject) String() string { return s.Kind.String() + ":" + s.ID }

// validSubjectID admits only [a-zA-Z0-9_.:-]. Deny-by-default, and the
// excluded characters are the load-bearing part: no "/" means a subject id
// can never forge a second key segment, and no control characters means it
// can never forge a log line.
func validSubjectID(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '_', r == '-', r == '.', r == ':':
		default:
			return false
		}
	}
	return true
}

// Grant is one subject's hold on one capability.
type Grant struct {
	// Subject is the principal holding the grant.
	Subject Subject `json:"subject"`
	// Capability is the name of the capability held. It must be
	// registered: a grant on an unregistered name is refused at write and
	// denied at read.
	Capability string `json:"capability"`
	// Conditions narrow the grant. Every pair here must be matched
	// exactly by the request's attributes for the grant to apply; an empty
	// map means unconditional. Conditions can only narrow — there is no
	// condition that widens a grant.
	Conditions map[string]string `json:"conditions,omitempty"`
	// ScopeClass is the widest reach this grant confers, expressed in
	// corpus's own visibility vocabulary. It is a CEILING the carrier
	// narrows against, never a floor: see TeamCarrier.EffectiveClass.
	ScopeClass corpus.VisibilityClass `json:"scope_class"`
	// ExpiresAt is when the grant stops applying. The zero value means it
	// does not expire on its own; a Revoke still ends it immediately.
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	// Verdict is what a match yields, so a grant can say "ask" or "deny"
	// as well as "allow" (R-21.237). A row that names no verdict yields
	// allow, which is what a grant record has meant since the grant model
	// landed: the row's existence IS the authorization, and a grant is
	// written only by an authorizing act. Read it through
	// EffectiveVerdict, never directly.
	Verdict Verdict `json:"verdict,omitempty"`
}

// EffectiveVerdict is what a matching grant yields. An out-of-range value
// reads as deny (safeVerdict), and an UNSET value reads as allow, per the
// field's own documented meaning.
func (g Grant) EffectiveVerdict() Verdict {
	if g.Verdict == 0 {
		return VerdictAllow
	}
	return safeVerdict(g.Verdict)
}

// Validate refuses a grant that could not be evaluated. It does not
// consult the registry — that is the store's job, because only the store
// knows which registry a grant is being written against.
func (g Grant) Validate() error {
	if err := g.Subject.Validate(); err != nil {
		return err
	}
	if err := validateCapabilityName(g.Capability); err != nil {
		return err
	}
	if !g.ScopeClass.Valid() {
		return newGrantDenied("grant for %s on %q: %q is not a visibility class",
			g.Subject, g.Capability, sanitize(string(g.ScopeClass)))
	}
	for k, v := range g.Conditions {
		if strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" {
			return newGrantDenied("grant for %s on %q: empty condition key or value",
				g.Subject, g.Capability)
		}
	}
	return nil
}

// key is the grant's storage key. It is built from validated components
// only, so no component can inject a separator.
func (g Grant) key() string {
	return grantKeyPrefix + string(g.Subject.Kind) + "/" + g.Subject.ID + "/" + g.Capability
}

// CheckRequest is one permission question, carried as a single value so a
// caller cannot omit half of it and get a wider answer than it is entitled
// to.
type CheckRequest struct {
	// Subject is who is asking.
	Subject Subject
	// Capability is what they are asking to do.
	Capability string
	// Attributes are the request's facts, matched against the grant's
	// Conditions. A condition with no matching attribute denies.
	Attributes map[string]string
}

// Decision is the outcome of a Check. Its zero value denies: Granted is
// false and ScopeClass is the empty (invalid) class, which
// narrowerVisibility collapses to private. A caller that ignores Check's
// error therefore still denies.
type Decision struct {
	// Granted is true only on an allowed decision.
	Granted bool
	// Capability is the registered capability the decision was made
	// against, carrying its action class.
	Capability Capability
	// ScopeClass is the grant's reach ceiling, to be narrowed against the
	// record's own class before it is used.
	ScopeClass corpus.VisibilityClass
	// ExpiresAt is the grant's expiry, zero when it does not expire.
	ExpiresAt time.Time
	// Verdict is the matched grant's own verdict, already resolved
	// through Grant.EffectiveVerdict.
	Verdict Verdict
}

// Clock abstracts time.Now so no expiry check reads the wall clock
// directly (Art.7.3, forbidigo). Declared locally and duck-typed, exactly
// as internal/storage.Clock is: internal/runtime's and internal/testkit's
// concrete clocks satisfy it with no adapter code.
type Clock interface {
	// Now returns the current instant.
	Now() time.Time
}

// GrantStore is the persistent grant model.
type GrantStore interface {
	// Grant writes g, replacing any existing grant the same subject holds
	// on the same capability. The capability must be registered.
	Grant(ctx context.Context, g Grant) error
	// Revoke removes the grant subject holds on capability. It takes
	// effect immediately: the next Check reads the store and finds
	// nothing. Revoking an absent grant is grant-denied, not a silent
	// success — a caller that thinks it revoked something must hear that
	// it did not.
	Revoke(ctx context.Context, subject Subject, capability string) error
	// Check answers one permission question. A non-nil error always means
	// denied, and is always returned with the zero Decision.
	Check(ctx context.Context, req CheckRequest) (Decision, error)
	// List returns every grant subject holds, ordered by storage key.
	List(ctx context.Context, subject Subject) ([]Grant, error)
}

// ErrGrantDenied and ErrSubjectUnknown are the comparison targets for this
// file's refusals, and every refusal built below WRAPS one of them, so
// errors.Is finds the specific refusal by identity rather than only the
// kind it presents as.
//
// The two carry different kinds deliberately. A denied grant is
// KindCapabilityDenied: the request was well formed and the answer is no.
// An unknown subject is KindInvalidInput: the request named nobody, so
// there was never a question to answer. Both are refusals — a non-nil
// error is always a denial here — but a caller can tell them apart, which
// it could not do if they shared a kind, because the taxonomy's own Is
// compares kinds alone.
var (
	ErrGrantDenied    = cascade.New(cascade.KindCapabilityDenied, CodeGrantDenied)
	ErrSubjectUnknown = cascade.New(cascade.KindInvalidInput, CodeSubjectUnknown)
)

// newGrantDenied builds a grant-denied refusal.
func newGrantDenied(format string, args ...any) error {
	return cascade.Wrapf(cascade.KindCapabilityDenied, ErrGrantDenied,
		"policy: "+CodeGrantDenied+": "+format, args...)
}

// newSubjectUnknown builds a subject-unknown refusal.
func newSubjectUnknown(format string, args ...any) error {
	return cascade.Wrapf(cascade.KindInvalidInput, ErrSubjectUnknown,
		"policy: "+CodeSubjectUnknown+": "+format, args...)
}
