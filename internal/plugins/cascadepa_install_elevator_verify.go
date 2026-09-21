package plugins

// Purpose (this file): the install.AttestationVerifier adapter
//   cascadepa_install_elevator.go's Elevate hands to
//   install.VerifyElevationWitness -- split out under the 300-line cap
//   (round-2 rework, T0 decision D2).
//
// REWORK (round-2, D2): the prior draft's install.NewElevationWitness took
//   a bare request-id string with no verification -- the ticket's own
//   tests could (and did) mint a "valid" witness from a literal. Now the
//   ONLY way to mint one is install.VerifyElevationWitness, whose sole
//   verification call is elevationAttestationVerifier.Verify below: it
//   re-encodes the SAME attestation as the elevated envelope and calls the
//   SAME rpc.ElevationMiddleware gate Elevate already built (nonce/trust/
//   replay, via rpc.VerifyAttestation under the hood) -- one verification
//   point, not a second independent recheck of the same attestation.
//
// Inputs: an install.Attestation (VerifyElevationWitness's own parameter).
// Outputs: nil (verified) or the gate's own typed refusal (invalid
//   signature, expired, replayed nonce, unenrolled key).
// Constraints: Verify never mints anything itself -- it only reports
//   whether att verifies; VerifyElevationWitness (plugins/cascade-pa/
//   install/elevation.go) is the sole minting point.
// SPORT: internal/plugins:cascadepa-install-wiring (ADD) -- FIX P1-E24-W5-S50-T4 (D2).

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/cascade-pa/install"
)

// elevationAttestationVerifier adapts one rpc.ElevationMiddleware gate call
// to install.AttestationVerifier. See this file's header for why Verify's
// own envelope-encode-then-gate call IS the real verification, not a
// second one.
type elevationAttestationVerifier struct {
	gate rpc.HandlerFunc
	args json.RawMessage
}

// Verify implements install.AttestationVerifier.
func (v elevationAttestationVerifier) Verify(ctx context.Context, att install.Attestation) error {
	envelope, err := json.Marshal(installElevatedEnvelope{Attestation: rpcAttestationFromInstall(att), Args: v.args})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "cascade-pa install: encode elevated request")
	}
	_, err = v.gate(ctx, envelope)
	return err
}

// installAttestationFromRPC converts att (cascadepa_install_elevator.go's
// own signed rpc.Attestation) into the install-package-local shape
// VerifyElevationWitness takes, field for field.
func installAttestationFromRPC(att rpc.Attestation) install.Attestation {
	return install.Attestation{
		RequestID: att.RequestID, ActionHash: att.ActionHash, Nonce: att.Nonce,
		PubkeyFingerprint: att.PubkeyFingerprint, IssuedUnix: att.IssuedUnix,
		ExpUnix: att.ExpUnix, SigB64: att.SigB64,
	}
}

// rpcAttestationFromInstall is installAttestationFromRPC's inverse, used to
// rebuild the envelope Verify sends over gate -- the exact same
// rpc.Attestation value cascadepa_install_elevator.go signed, round-tripped
// through the install-package boundary rather than closed over directly,
// so Verify's only input is the Attestation VerifyElevationWitness handed
// it.
func rpcAttestationFromInstall(att install.Attestation) rpc.Attestation {
	return rpc.Attestation{
		RequestID: att.RequestID, ActionHash: att.ActionHash, Nonce: att.Nonce,
		PubkeyFingerprint: att.PubkeyFingerprint, IssuedUnix: att.IssuedUnix,
		ExpUnix: att.ExpUnix, SigB64: att.SigB64,
	}
}
