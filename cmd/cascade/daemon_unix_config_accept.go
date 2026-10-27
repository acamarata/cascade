//go:build !windows

// Purpose: the config.accept_loosening handler: the one elevated verb that
// accepts a config file looser than the most-restrictive defaults. It binds
// the acceptance to the exact file bytes, records the signed baseline, and
// lets exactly one hot reload of those guarded values through.
// Inputs: params {"config_sha256": "<64 lowercase hex>"} and the verified
// elevation witness the middleware attached (never a request field).
// Outputs: {"sections_hash", "reload": {"accepted", "restart_required_keys",
// "elevation_ref"}}.
// Constraints: order is witness, params, one read of config.toml with
// Load's permission and ownership checks, its digest compared to the param
// before anything is parsed, those exact bytes parsed without and with the
// daemon's environment (they must declare the same guarded values: no env
// override), Accept, then arm the slot and reload. The file is never
// reopened between the digest and the parse. A failed step writes and arms
// nothing.
// SPORT: cmd/cascade config.accept_loosening (P1-SEC-04).
package main

import (
	"context"
	"encoding/json"
	"slices"
	"sort"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// acceptLooseningResult is the handler's result.
type acceptLooseningResult struct {
	SectionsHash string              `json:"sections_hash"`
	Reload       acceptLooseningLoad `json:"reload"`
}

type acceptLooseningLoad struct {
	Accepted            bool     `json:"accepted"`
	RestartRequiredKeys []string `json:"restart_required_keys"`
	ElevationRef        string   `json:"elevation_ref"`
}

// acceptLoosening is the config.accept_loosening handler.
func (b *configBaseline) acceptLoosening(ctx context.Context, raw json.RawMessage) (any, error) {
	witness, ok := rpc.VerifiedElevationFrom(ctx)
	if !ok || witness.Method() != runtime.AcceptLooseningMethod || witness.ParamsHash() != rpc.ParamsHash(raw) {
		return nil, cascade.New(cascade.KindElevationRequired, "config.accept_loosening needs a verified elevation for this call")
	}
	attestation, ok := witness.RecordedAttestation()
	if !ok {
		return nil, cascade.New(cascade.KindElevationRequired, "config.accept_loosening needs the verified attestation")
	}
	want, ok := runtime.DecodeAcceptParams(raw)
	if !ok {
		return nil, cascade.New(cascade.KindInvalidInput, `params must be exactly {"config_sha256": "<64 lowercase hex>"}`)
	}
	file, err := runtime.ReadConfigFile(b.configPath)
	if err != nil {
		if _, typed := cascade.KindOf(err); typed {
			return nil, err
		}
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "read config.toml")
	}
	if !file.Found {
		return nil, cascade.New(cascade.KindInvalidInput, "there is no config.toml to accept")
	}
	if file.Digest() != want {
		return nil, cascade.New(cascade.KindConflict, "config.toml on disk does not match config_sha256")
	}
	signed, err := file.Parse(ctx, envFreeLoadOptions())
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "config.toml does not load")
	}
	withEnv, err := file.Parse(ctx, b.loadOpts)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "config.toml does not load")
	}
	onDisk := runtime.EffectiveConfigOf(signed)
	if runtime.ComputeSectionsHash(runtime.EffectiveConfigOf(withEnv)) != runtime.ComputeSectionsHash(onDisk) {
		return nil, cascade.New(cascade.KindConflict, "an environment override changes a guarded value; unset it before accepting")
	}
	proof := runtime.BaselineProof{Method: runtime.AcceptLooseningMethod, RawParams: append([]byte(nil), raw...), Attestation: attestation}
	if err := b.Checker.Accept(ctx, onDisk, proof); err != nil {
		return nil, err
	}
	hash := runtime.ComputeSectionsHash(onDisk)
	tok := b.slot.arm(hash, witness.ParamsHash())
	return acceptLooseningResult{SectionsHash: hash, Reload: b.reload(ctx, tok, onDisk)}, nil
}

// reload runs one hot reload carrying tok. The reload updates the daemon's
// config view only (applyLive pushes [logging]); the consumers of guarded
// values (policy, sync) read them at start. So every guarded key accepted
// loosens relative to the values this process booted with is reported in
// restart_required_keys, on every acceptance until the next start.
func (b *configBaseline) reload(ctx context.Context, tok *slotToken, accepted runtime.EffectiveConfig) acceptLooseningLoad {
	var out runtime.ReloadOutcome
	if b.reloader != nil {
		out = b.reloader.Reload(withSlotToken(ctx, tok))
	}
	keys := append(append([]string{}, out.RestartKeys...), b.Checker.LoosenedSinceBoot(accepted)...)
	sort.Strings(keys)
	return acceptLooseningLoad{Accepted: out.Accepted, RestartRequiredKeys: slices.Compact(keys), ElevationRef: out.ElevationRef}
}

// envFreeLoadOptions loads a config with no environment: the guarded
// values a signed file declares by its bytes alone.
func envFreeLoadOptions() runtime.LoadOptions {
	return runtime.LoadOptions{Getenv: func(string) string { return "" }, Environ: func() []string { return nil }}
}

// productionBaselineVerifier verifies a proof against the trust record
// bound to the selected custody under dataDir, resolved at each call. A
// custody that cannot elevate, or an unbound one, refuses.
func productionBaselineVerifier(dataDir string, clock runtime.Clock) runtime.BaselineVerifier {
	return func(p runtime.BaselineProof) error {
		custody := daemonElevationSelector(dataDir).Select()
		if !custody.Tier().SatisfiesElevation() {
			return cascade.New(cascade.KindElevationRequired, "config baseline: custody cannot verify elevation: "+custody.Reason())
		}
		fingerprint, key, err := elevation.BoundTrust(custody, elevation.NewFileBackend(dataDir), clock)
		if err != nil {
			return err
		}
		return rpc.VerifyRecordedAttestation(p.Attestation, p.Method, p.RawParams, rpc.MapTrustStore{fingerprint: key})
	}
}
