package rpc

// Purpose (this file): the proof, carried in a handler's context, that the
//   elevation middleware verified an attestation for exactly this call.
// Inputs: the method and params hash the middleware verified.
// Outputs: VerifiedElevation, VerifiedElevationFrom, ParamsHash.
// Constraints: nothing outside this package can build a non-zero
//   VerifiedElevation: the fields are unexported, the only constructor
//   (newVerifiedElevation) is unexported, and it is called from exactly one
//   place, the middleware's success path after the grant record was
//   appended, and only when the middleware's deps carry a WitnessIssuer
//   (a gate without one runs its handler with no witness). The witness
//   remembers its issuer; a reader that must trust one domain checks
//   IssuedBy with the
//   verify-only handle its issuer hands out. The context key is an unexported type, so no foreign
//   context.WithValue can collide with or fake it. A zero value reports
//   absent. A reader must compare Method() with the verb it is about to
//   perform: a witness for one verb says nothing about another.
// SPORT: internal/rpc VerifiedElevation (ADD) — P1-SEC-03.

import (
	"context"
	"encoding/json"
	"time"
)

// witnessKey is the context key of the witness.
type witnessKey struct{}

// WitnessIssuer is the capability to mint witnesses. One is created per
// trust domain (the daemon builds one beside its bound custody) and handed
// to the middleware that guards it and to the attestor that reads it. A
// witness remembers its issuer, so a witness minted by any other middleware
// (a ceremony gate with its own trust store, a test gate) is never accepted
// by an attestor bound to a different issuer. Compared by pointer identity.
type WitnessIssuer struct{ _ byte }

// NewWitnessIssuer returns a fresh issuer.
func NewWitnessIssuer() *WitnessIssuer { return &WitnessIssuer{} }

// WitnessVerifier recognises the witnesses one issuer minted but cannot mint
// any: it is what a reader of witnesses (the policy attestor) is handed, so
// holding it grants no ability to forge. The zero value matches nothing.
type WitnessVerifier struct{ issuer *WitnessIssuer }

// Verifier returns the verify-only handle for this issuer.
func (i *WitnessIssuer) Verifier() WitnessVerifier { return WitnessVerifier{issuer: i} }

// VerifiedElevation records that the middleware verified an attestation for
// one method and one params hash.
type VerifiedElevation struct {
	method     string
	paramsHash string
	verifiedAt time.Time
	issuer     *WitnessIssuer
}

// IssuedBy reports whether the witness was minted under the verifier's
// issuer. The zero verifier matches nothing.
func (v VerifiedElevation) IssuedBy(by WitnessVerifier) bool {
	return by.issuer != nil && v.issuer == by.issuer
}

// Method is the elevated verb the attestation covered.
func (v VerifiedElevation) Method() string { return v.method }

// ParamsHash is the hash of the exact params the attestation covered, in the
// form ParamsHash returns.
func (v VerifiedElevation) ParamsHash() string { return v.paramsHash }

// VerifiedAt is when the middleware verified the attestation.
func (v VerifiedElevation) VerifiedAt() time.Time { return v.verifiedAt }

// newVerifiedElevation builds the witness. Only the middleware calls it.
func newVerifiedElevation(issuer *WitnessIssuer, method, paramsHash string, at time.Time) VerifiedElevation {
	return VerifiedElevation{method: method, paramsHash: paramsHash, verifiedAt: at, issuer: issuer}
}

// withVerifiedElevation attaches v to ctx.
func withVerifiedElevation(ctx context.Context, v VerifiedElevation) context.Context {
	return context.WithValue(ctx, witnessKey{}, v)
}

// withoutVerifiedElevation hides any witness already in ctx. The middleware
// applies it to every call it does not verify itself, so a witness minted for
// one dispatch never rides into another.
func withoutVerifiedElevation(ctx context.Context) context.Context {
	if _, ok := VerifiedElevationFrom(ctx); !ok {
		return ctx
	}
	return context.WithValue(ctx, witnessKey{}, VerifiedElevation{})
}

// VerifiedElevationFrom returns the witness attached to ctx. It reports false
// when ctx carries none, and for the zero value.
func VerifiedElevationFrom(ctx context.Context) (VerifiedElevation, bool) {
	v, ok := ctx.Value(witnessKey{}).(VerifiedElevation)
	if !ok || v.method == "" || v.paramsHash == "" || v.issuer == nil {
		return VerifiedElevation{}, false
	}
	return v, true
}

// ParamsHash is the byte-exact SHA-256 (hex) of raw, the value the nonce
// ledger binds and an attestation's action_hash must equal. It is not a
// hash of canonical JSON: two encodings of one value hash differently.
func ParamsHash(raw json.RawMessage) string { return hashParams(raw) }
