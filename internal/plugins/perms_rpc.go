// Package plugins (perms_rpc.go): Purpose: the daemon's "perms.grant" and
// "perms.revoke" RPC handlers, the path `cascade plugin perms` takes while
// a daemon holds the plugin store.
// Inputs: the handler context (which must carry the elevation middleware's
// witness for exactly this call), PermsParams, the plugin store and an
// audit.EffectWriter.
// Outputs: {"name", "grants_before", "grants_after"} after one attested
// commit that writes Grants and (for a trusted process plugin) TrustGrants
// together with an intent+confirm effect pair.
// Constraints: owner-only; both verbs are always elevated (internal/rpc's
// elevation table): a call without a witness naming this verb and these
// exact params bytes is refused before the store is read; unknown params
// fields are refused. Each commit records a unique effect key; grant-set
// edits are idempotent.
// SPORT: internal/plugins perms.grant/perms.revoke RPC (P1-PLG-11).
package plugins

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
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
	ID         string `json:"id"`
	Capability string `json:"capability"`
}

// PermsResult reports the grant set before and after the change.
type PermsResult struct {
	Name   string   `json:"name"`
	Before []string `json:"grants_before"`
	After  []string `json:"grants_after"`
}

// PermsRPCHandler builds the handler for grant (true) or revoke (false).
// effects nil refuses: every external-state write in P1 records intent
// before and confirm after, and this handler is no exception.
// The legacy two-argument form lacks an effect writer and always refuses.
func PermsRPCHandler(store provider.Store, effectOrGrant any, direction ...bool) func(ctx context.Context, raw json.RawMessage) (any, error) {
	effects, _ := effectOrGrant.(audit.EffectWriter)
	grant, _ := effectOrGrant.(bool)
	if len(direction) == 1 {
		grant = direction[0]
	} else {
		effects = nil
	}
	method := PermsRevokeRPCMethod
	if grant {
		method = PermsGrantRPCMethod
	}
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		w, ok := rpc.VerifiedElevationFrom(ctx)
		bound, boundOK := installParamsFrom(ctx)
		if !ok || w.Method() != method || !boundOK || w.ParamsHash() != rpc.ParamsHash(bound) || !bytes.Equal(bound, raw) {
			return nil, cascade.Newf(cascade.KindElevationRequired,
				"%s: no verified elevation for this call; nothing changed", method)
		}
		if store == nil {
			return nil, cascade.Newf(cascade.KindUnavailable,
				"%s: no plugin store; nothing changed", method)
		}
		if effects == nil {
			return nil, cascade.Newf(cascade.KindUnavailable,
				"%s: no effect writer; nothing changed", method)
		}
		p, err := decodePermsParams(raw)
		if err != nil {
			return nil, err
		}
		return commitPermsChange(ctx, store, effects, p, grant, w.ParamsHash())
	}
}

// decodePermsParams validates the witnessed request before any store access.
func decodePermsParams(raw json.RawMessage) (PermsParams, error) {
	var p PermsParams
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, cascade.Wrapf(cascade.KindInvalidInput, err, "perms: decode params")
	}
	if !json.Valid(raw) {
		return p, cascade.New(cascade.KindInvalidInput, "perms: invalid params")
	}
	if strings.TrimSpace(p.Capability) == "" {
		return p, cascade.New(cascade.KindInvalidInput, "perms: capability must not be empty")
	}
	return p, nil
}

// permsCommitMu serializes permission read-modify-write commits, including
// their effect records, so concurrent grants and revokes cannot lose changes.
var permsCommitMu sync.Mutex

// commitPermsChange records intent before the single metadata commit.
func commitPermsChange(ctx context.Context, store provider.Store, effects audit.EffectWriter, p PermsParams, grant bool, paramsHash string) (any, error) {
	permsCommitMu.Lock()
	defer permsCommitMu.Unlock()
	before, ok, err := LoadMetadata(ctx, store, p.ID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, cascade.Newf(cascade.KindNotFound,
			"perms: plugin %q is not installed", p.ID)
	}
	nonce, err := permsNonce()
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("plugin.perms:%s:%s", p.ID, nonce)
	action := "plugin.perms.revoke"
	if grant {
		action = "plugin.perms.grant"
	}
	handle, err := effects.BeginEffect(ctx, audit.EffectRequest{
		Kind:       audit.KindExternalEffect,
		Actor:      "owner-cli",
		Action:     action,
		Key:        key,
		ParamsHash: paramsHash,
	})
	if err != nil {
		if handle.IntentSeq != 0 {
			_, _ = effects.FailEffect(context.WithoutCancel(ctx), handle, err.Error())
		}
		return nil, err
	}
	rec, err := applyPermsChange(ctx, store, before, p.Capability, grant)
	if err != nil {
		_, _ = effects.FailEffect(context.WithoutCancel(ctx), handle, err.Error())
		return nil, err
	}
	if _, err := effects.ConfirmEffect(context.WithoutCancel(ctx), handle, "grants updated"); err != nil {
		return nil, err
	}
	return PermsResult{Name: rec.Name, Before: before.Grants, After: rec.Grants}, nil
}

// applyPermsChange writes Grants and (for a trusted RuntimeProcess record)
// TrustGrants through applyGrantChange in one SaveMetadata call. It is the
// only permission-change writer of TrustGrants.
func applyPermsChange(ctx context.Context, store provider.Store, rec PluginMetadata, capability string, grant bool) (PluginMetadata, error) {
	next := rec
	next.Grants = applyGrantChange(rec.Grants, capability, grant)
	if rec.RuntimeMode == plugin.RuntimeProcess && rec.TrustTier == TrustTrusted {
		next.TrustGrants = applyGrantChange(rec.TrustGrants, capability, grant)
	}
	if err := SaveMetadata(ctx, store, next); err != nil {
		return PluginMetadata{}, err
	}
	return next, nil
}

// permsNonce returns 8 bytes of crypto/rand hex for the effect key. A
// failure here is a platform fault, not a caller error.
func permsNonce() (string, error) {
	var b [8]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		return "", cascade.Wrapf(cascade.KindUnavailable, err,
			"perms: generate effect nonce")
	}
	return hex.EncodeToString(b[:]), nil
}
