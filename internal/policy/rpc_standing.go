// Package policy (rpc_standing.go): Purpose: the standing_grant.* half of
//
//	the Epic I handler set: list, create, change and revoke. Split from
//	rpc_policy.go under Art.10.3's 300-line cap, which that file crossed at
//	343 lines; the guard, the params decoder and RPCDeps live in rpc.go and
//	are shared by both halves.
//
// Inputs: raw JSON params and the RPCDeps collaborators.
// Outputs: the four standing-grant handlers and their params/result types.
// Constraints: these verbs add NO second guard. They call
//
//	CreateStandingGrant, whose deny-list and elevation-class guards run
//	before any storage write, and re-present its ErrDeniedClass as the
//	permission-denied kind. create and change deliberately share one
//	guarded write: a change that skipped the deny-list guard would be a way
//	to reach a denied class in two steps.
//
// SPORT: internal/policy rpc-standing-handlers/ADDED (P1-E09-W2-S18-T6).
package policy

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// StandingListParams names whose standing grants to list.
type StandingListParams struct {
	// Subject is the grantee.
	Subject Subject `json:"subject"`
}

// StandingListResult is the list of grants one subject holds.
type StandingListResult struct {
	// Grants are the subject's grants, ordered by storage key.
	Grants []Grant `json:"grants"`
}

// standingList returns the grants one subject holds.
func (d RPCDeps) standingList(ctx context.Context, params json.RawMessage) (any, error) {
	var p StandingListParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if d.Grants == nil {
		return nil, cascade.New(cascade.KindUnavailable, "policy: no grant store is wired in this process")
	}
	grants, err := d.Grants.List(ctx, p.Subject)
	if err != nil {
		return nil, err
	}
	if grants == nil {
		grants = []Grant{}
	}
	return StandingListResult{Grants: grants}, nil
}

// StandingWriteParams is what standing_grant.create and
// standing_grant.change are called with. Both verbs take the same shape:
// a change is the same guarded write with new terms, addressed to the same
// row, which is why neither verb has a second params type that could
// diverge from the other.
type StandingWriteParams struct {
	// GrantID identifies the grant.
	GrantID string `json:"grant_id"`
	// ActionClass is the class of work the grant covers.
	ActionClass ActionClass `json:"action_class"`
	// Action is the verb the grant covers.
	Action string `json:"action"`
	// Capability is the registered capability the row is stored under.
	Capability string `json:"capability"`
	// Scope narrows the grant.
	Scope string `json:"scope"`
	// Grantee is the principal holding the grant.
	Grantee Subject `json:"grantee"`
	// Exp is when the grant stops applying, in RFC3339.
	Exp time.Time `json:"exp"`
}

// StandingWriteResult reports what a standing-grant write changed.
type StandingWriteResult struct {
	// GrantID names the written row.
	GrantID string `json:"grant_id"`
	// Action is the verb the grant now covers.
	Action string `json:"action"`
	// Changed is true when a row was written.
	Changed bool `json:"changed"`
}

// standingCreate writes a new standing grant through CreateStandingGrant.
func (d RPCDeps) standingCreate(ctx context.Context, params json.RawMessage) (any, error) {
	return d.standingWrite(ctx, params)
}

// standingChange rewrites an existing standing grant's terms. It runs the
// SAME guarded path as create: a change that skipped the deny-list guard
// would be a way to reach a denied class in two steps.
func (d RPCDeps) standingChange(ctx context.Context, params json.RawMessage) (any, error) {
	return d.standingWrite(ctx, params)
}

// standingWrite is the one guarded write both verbs run.
func (d RPCDeps) standingWrite(ctx context.Context, params json.RawMessage) (any, error) {
	var p StandingWriteParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	id, err := cascade.ParseID(p.GrantID)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindPermissionDenied, ErrDeniedClass,
			"policy: the standing grant names no well-formed grant id")
	}
	deps := StandingGrantDeps{Grants: d.Grants, DenyList: d.DenyList, Clock: d.Clock}
	grant := StandingGrant{
		GrantID:     id,
		ActionClass: p.ActionClass,
		Action:      p.Action,
		Capability:  p.Capability,
		Scope:       p.Scope,
		Grantee:     p.Grantee,
		Exp:         p.Exp,
	}
	if err := CreateStandingGrant(ctx, deps, grant); err != nil {
		return nil, standingRefusal(err)
	}
	return StandingWriteResult{GrantID: p.GrantID, Action: p.Action, Changed: true}, nil
}

// standingRefusal maps CreateStandingGrant's ErrDeniedClass onto the
// permission-denied kind. Any other error is returned unchanged: it
// already carries its own taxonomy kind.
func standingRefusal(err error) error {
	if errors.Is(err, ErrDeniedClass) {
		return cascade.Wrapf(cascade.KindPermissionDenied, err,
			"policy: this action class may not be granted standing")
	}
	return err
}

// StandingRevokeParams names the grant to revoke.
type StandingRevokeParams struct {
	// Grantee is the principal holding the grant.
	Grantee Subject `json:"grantee"`
	// Capability is the capability the grant is stored under.
	Capability string `json:"capability"`
}

// standingRevoke removes one standing grant and reports that it did.
func (d RPCDeps) standingRevoke(ctx context.Context, params json.RawMessage) (any, error) {
	var p StandingRevokeParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if d.Grants == nil {
		return nil, cascade.New(cascade.KindUnavailable, "policy: no grant store is wired in this process")
	}
	if err := d.Grants.Revoke(ctx, p.Grantee, p.Capability); err != nil {
		return nil, err
	}
	return StandingWriteResult{Action: p.Capability, Changed: true}, nil
}
