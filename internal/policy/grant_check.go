// Package policy (grant_check.go): Purpose: StoreGrants' read path —
//
//	Check, the single fail-closed decision every permission question in
//	Epic I funnels through, and List. Split from grant.go as a sibling
//	file per R-14.117 (Art.10.3's 300-line cap); no behavior lives here
//	that grant.go's contract does not describe.
//
// Inputs: a CheckRequest, or a Subject to list for.
// Outputs: a Decision, a []Grant, or a typed refusal.
// Constraints: every step below is a DENY step. There is no branch that
//
//	turns an unreadable input into a permission, and there is no cache
//	between Check and the store, so a revoked grant is gone on the very
//	next call.
//
// SPORT: internal/policy StoreGrants.Check/ADDED,
//
//	StoreGrants.List/ADDED (P1-E09-W2-S17-T1).
package policy

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Check implements GrantStore.
//
// The order of the checks is the security contract, and each one returns
// the zero Decision alongside its error:
//
//  1. the subject must name somebody (subject-unknown);
//  2. the capability must be REGISTERED (capability-not-found) — an
//     unknown capability denies before any grant row is read, so a grant
//     row naming a capability that was never registered, or was removed,
//     can never be honoured;
//  3. a grant row must exist for exactly this subject and capability
//     (grant-denied on absent);
//  4. the row must decode, and must validate after decoding
//     (grant-denied) — an unparseable row is a denial, never an allow;
//  5. the decoded row must name the same subject and capability its key
//     claims (grant-denied), so a row moved or forged under another key
//     does not authorise the key it was moved to;
//  6. the grant must not have expired, measured against the injected
//     clock (grant-denied, naming the expiry instant);
//  7. every one of the grant's conditions must be matched exactly by the
//     request's attributes (grant-denied) — a missing attribute denies,
//     an empty condition map is unconditional and narrows nothing.
func (s *StoreGrants) Check(ctx context.Context, req CheckRequest) (Decision, error) {
	if err := req.Subject.Validate(); err != nil {
		return Decision{}, err
	}
	capDef, err := s.registry.Lookup(ctx, req.Capability)
	if err != nil {
		return Decision{}, err
	}
	g, err := s.load(ctx, req.Subject, req.Capability)
	if err != nil {
		return Decision{}, err
	}
	if err := s.checkExpiry(g); err != nil {
		return Decision{}, err
	}
	if err := checkConditions(g, req.Attributes); err != nil {
		return Decision{}, err
	}
	return Decision{
		Granted:    true,
		Capability: capDef,
		ScopeClass: g.ScopeClass,
		ExpiresAt:  g.ExpiresAt,
	}, nil
}

// load reads and re-validates the stored grant for subject/capability.
// Steps 3 to 5 of Check's contract live here.
func (s *StoreGrants) load(ctx context.Context, subject Subject, capability string) (Grant, error) {
	key, err := grantKey(subject, capability)
	if err != nil {
		return Grant{}, err
	}
	raw, err := s.store.Get(ctx, s.namespace(), key)
	if err != nil {
		if errors.Is(err, cascade.ErrNotFound) {
			return Grant{}, newGrantDenied("%s holds no grant on %q",
				subject, sanitize(capability))
		}
		return Grant{}, err
	}
	var g Grant
	if err := json.Unmarshal(raw, &g); err != nil {
		// An undecodable row is a denial, not an error the caller might
		// mistake for a transport failure and retry into an allow.
		return Grant{}, newGrantDenied("%s: stored grant on %q could not be decoded",
			subject, sanitize(capability))
	}
	if err := g.Validate(); err != nil {
		return Grant{}, newGrantDenied("%s: stored grant on %q is not valid",
			subject, sanitize(capability))
	}
	if g.Subject != subject || g.Capability != capability {
		return Grant{}, newGrantDenied(
			"%s: stored grant on %q names %s on %q instead",
			subject, sanitize(capability), g.Subject, sanitize(g.Capability))
	}
	return g, nil
}

// checkExpiry denies an expired grant. The comparison is against the
// injected clock and is exclusive at the boundary: a grant whose ExpiresAt
// equals now has expired, because "expires at T" means it is not valid AT
// T. The refusal names the expiry instant, per the acceptance criterion.
func (s *StoreGrants) checkExpiry(g Grant) error {
	if g.ExpiresAt.IsZero() {
		return nil
	}
	now := s.clock.Now()
	if !now.Before(g.ExpiresAt) {
		return newGrantDenied("%s: grant on %q expired at %s",
			g.Subject, g.Capability, g.ExpiresAt.UTC().Format(time.RFC3339))
	}
	return nil
}

// checkConditions requires every condition on the grant to be matched
// exactly by the request's attributes.
//
// Deny-by-default in both directions that matter: a condition whose
// attribute is absent denies, and a condition whose attribute differs
// denies. Extra attributes the grant does not mention are ignored, because
// a grant that says nothing about an attribute is not narrowed by it.
func checkConditions(g Grant, attrs map[string]string) error {
	for k, want := range g.Conditions {
		got, ok := attrs[k]
		if !ok {
			return newGrantDenied("%s: grant on %q requires condition %q, which the request does not carry",
				g.Subject, g.Capability, sanitize(k))
		}
		if got != want {
			return newGrantDenied("%s: grant on %q requires condition %q to match",
				g.Subject, g.Capability, sanitize(k))
		}
	}
	return nil
}

// List implements GrantStore. It returns every grant subject holds, in
// storage-key order.
//
// A row that does not decode, or that decodes into a grant belonging to
// another subject, fails the whole call rather than being silently
// skipped: a listing that quietly omits a row would let a corrupted grant
// hide from the operator reviewing what a subject holds. Nothing here
// grants anything — List is presentational, and Check is the only read a
// permission decision may rely on.
func (s *StoreGrants) List(ctx context.Context, subject Subject) ([]Grant, error) {
	if err := subject.Validate(); err != nil {
		return nil, err
	}
	prefix := grantKeyPrefix + string(subject.Kind) + "/" + subject.ID + "/"
	it, err := s.store.Scan(ctx, s.namespace(), prefix)
	if err != nil {
		return nil, err
	}
	defer func() { _ = it.Close() }()
	return collectGrants(ctx, it, subject)
}

// collectGrants drains it, decoding and re-validating every row. Split out
// so List stays inside Art.10.3's 50-line function cap.
func collectGrants(ctx context.Context, it provider.Iterator, subject Subject) ([]Grant, error) {
	var out []Grant
	for it.Next(ctx) {
		var g Grant
		if err := json.Unmarshal(it.Value(), &g); err != nil {
			return nil, newGrantDenied("%s: stored grant at %q could not be decoded",
				subject, sanitize(it.Key()))
		}
		if err := g.Validate(); err != nil {
			return nil, newGrantDenied("%s: stored grant at %q is not valid",
				subject, sanitize(it.Key()))
		}
		if g.Subject != subject {
			return nil, newGrantDenied("%s: stored grant at %q names %s instead",
				subject, sanitize(it.Key()), g.Subject)
		}
		out = append(out, g)
	}
	if err := it.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
