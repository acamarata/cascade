package journal

// Purpose: the fleet.journal_show/replay Go client SDK wrapper, split out
//
//	of rpc.go purely to stay under the repo-wide 300-line-per-file cap
//	(R-14.117) once the handler-side doc comments and CONTRACT DEVIATION
//	notes were added — see rpc.go's own doc comment for the read-path
//	contract this Client is the CLI-facing half of.
//
// Inputs: entity_id plus an optional cursor (Show's after, Replay's
//
//	from) and, for Show, an optional limit — the same shape rpc.go's
//	showParams/replayParams declare.
//
// Outputs: the entity's entries, or a pkg/cascade taxonomy error.
// Constraints: never writes; both methods are pure RPC calls over the
//
//	injected RPCCaller.
//
// SPORT: internal.fleet.journal.Client/ADDED (P1-E13-W3-S27-T4).

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// RPCCaller is the minimal one-method interface the frozen D/S-07.T3
// client (internal/client.Client) already satisfies, declared locally so
// this package never imports internal/client directly — mirroring
// internal/fleet/sessions/rpc.go's identical seam.
type RPCCaller interface {
	Do(ctx context.Context, method string, params, out any) error
}

// Client wraps an RPCCaller with the typed fleet.journal_show/replay
// doors, for cmd/cascade/fleet_journal.go.
type Client struct {
	caller RPCCaller
}

// NewClient builds a Client dialing every call through c.
func NewClient(c RPCCaller) *Client {
	return &Client{caller: c}
}

// Show calls fleet.journal_show through c.caller. limit <= 0 sends no
// limit field (server default: unlimited, subject to maxShowLimit's
// clamp only when the caller does ask for more than it allows).
func (c *Client) Show(ctx context.Context, entityID string, after *uint64, limit int) ([]Entry, error) {
	p := showParams{EntityID: entityID, AfterSeq: after}
	if limit > 0 {
		p.Limit = &limit
	}
	var resp showResult
	if err := c.caller.Do(ctx, MethodShow, p, &resp); err != nil {
		if _, ok := cascade.KindOf(err); ok {
			return nil, err
		}
		return nil, cascade.Wrap(cascade.KindInternal, err, "journal: fleet.journal_show failed")
	}
	return resp.Entries, nil
}

// Replay calls fleet.journal_replay through c.caller.
func (c *Client) Replay(ctx context.Context, entityID string, from *uint64) ([]Entry, error) {
	p := replayParams{EntityID: entityID, FromSeq: from}
	var resp replayResult
	if err := c.caller.Do(ctx, MethodReplay, p, &resp); err != nil {
		if _, ok := cascade.KindOf(err); ok {
			return nil, err
		}
		return nil, cascade.Wrap(cascade.KindInternal, err, "journal: fleet.journal_replay failed")
	}
	return resp.Entries, nil
}
