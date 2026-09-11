package supervision

// Purpose (this file): the fleet.attention.list/get/ack JSON-RPC 2.0
// methods (ticket task 4): RegisterHandlers binds them into an
// *rpc.Registry, mirroring internal/fleet/capacity/rpc.go's and
// internal/fleet/sessions/rpc.go's exact pattern.
//
// CONTRACT DEVIATION (files_scope path, recorded, not papered over —
// R-16.79). The ticket's files_scope names internal/rpc/fleet_attention.go
// for this handler code. Placing it there produces a real import cycle:
// internal/rpc -> internal/fleet/supervision (for the domain types) ->
// internal/fleet/sessions (subscribe.go, for IsAttentionState/
// SessionRecord) -> internal/rpc (sessions/rpc.go already imports
// internal/rpc for Registry/HandlerFunc) -> back to internal/rpc. `go
// build` confirmed this cycle verbatim the first time this file was
// tried at that path. Every other domain in this tree that registers RPC
// methods (internal/fleet/sessions/rpc.go, internal/fleet/capacity/
// rpc.go) already avoids exactly this shape by keeping its handler code
// in ITS OWN package and importing internal/rpc's generic Registry/
// HandlerFunc types (never the reverse) — this file follows that same,
// already-established precedent instead of the wrong path the ticket
// named. internal/rpc itself needs no change (Register is method-name-
// agnostic, matching capacity/rpc.go's own identical CONTRACT DEVIATION
// note for internal/rpc/registry.go).
//
// The checks list's `./internal/rpc/...` entries (the TestFleetAttentionRPC
// run and the FuzzAttentionParams fuzz target) are run against
// ./internal/fleet/supervision/... instead — the real location of this
// code — and reported as such in the journal, rather than silently
// reporting a check against a package that holds none of this code.
//
// Inputs: fleet.attention.list/get/ack's JSON params (unknown fields
// rejected).
// Outputs: the wire result shapes, or a pkg/cascade taxonomy error.
//
// CONTRACT DEVIATION (caller-scope attribution, recorded, not papered
// over). R-16.5 says list/get default to "the calling session's own
// scope chain." No mechanism in this tree currently attributes a
// caller's scope to an inbound JSON-RPC call (grep for a
// scope.SessionScope-from-context accessor across internal/rpc and
// internal/context/scope found none — every existing scope resolution
// call site takes an explicit ResolveInput, never reads ambient request
// context). Until that lands, own_scope is an explicit request field the
// caller states, exactly as every other explicit-input scope call in
// this tree already requires. This is recorded as a genuine gap, not
// papered over with an invented "current session" default.
//
// SPORT: fleet.supervision.RegisterHandlers/ADDED (P1-E18-W4-S39-T1).

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// attnListParams is fleet.attention.list's wire request shape.
type attnListParams struct {
	All          bool       `json:"all,omitempty"`
	OwnScope     *ScopeRef  `json:"own_scope,omitempty"`
	Chain        []ScopeRef `json:"chain,omitempty"`
	Kind         *Kind      `json:"kind,omitempty"`
	IncludeAcked bool       `json:"include_acked,omitempty"`
}

type attnListResult struct {
	Items []AttentionItem `json:"items"`
}

type attnGetParams struct {
	ID       string     `json:"id"`
	OwnScope *ScopeRef  `json:"own_scope,omitempty"`
	Chain    []ScopeRef `json:"chain,omitempty"`
}

type attnGetResult struct {
	Item AttentionItem `json:"item"`
}

type attnAckParams struct {
	ID string `json:"id"`
}

type attnAckResult struct {
	Item AttentionItem `json:"item"`
}

// errAttnUnknownField mirrors sessions.ErrUnknownFilterField for this
// method family.
var errAttnUnknownField = cascade.New(cascade.KindInvalidInput, "supervision: fleet.attention: unknown request field")

// decodeAttnParams decodes raw into dst, rejecting unknown fields — the
// wire shape IS the allow-list, matching internal/fleet/sessions/rpc.go's
// decodeListParams precedent.
func decodeAttnParams(raw json.RawMessage, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return cascade.Wrapf(cascade.KindInvalidInput, errAttnUnknownField, "supervision: decoding fleet.attention params: %v", err)
	}
	return nil
}

// RegisterHandlers binds the three fleet.attention methods on reg.
// graphStore may be nil (visibility then collapses to own_scope only,
// per ResolveVisibleScopes's own nil-store contract).
func RegisterHandlers(reg *rpc.Registry, store *Store, graphStore *scope.GraphStore) {
	reg.Register(MethodList, fleetAttentionListHandler(store, graphStore))
	reg.Register(MethodGet, fleetAttentionGetHandler(store, graphStore))
	reg.Register(MethodAck, fleetAttentionAckHandler(store))
}

func fleetAttentionListHandler(store *Store, graphStore *scope.GraphStore) rpc.HandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p attnListParams
		if len(raw) > 0 {
			if err := decodeAttnParams(raw, &p); err != nil {
				return nil, err
			}
		}
		if p.OwnScope == nil {
			return nil, cascade.New(cascade.KindInvalidInput, "supervision: fleet.attention.list: own_scope is required")
		}
		scopes, err := resolveScopes(ctx, graphStore, p.All, p.Chain, *p.OwnScope)
		if err != nil {
			return nil, err
		}
		items, err := store.ListInScopes(ctx, scopes, Filter{KindFilter: p.Kind, IncludeAcked: p.IncludeAcked})
		if err != nil {
			return nil, err
		}
		return attnListResult{Items: items}, nil
	}
}

func fleetAttentionGetHandler(store *Store, graphStore *scope.GraphStore) rpc.HandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p attnGetParams
		if err := decodeAttnParams(raw, &p); err != nil {
			return nil, err
		}
		if p.ID == "" {
			return nil, cascade.New(cascade.KindInvalidInput, "supervision: fleet.attention.get: id is required")
		}
		item, err := store.Get(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		if p.OwnScope != nil {
			visible, err := resolveScopes(ctx, graphStore, true, p.Chain, *p.OwnScope)
			if err != nil {
				return nil, err
			}
			if !scopeVisible(item.ScopeRef, visible) {
				return nil, ErrNotFound
			}
		}
		return attnGetResult{Item: item}, nil
	}
}

func fleetAttentionAckHandler(store *Store) rpc.HandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p attnAckParams
		if err := decodeAttnParams(raw, &p); err != nil {
			return nil, err
		}
		if p.ID == "" {
			return nil, cascade.New(cascade.KindInvalidInput, "supervision: fleet.attention.ack: id is required")
		}
		item, err := store.Ack(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		return attnAckResult{Item: item}, nil
	}
}

// resolveScopes wraps ResolveVisibleScopes: all=false (the default view)
// collapses to ownScope alone without ever touching the traversal table.
func resolveScopes(ctx context.Context, graphStore *scope.GraphStore, all bool, chain []ScopeRef, ownScope ScopeRef) ([]ScopeRef, error) {
	if !all {
		return []ScopeRef{ownScope}, nil
	}
	return ResolveVisibleScopes(ctx, graphStore, chain, ownScope)
}

// scopeVisible reports whether ref appears in visible.
func scopeVisible(ref ScopeRef, visible []ScopeRef) bool {
	for _, v := range visible {
		if v == ref {
			return true
		}
	}
	return false
}
