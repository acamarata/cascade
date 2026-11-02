// Package plugins (update_rpc.go): Purpose: the daemon's "plugin.update" RPC handler, the only path that
// commits an update UpdatePlugin reported as elevation-required.
// Inputs: the handler context (which must carry the elevation middleware's
// witness for exactly this call) and UpdateParams.
// Outputs: {"message", "grants"} after one commit through commitUpdate.
// Constraints: owner-only; refuses KindElevationRequired unless the witness
// names plugin.update and the exact params bytes; re-runs every UpdatePlugin
// guard (remote refusal, id match, checksum, installed record, process
// handshake) against the stored record; a process-tier candidate is trusted
// only through grantProcessTrust; every refusal writes nothing.
// SPORT: internal/plugins plugin.update RPC.
package plugins

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/pkg/provider"
)

// UpdateRPCMethod is the elevated verb this handler serves.
const UpdateRPCMethod = "plugin.update"

var _ = rpc.DeclareAccess(UpdateRPCMethod, rpc.AccessOwnerOnly)

// UpdateParams is the "plugin.update" wire request: the candidate manifest
// bytes, the id the caller means to update, and an optional pinned checksum
// the manifest bytes must match. Reverify with IndexBytes (the signed
// registry index) asks to re-pin the installed version's executable digest
// instead of updating (reverify.go).
type UpdateParams struct {
	ID            string `json:"id"`
	Checksum      string `json:"checksum"`
	ManifestBytes []byte `json:"manifest_bytes"`
	Reverify      bool   `json:"reverify,omitempty"`
	IndexBytes    []byte `json:"index_bytes,omitempty"`
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

// errUpdateNoStore refuses a plugin.update (or reverify) wired without a
// plugin store, before any record is read.
var errUpdateNoStore = cascade.New(cascade.KindUnavailable, "plugin.update: no plugin store; update not applied")

// UpdateRPCHandler builds the "plugin.update" handler over store.
// handshaker supplies the process handshake for each call; a nil func (or a
// nil result) leaves commitUpdate's process-tier guard to refuse. A reverify
// call is refused here (no registry verifier); WithReverify adds one.
func UpdateRPCHandler(store provider.Store, handshaker func() ProcessHandshaker) func(ctx context.Context, raw json.RawMessage) (any, error) {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		w, ok := rpc.VerifiedElevationFrom(ctx)
		if !ok || w.Method() != UpdateRPCMethod || w.ParamsHash() != rpc.ParamsHash(raw) {
			return nil, errUpdateNotAttested
		}
		if store == nil {
			return nil, errUpdateNoStore
		}
		var p UpdateParams
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p); err != nil {
			return nil, cascade.Wrap(cascade.KindInvalidInput, err, "plugin.update: decode params")
		}
		if p.Reverify || len(p.IndexBytes) > 0 {
			return reverifyResult(ctx, store, nil, p)
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

// WithReverify serves reverify calls of the "plugin.update" handler next
// with verify as the index verifier; every other call goes to next. A
// reverify call needs the same witness as an update.
func WithReverify(next func(ctx context.Context, raw json.RawMessage) (any, error), store provider.Store, verify IndexVerifier) func(ctx context.Context, raw json.RawMessage) (any, error) {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var probe struct {
			Reverify   bool            `json:"reverify"`
			IndexBytes json.RawMessage `json:"index_bytes"`
		}
		if json.Unmarshal(raw, &probe) != nil || (!probe.Reverify && len(probe.IndexBytes) == 0) {
			return next(ctx, raw)
		}
		if w, ok := rpc.VerifiedElevationFrom(ctx); !ok || w.Method() != UpdateRPCMethod || w.ParamsHash() != rpc.ParamsHash(raw) {
			return nil, errUpdateNotAttested
		}
		if store == nil {
			return nil, errUpdateNoStore
		}
		var p UpdateParams
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p); err != nil {
			return nil, cascade.Wrap(cascade.KindInvalidInput, err, "plugin.update: decode params")
		}
		return reverifyResult(ctx, store, verify, p)
	}
}

// reverifyResult runs reverifyTrust and shapes its result.
func reverifyResult(ctx context.Context, store provider.Store, verify IndexVerifier, p UpdateParams) (any, error) {
	if !p.Reverify {
		return nil, cascade.New(cascade.KindInvalidInput, "plugin.update: index_bytes is only accepted with reverify")
	}
	rec, err := reverifyTrust(ctx, store, verify, p)
	if err != nil {
		return nil, err
	}
	return updateRPCResult{Message: "re-verified " + rec.Name + " v" + rec.InstalledVersion, Grants: rec.TrustGrants}, nil
}

// applyAttestedUpdate re-runs UpdatePlugin's guards for an attested call and
// commits through commitUpdate. Elevation is already proven, so no
// elevation decision is made here; a handshake failure is an error, since
// the caller asked for a commit and none happened. Order: parse, id match,
// then the trust grant for a process-tier candidate (so its refusal is the
// policy-denied one naming the failed condition), then the checksum, the
// installed record, the handshaker and the handshake.
func applyAttestedUpdate(ctx context.Context, store provider.Store, hs ProcessHandshaker, p UpdateParams) (UpdateResult, error) {
	m, err := parseUpdatableManifest(p.ManifestBytes)
	if err != nil {
		return UpdateResult{}, err
	}
	if m.ID != p.ID {
		return UpdateResult{}, cascade.Newf(cascade.KindInvalidInput,
			"plugin.update: manifest id %q does not match requested id %q", m.ID, p.ID)
	}
	trust, err := attestedUpdateTrust(ctx, hs, m, p.Checksum)
	if err != nil {
		return UpdateResult{}, err
	}
	if err := verifyUpdateChecksum(p.Checksum, p.ManifestBytes); err != nil {
		return UpdateResult{}, err
	}
	existing, err := loadInstalled(ctx, store, m.ID)
	if err != nil {
		return UpdateResult{}, err
	}
	if trust.Digest != "" {
		ctx = withGrantedDigest(ctx, m.ID, trust.Digest) // the handshake runs only this executable
	}
	res, err := commitUpdate(ctx, store, hs, m, existing, trust, p.Checksum, "")
	if err != nil {
		return UpdateResult{}, err
	}
	if res.Outcome != UpdateOutcomeUpdated {
		return UpdateResult{}, cascade.Wrapf(cascade.KindUnavailable, res.RollbackErr,
			"plugin.update: %s handshake failed; update not applied", m.ID)
	}
	return res, nil
}

// attestedUpdateTrust is the trust an attested update records. A process-tier
// candidate is trusted only by grantProcessTrust for plugin.update, decided
// before any handshake could launch it; a granted candidate with no
// handshaker is then refused (KindUnavailable) as commitUpdate would. Any
// other runtime records no trust.
func attestedUpdateTrust(ctx context.Context, hs ProcessHandshaker, m plugin.Manifest, checksum string) (processTrust, error) {
	if m.Runtime != plugin.RuntimeProcess {
		return processTrust{}, nil
	}
	tier, digest, err := grantProcessTrust(ctx, UpdateRPCMethod, m, checksum)
	if err != nil {
		return processTrust{}, err
	}
	if hs == nil {
		return processTrust{}, errProcessHandshakeUnconfirmed
	}
	return processTrust{Tier: tier, Digest: digest, Bound: append([]string(nil), m.Requires...)}, nil
}
