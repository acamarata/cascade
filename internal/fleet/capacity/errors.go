// Purpose: this ticket's two sentinel errors, each wrapping exactly one
// frozen pkg/cascade.Kind -- no new taxonomy kind is defined here
// (06-FORGE-SPEC.md §2).
//
// Inputs: none. Outputs: *cascade.Error sentinels tier_policy.go's
// SelectTier returns.
// Constraints: never invent a cascade.Kind.
//
// SPORT: fleet.capacity.policy (ADD, P1-E31-W6-S63-T2).

package capacity

import "github.com/acamarata/cascade/pkg/cascade"

// ErrFleetExhausted reports that every tier the walk considered is
// unavailable right now (KindUnavailable: a temporarily unreachable
// dependency the caller may retry, not a spent purchased quota).
var ErrFleetExhausted = cascade.New(cascade.KindUnavailable, "capacity: every tier is exhausted or unavailable")

// ErrNoAllowedLanes reports that every tier the walk considered is
// disallowed by the caller's user-config lane policy (KindPolicyDenied:
// the refusal comes from policy, not from provider capacity).
var ErrNoAllowedLanes = cascade.New(cascade.KindPolicyDenied, "capacity: every tier is disallowed by user-config lane policy")
