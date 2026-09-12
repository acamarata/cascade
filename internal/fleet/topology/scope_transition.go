// Purpose: R-21.126's reservation-rollback cascade. This package owns no
//
//	reservation type and imports no economics package -- OnScopeTransition
//	and OnDomainQuarantined only compute the transition and invoke the
//	INJECTED CascadeFn (AO/S-79.T4's reserve_cascade.go implements it and
//	is bound at the composition root).
//
// Inputs: a LimitScopeID or DomainID and the target ScopeState. Outputs: a
//
//	Cascade value describing what the injected function rolled back.
//
// Constraints: RATE_LIMITED (represented here as one CascadeFn invocation)
//
//	fires exactly ONCE per actual state transition -- a repeat call that
//	would not change scopeState is a no-op returning the cached Cascade,
//	never a second invocation for the same transition.
//
// SPORT: fleet/topology/scope_transition/ADD (P1-E40-W9-S78-T1).

package topology

import (
	"context"
	"time"
)

// ScopeState is the closed vocabulary OnScopeTransition drives a scope
// through. The zero value is invalid.
type ScopeState string

// The three closed ScopeState members.
const (
	ScopeStateOK          ScopeState = "ok"
	ScopeStateRateLimited ScopeState = "rate_limited"
	ScopeStateQuarantined ScopeState = "quarantined"
)

// Valid reports whether s is one of the three declared members.
func (s ScopeState) Valid() bool {
	switch s {
	case ScopeStateOK, ScopeStateRateLimited, ScopeStateQuarantined:
		return true
	}
	return false
}

// Cascade reports what one scope or domain state transition rolled back:
// every affected reservation, as an opaque description from the injected
// CascadeFn (AO/S-79.T4 owns the reservation row and its states; this
// package interprets nothing in AffectedStates beyond passing it through).
type Cascade struct {
	Scope          LimitScopeID
	AffectedStates []string
}

// CascadeFn is the injected AO/S-79.T4 seam: given a scope transitioning
// to state, roll back every held/parked reservation on it to
// rolled_back, leave every committed one to finish with retries deferred
// to reset_at, and report what changed. Bound at the composition root --
// this package never implements it.
type CascadeFn func(ctx context.Context, scope LimitScopeID, state ScopeState) (Cascade, error)

// OnScopeTransition drives scope to state (until matters only for
// ScopeStateRateLimited, recorded via on429's own path -- this method is
// the explicit-call surface a caller uses outside the OnProviderError
// flow, e.g. from an admin command). It invokes the injected CascadeFn
// exactly once per actual transition: a call that leaves scopeState
// unchanged returns the previously cached Cascade without a second
// invocation, satisfying "RATE_LIMITED emitted once per scope
// transition, never once per call".
func (c *Chooser) OnScopeTransition(ctx context.Context, scope LimitScopeID, to ScopeState, until time.Time) (Cascade, error) {
	if scope == "" {
		return Cascade{}, newInvariantErr("scope_transition", "", "scope must not be empty")
	}
	if !to.Valid() {
		return Cascade{}, newInvariantErr("scope_transition", string(scope), "unrecognised scope state")
	}
	c.mu.Lock()
	prev, seen := c.scopeState[scope]
	changed := !seen || prev != to
	if changed {
		c.scopeState[scope] = to
		if to == ScopeStateRateLimited {
			u := until
			if u.IsZero() {
				u = c.clock.Now().Add(defaultRateLimitProbe)
			}
			c.scopeConstrainedUntil[scope] = u
		}
	}
	cached, hasCached := c.scopeCascade[scope]
	c.mu.Unlock()

	if !changed && hasCached {
		return cached, nil
	}
	if c.cascadeFn == nil {
		return Cascade{}, newInvariantErr("scope_transition", string(scope), "no CascadeFn configured (fail closed)")
	}
	casc, err := c.cascadeFn(ctx, scope, to)
	if err != nil {
		return Cascade{}, err
	}
	c.mu.Lock()
	c.scopeCascade[scope] = casc
	c.mu.Unlock()
	return casc, nil
}

// OnDomainQuarantined drives dom's every referencing scope to
// ScopeStateQuarantined (R-21.24: "every lane whose domain is quarantined
// becomes quarantined"), by resolving dom's own scope and delegating to
// OnScopeTransition -- there is no second cascade implementation here.
func (c *Chooser) OnDomainQuarantined(ctx context.Context, dom DomainID) (Cascade, error) {
	c.mu.Lock()
	domain, ok := c.domains[dom]
	account := c.accounts[domain.AccountRef]
	c.domainQuarantined[dom] = true
	c.mu.Unlock()
	if !ok {
		return Cascade{}, newNotFoundErr("quota_domain", string(dom))
	}
	scope := ResolveScope(account, domain)
	return c.OnScopeTransition(ctx, scope, ScopeStateQuarantined, time.Time{})
}
