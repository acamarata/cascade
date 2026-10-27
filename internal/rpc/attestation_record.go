package rpc

// Purpose: verification of an attestation that was persisted as proof (the
//
//	signed config baseline), and the witness accessor a handler uses to
//	obtain the attestation the middleware verified for its call.
//
// Inputs: the attestation JSON, the elevated method, the byte-exact params,
//
//	and the trust store (production: the custody-bound trust root).
//
// Outputs: nil only when the signature verifies against an enrolled key,
//
//	the signed method is method and action_hash is ParamsHash(rawParams).
//
// Constraints: no nonce or expiry check: a persisted record is re-verified
//
//	long after its nonce was spent and its window closed. The signed method
//	must equal method (an always-elevated verb), so one signature never
//	verifies for two methods, and a pre-method record refuses. Unknown
//	attestation fields are refused.
//
// SPORT: internal/rpc recorded attestation verification (P1-SEC-04).

import (
	"bytes"
	"encoding/json"

	"github.com/acamarata/cascade/pkg/cascade"
)

// config.accept_loosening is owner-only: it is the one verb that admits a
// looser config, so no delegated caller may reach it.
var _ = DeclareAccess("config.accept_loosening", AccessOwnerOnly)

// VerifyRecordedAttestation verifies a persisted attestation for method over
// rawParams against trust.
func VerifyRecordedAttestation(att json.RawMessage, method string, rawParams []byte, trust TrustStore) error {
	if trust == nil {
		return cascade.New(cascade.KindIntegrity, "elevation: no trust root for a recorded attestation")
	}
	if !alwaysElevatedVerb(method) {
		return cascade.New(cascade.KindInvalidInput, "elevation: recorded attestation names a verb that is not always elevated")
	}
	dec := json.NewDecoder(bytes.NewReader(att))
	dec.DisallowUnknownFields()
	var a Attestation
	if err := dec.Decode(&a); err != nil || dec.More() {
		return cascade.New(cascade.KindInvalidInput, "elevation: recorded attestation is malformed")
	}
	if err := verifySignedAttestation(a, trust, method, hashParams(rawParams)); err != nil {
		return err
	}
	return nil
}

// RecordedAttestation returns, as JSON, the attestation the middleware
// verified before it attached this witness. It reports false for a witness
// that carries none.
func (v VerifiedElevation) RecordedAttestation() (json.RawMessage, bool) {
	if v.attestation.SigB64 == "" || v.attestation.PubkeyFingerprint == "" {
		return nil, false
	}
	raw, err := json.Marshal(v.attestation)
	if err != nil {
		return nil, false
	}
	return raw, true
}

// alwaysElevatedVerb reports whether method is an always-elevated row of the
// elevation table. It reads the table without sealing it: a boot-time check
// runs before the registry's first dispatch, and sealing there would refuse
// later registrations.
func alwaysElevatedVerb(method string) bool {
	elevationMu.RLock()
	defer elevationMu.RUnlock()
	for _, r := range elevationTable {
		if r.method == method {
			return r.always
		}
	}
	return false
}
