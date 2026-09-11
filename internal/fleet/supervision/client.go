package supervision

// Purpose (this file): the fleet.attention.list/get/ack Go client SDK
// (D/S-07.T3), mirroring internal/fleet/journal/rpc_client.go's exact
// pattern, for cmd/cascade/fleet_attention.go. Also NewSystemIDGenerator,
// the production IDGenerator (crypto/rand, never math/rand per the
// repo-wide gate).
//
// CONTRACT DEVIATION (file location, recorded, not papered over). This
// ticket's files_scope names no client-SDK file explicitly, but every
// landed precedent for a domain's own RPC client (journal, sessions)
// places it in the domain package itself, never in internal/rpc or
// cmd/cascade — matching files_scope's own INTENT (D/S-07.T3 "Go client
// SDK", per R-16.79: files_scope is binding as intent, not as an
// exhaustive file list) rather than its literal silence.
//
// SPORT: fleet.supervision.Client/ADDED (P1-E18-W4-S39-T1).

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"github.com/acamarata/cascade/pkg/cascade"
)

// The three fleet.attention JSON-RPC 2.0 method names (ticket task 4).
// Declared here (not in internal/rpc) so both this package's Client and
// internal/rpc/fleet_attention.go's handlers reference the SAME constant
// with no import cycle (internal/rpc imports this package for the
// handler side; this package must not import internal/rpc back).
const (
	MethodList = "fleet.attention.list"
	MethodGet  = "fleet.attention.get"
	MethodAck  = "fleet.attention.ack"
)

// RPCCaller is the minimal one-method interface the frozen D/S-07.T3
// client (internal/client.Client) already satisfies, declared locally so
// this package never imports internal/client directly — mirroring
// internal/fleet/journal.RPCCaller's identical seam.
type RPCCaller interface {
	Do(ctx context.Context, method string, params, out any) error
}

// Client wraps an RPCCaller with the typed fleet.attention.list/get/ack
// doors.
type Client struct {
	caller RPCCaller
}

// NewClient builds a Client dialing every call through c.
func NewClient(c RPCCaller) *Client {
	return &Client{caller: c}
}

// List calls fleet.attention.list through c.caller. ownScope and chain
// are this call's explicit scope attribution — see rpc.go's CONTRACT
// DEVIATION note on caller-scope attribution.
func (c *Client) List(ctx context.Context, ownScope ScopeRef, chain []ScopeRef, all bool, kindFilter *Kind, includeAcked bool) ([]AttentionItem, error) {
	req := attnListParams{All: all, OwnScope: &ownScope, Chain: chain, Kind: kindFilter, IncludeAcked: includeAcked}
	var resp attnListResult
	if err := c.caller.Do(ctx, MethodList, req, &resp); err != nil {
		return nil, wrapClientErr(err, "fleet.attention.list")
	}
	return resp.Items, nil
}

// Get calls fleet.attention.get through c.caller.
func (c *Client) Get(ctx context.Context, id string, ownScope ScopeRef, chain []ScopeRef) (AttentionItem, error) {
	req := attnGetParams{ID: id, OwnScope: &ownScope, Chain: chain}
	var resp attnGetResult
	if err := c.caller.Do(ctx, MethodGet, req, &resp); err != nil {
		return AttentionItem{}, wrapClientErr(err, "fleet.attention.get")
	}
	return resp.Item, nil
}

// Ack calls fleet.attention.ack through c.caller.
func (c *Client) Ack(ctx context.Context, id string) (AttentionItem, error) {
	var resp attnAckResult
	if err := c.caller.Do(ctx, MethodAck, attnAckParams{ID: id}, &resp); err != nil {
		return AttentionItem{}, wrapClientErr(err, "fleet.attention.ack")
	}
	return resp.Item, nil
}

// wrapClientErr preserves a taxonomy error's Kind and wraps a non-
// taxonomy transport error as KindInternal, mirroring
// internal/fleet/journal.Client's identical helper.
func wrapClientErr(err error, method string) error {
	if _, ok := cascade.KindOf(err); ok {
		return err
	}
	return cascade.Wrapf(cascade.KindInternal, err, "supervision: %s failed", method)
}

// NewSystemIDGenerator returns the production IDGenerator: a random
// 128-bit hex string from crypto/rand, never math/rand (the repo-wide
// egress/randomness gate forbids math/rand outside tests — see
// internal/fleet/sessions/sse.go's newConnCursorName for the identical
// pattern this mirrors).
func NewSystemIDGenerator() IDGenerator {
	return func() string {
		var b [16]byte
		// nolint:forbidigo // crypto/rand.Read, not math/rand.Read.
		_, _ = rand.Read(b[:])
		return hex.EncodeToString(b[:])
	}
}
