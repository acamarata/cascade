package rpc

// Purpose: registers conductor.expand on the D/S-06.T3 dispatcher -- the
// R-21.68 AQ-owned row of the AP/S-82.T1 conductor.* manifest (R-21.39's
// "(AQ)" annotation names exactly this method). Every other conductor.*
// method is AP/S-82.T1's; this file owns only this one row.
//
// Inputs: {claim_id, page_token} decoded from the request's raw params.
// Outputs: the evidence.Packet unchanged, or a typed error mapped
// through Registry's own errorObjectFrom (evidence's *cascade.Error
// sentinels carry their Kind straight through).
// Constraints: RegisterConductorExpand refuses a second registration of
// "conductor.expand" on the same Registry with a typed conflict error
// (Registry.Register's own doc comment: a bare second Register call is a
// silent overwrite, which this ticket's own duplicate-registration-fails
// acceptance criterion requires this file to refuse instead).
//
// SPORT: rpc/conductor-expand (ADD), R-21.68.

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/evidence"
	"github.com/acamarata/cascade/pkg/cascade"
)

// MethodConductorExpand is the JSON-RPC method name this file registers.
const MethodConductorExpand = "conductor.expand"

// expandParams is the {claim_id, page_token} wire shape conductor.expand
// decodes.
type expandParams struct {
	ClaimID   string `json:"claim_id"`
	PageToken string `json:"page_token"`
}

// RegisterConductorExpand binds conductor.expand to registry, decoding
// {claim_id, page_token} and returning fetcher.Expand's packet unchanged.
// Returns a typed conflict error if conductor.expand is already
// registered on registry -- see this file's doc comment.
func RegisterConductorExpand(registry *Registry, fetcher *evidence.Fetcher) error {
	if registry.Registered(MethodConductorExpand) {
		return cascade.Newf(cascade.KindConflict, "rpc: %s is already registered", MethodConductorExpand)
	}
	registry.Register(MethodConductorExpand, func(ctx context.Context, params json.RawMessage) (any, error) {
		var p expandParams
		if len(params) > 0 {
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, cascade.Wrap(cascade.KindInvalidInput, err, "rpc: conductor.expand: decode params")
			}
		}
		if p.ClaimID == "" {
			return nil, cascade.New(cascade.KindInvalidInput, "rpc: conductor.expand: claim_id is required")
		}
		return fetcher.Expand(ctx, p.ClaimID, p.PageToken)
	})
	return nil
}
