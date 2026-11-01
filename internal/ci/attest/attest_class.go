package attest

// Purpose: the closed AttestorClass enum and DeriveClass, the only source
// of a record's class (contract:ci-attestation, R18 M3).
//
// Inputs: a SignedAttestation, the TrustStore that resolves node keys and
// an optional CountersignVerifier for controller-countersigned OIDC records.
// Outputs: enrolled-node, ephemeral-oidc or unverified; never the
// signer-asserted attestor_class.
// Constraints: the zero class is unverified (never permissive); unknown
// and empty names parse to unverified; Eligible is true for enrolled-node
// and ephemeral-oidc only.
// SPORT: internal.ci.attest.AttestorClass/ADDED,
// internal.ci.attest.DeriveClass/ADDED (P1-CI-04).

import (
	"context"
)

// AttestorClass names who attested a CI run.
type AttestorClass string

// The closed set of attestor classes.
const (
	// ClassEnrolledNode: signed by an enrolled node's current identity key.
	ClassEnrolledNode AttestorClass = "enrolled-node"
	// ClassEphemeralOIDC: an ephemeral runner whose OIDC token the
	// controller verified and countersigned.
	ClassEphemeralOIDC AttestorClass = "ephemeral-oidc"
	// ClassUnattested: evidence with no attesting signer.
	ClassUnattested AttestorClass = "unattested"
	// ClassUnverified: any class that could not be proven.
	ClassUnverified AttestorClass = "unverified"
)

// ParseAttestorClass maps s to its class; unknown and empty names are
// ClassUnverified.
func ParseAttestorClass(s string) AttestorClass {
	switch c := AttestorClass(s); c {
	case ClassEnrolledNode, ClassEphemeralOIDC, ClassUnattested:
		return c
	case ClassUnverified:
		return ClassUnverified
	default:
		return ClassUnverified
	}
}

// Eligible reports whether c may satisfy a CI gate: enrolled-node and
// ephemeral-oidc only.
func (c AttestorClass) Eligible() bool {
	return c == ClassEnrolledNode || c == ClassEphemeralOIDC
}

// CountersignVerifier proves a record was countersigned by the controller
// key after its OIDC token verified (P1-CI-14 implements it). A nil error
// means the countersignature holds for exactly these canonical bytes.
type CountersignVerifier interface {
	VerifyCountersign(ctx context.Context, sa SignedAttestation) error
}

// DeriveClass derives sa's attestor class from its proof, never from the
// record's own attestor_class field:
//   - enrolled-node: both oidc fields empty and the signature verifies
//     under the current key ts resolves for (node_id, key_id);
//   - ephemeral-oidc: both oidc fields set and countersign verifies;
//   - anything else, or a signer-asserted class that disagrees with the
//     derived one, is unverified.
func DeriveClass(sa SignedAttestation, ts TrustStore, countersign CountersignVerifier) AttestorClass {
	ctx := context.Background()
	rec, err := DecodeCanonical(sa.CanonicalJSON)
	if err != nil {
		return ClassUnverified
	}
	derived := ClassUnverified
	switch {
	case rec.OIDCTokenSHA256 == "" && rec.OIDCRunID == "":
		if ts != nil {
			if _, err := verifiedRecord(ctx, sa, ts); err == nil {
				derived = ClassEnrolledNode
			}
		}
	case rec.OIDCTokenSHA256 != "" && rec.OIDCRunID != "":
		if countersign != nil && countersign.VerifyCountersign(ctx, sa) == nil {
			derived = ClassEphemeralOIDC
		}
	}
	if ParseAttestorClass(string(rec.AttestorClass)) != derived {
		return ClassUnverified
	}
	return derived
}
