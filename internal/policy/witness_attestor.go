// Purpose: the Attestor that vouches for an elevated verb only when the
//
//	elevation middleware verified an attestation for that very verb in this
//	call's context.
//
// Inputs: the handler's context and the verb it is about to authorize.
// Outputs: WitnessAttestor.
// Constraints: FAIL CLOSED. No witness, a zero witness and a witness for a
//
//	different verb all refuse with KindElevationRequired. The witness is
//	minted per dispatch by internal/rpc (nothing outside it can build one),
//	so a verb never rides on another verb's attestation, and this package
//	holds no keystore, nonce or clock of its own.
//
// SPORT: internal/policy WitnessAttestor/ADDED (P1-SEC-03).

package policy

import (
	"context"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// witnessAttestor reads the middleware's witness from the context.
type witnessAttestor struct{ by rpc.WitnessVerifier }

// WitnessAttestor returns the Attestor the daemon's policy wiring passes as
// the elevation source: FreshLocalAttestation returns nil exactly when ctx
// carries a witness minted under the verifier's issuer (the daemon's, from its
// elevation status) whose Method equals the verb asked about. A witness from
// any other middleware, and the zero verifier, refuse.
func WitnessAttestor(by rpc.WitnessVerifier) Attestor { return witnessAttestor{by: by} }

// FreshLocalAttestation implements Attestor.
func (a witnessAttestor) FreshLocalAttestation(ctx context.Context, method string) error {
	w, ok := rpc.VerifiedElevationFrom(ctx)
	if !ok || !w.IssuedBy(a.by) {
		return cascade.Newf(cascade.KindElevationRequired,
			"policy: %s needs a fresh local attestation and this call carries none", method)
	}
	if w.Method() != method {
		return cascade.Newf(cascade.KindElevationRequired,
			"policy: the attestation on this call covers a different verb than %s", method)
	}
	return nil
}
