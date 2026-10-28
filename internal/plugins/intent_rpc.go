// Package plugins (intent_rpc.go): Purpose: the daemon's "plugin.intent"
// RPC handler, the daemon-side path `cascade plugin add <intent-ref>` takes.
// Inputs: params {"intent_name": string, "input": <json>} and an
// IntentDispatcher over the daemon's builtin registry.
// Outputs: {"result": <the plugin's DispatchIntent bytes, unchanged>}.
// Constraints: owner only. The verb itself is not elevated: it routes an
// intent and grants nothing. Anything the routed plugin installs passes its
// own elevation gate (the install flow's Elevator and installer). Unknown
// params fields and a non-JSON plugin result are refused.
// SPORT: internal/plugins plugin.intent RPC (ADD) — P1-PLG-07.
package plugins

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// IntentRPCMethod is the verb IntentRPCHandler serves.
const IntentRPCMethod = "plugin.intent"

var _ = rpc.DeclareAccess(IntentRPCMethod, rpc.AccessOwnerOnly)

// IntentRPCParams is the plugin.intent wire request, shared with the CLI so
// the two sides cannot drift.
type IntentRPCParams struct {
	IntentName string          `json:"intent_name"`
	Input      json.RawMessage `json:"input"`
}

// IntentRPCResult is the plugin.intent wire answer, shared with the CLI.
type IntentRPCResult struct {
	Result json.RawMessage `json:"result"`
}

// IntentRPCHandler builds the plugin.intent handler over d. A nil d refuses
// every call with KindUnavailable.
func IntentRPCHandler(d *IntentDispatcher) func(ctx context.Context, raw json.RawMessage) (any, error) {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		if d == nil {
			return nil, cascade.Newf(cascade.KindUnavailable, "%s: no intent dispatcher wired; nothing was dispatched", IntentRPCMethod)
		}
		var p IntentRPCParams
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p); err != nil {
			return nil, cascade.Wrapf(cascade.KindInvalidInput, err, "%s: decode params", IntentRPCMethod)
		}
		out, err := d.Dispatch(ctx, p.IntentName, p.Input)
		if err != nil {
			return nil, err
		}
		if !json.Valid(out) {
			return nil, cascade.Newf(cascade.KindInternal, "%s: intent %q returned a result that is not JSON", IntentRPCMethod, p.IntentName)
		}
		return IntentRPCResult{Result: out}, nil
	}
}
