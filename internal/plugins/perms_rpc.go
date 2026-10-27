// Package plugins (perms_rpc.go): Purpose: the daemon's "perms.grant" and "perms.revoke" RPC handlers, the
// path `cascade plugin perms` takes while a daemon holds the plugin store.
// Inputs: the handler context (which must carry the elevation middleware's
// witness for exactly this call) and PermsParams.
// Outputs: {"name", "grants_before", "grants_after"} after one ChangePerms.
// Constraints: owner-only; both verbs are always elevated (internal/rpc's
// elevation table): a call without a witness naming this verb and these
// exact params bytes is refused before the store is read; unknown params
// fields are refused.
// SPORT: internal/plugins perms.grant/perms.revoke RPC (P1-SEC-08).
package plugins

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// PermsGrantRPCMethod and PermsRevokeRPCMethod are the elevated verbs these
// handlers serve.
const (
	PermsGrantRPCMethod  = "perms.grant"
	PermsRevokeRPCMethod = "perms.revoke"
)

var (
	_ = rpc.DeclareAccess(PermsGrantRPCMethod, rpc.AccessOwnerOnly)
	_ = rpc.DeclareAccess(PermsRevokeRPCMethod, rpc.AccessOwnerOnly)
)

// PermsParams is the perms.grant / perms.revoke wire request.
type PermsParams struct {
	Name       string `json:"name"`
	Capability string `json:"capability"`
}

// PermsResult reports the grant set before and after the change.
type PermsResult struct {
	Name   string   `json:"name"`
	Before []string `json:"grants_before"`
	After  []string `json:"grants_after"`
}

// PermsRPCHandler builds the handler for grant (true) or revoke (false).
func PermsRPCHandler(store provider.Store, grant bool) func(ctx context.Context, raw json.RawMessage) (any, error) {
	method := PermsRevokeRPCMethod
	if grant {
		method = PermsGrantRPCMethod
	}
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		w, ok := rpc.VerifiedElevationFrom(ctx)
		if !ok || w.Method() != method || w.ParamsHash() != rpc.ParamsHash(raw) {
			return nil, cascade.Newf(cascade.KindElevationRequired, "%s: no verified elevation for this call; nothing changed", method)
		}
		if store == nil {
			return nil, cascade.Newf(cascade.KindUnavailable, "%s: no plugin store; nothing changed", method)
		}
		var p PermsParams
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p); err != nil {
			return nil, cascade.Wrapf(cascade.KindInvalidInput, err, "%s: decode params", method)
		}
		before, _, err := LoadMetadata(ctx, store, p.Name)
		if err != nil {
			return nil, err
		}
		rec, err := ChangePerms(ctx, store, p.Name, p.Capability, grant, true, false)
		if err != nil {
			return nil, err
		}
		return PermsResult{Name: rec.Name, Before: before.Grants, After: rec.Grants}, nil
	}
}
