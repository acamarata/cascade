// Package plugins (update_rpc.go): Purpose: the daemon's "plugin.update" RPC handler, the only path that
// commits an update UpdatePlugin reported as elevation-required.
// Inputs: the handler context (which must carry the elevation middleware's
// witness for exactly this call) and UpdateParams.
// Outputs: {"message", "grants"} after one commit through commitUpdate.
// Constraints: owner-only; refuses KindElevationRequired unless the witness
// names plugin.update and the exact params bytes; re-runs every UpdatePlugin
// guard (remote refusal, id match, checksum, installed record, process
// handshake) against the stored record; every refusal writes nothing.
// SPORT: internal/plugins plugin.update RPC.
package plugins

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// UpdateRPCMethod is the elevated verb this handler serves.
const UpdateRPCMethod = "plugin.update"

var _ = rpc.DeclareAccess(UpdateRPCMethod, rpc.AccessOwnerOnly)

// UpdateParams is the "plugin.update" wire request: the candidate manifest
// bytes, the id the caller means to update, and an optional pinned checksum
// the manifest bytes must match.
type UpdateParams struct {
	ID            string `json:"id"`
	Checksum      string `json:"checksum"`
	ManifestBytes []byte `json:"manifest_bytes"`
}

// updateRPCResult mirrors plugin.add's result shape.
type updateRPCResult struct {
	Message string   `json:"message"`
	Grants  []string `json:"grants,omitempty"`
}

// errUpdateNotAttested refuses a plugin.update that reached the handler
// without a witness for this verb and these exact params.
var errUpdateNotAttested = cascade.New(cascade.KindElevationRequired,
	"plugin.update: no verified elevation for this call; update not applied")

// UpdateRPCHandler builds the "plugin.update" handler over store.
// handshaker supplies the process handshake for each call; a nil func (or a
// nil result) leaves commitUpdate's process-tier guard to refuse.
func UpdateRPCHandler(store provider.Store, handshaker func() ProcessHandshaker) func(ctx context.Context, raw json.RawMessage) (any, error) {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		w, ok := rpc.VerifiedElevationFrom(ctx)
		if !ok || w.Method() != UpdateRPCMethod || w.ParamsHash() != rpc.ParamsHash(raw) {
			return nil, errUpdateNotAttested
		}
		if store == nil {
			return nil, cascade.New(cascade.KindUnavailable, "plugin.update: no plugin store; update not applied")
		}
		var p UpdateParams
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p); err != nil {
			return nil, cascade.Wrap(cascade.KindInvalidInput, err, "plugin.update: decode params")
		}
		var hs ProcessHandshaker
		if handshaker != nil {
			hs = handshaker()
		}
		res, err := applyAttestedUpdate(ctx, store, hs, p)
		if err != nil {
			return nil, err
		}
		return updateRPCResult{Message: "updated " + res.Metadata.Name + " to v" + res.Metadata.InstalledVersion, Grants: res.Metadata.Grants}, nil
	}
}

// applyAttestedUpdate re-runs UpdatePlugin's guards for an attested call and
// commits through commitUpdate. Elevation is already proven, so no
// elevation decision is made here; a handshake failure is an error, since
// the caller asked for a commit and none happened.
func applyAttestedUpdate(ctx context.Context, store provider.Store, hs ProcessHandshaker, p UpdateParams) (UpdateResult, error) {
	m, err := parseUpdatableManifest(p.ManifestBytes)
	if err != nil {
		return UpdateResult{}, err
	}
	if m.ID != p.ID {
		return UpdateResult{}, cascade.Newf(cascade.KindInvalidInput,
			"plugin.update: manifest id %q does not match requested id %q", m.ID, p.ID)
	}
	if err := verifyUpdateChecksum(p.Checksum, p.ManifestBytes); err != nil {
		return UpdateResult{}, err
	}
	existing, err := loadInstalled(ctx, store, m.ID)
	if err != nil {
		return UpdateResult{}, err
	}
	res, err := commitUpdate(ctx, store, hs, m, existing, p.Checksum, "")
	if err != nil {
		return UpdateResult{}, err
	}
	if res.Outcome != UpdateOutcomeUpdated {
		return UpdateResult{}, cascade.Wrapf(cascade.KindUnavailable, res.RollbackErr,
			"plugin.update: %s handshake failed; update not applied", m.ID)
	}
	return res, nil
}
