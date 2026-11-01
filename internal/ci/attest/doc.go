// Package attest is the CI attestation core (contract:ci-attestation): the
// versioned canonical AttestationRecord, Attest over a custody signing
// function, the closed AttestorClass enum, VerifySignature over a
// TrustStore and the two trust stores (node records, controller identity).
//
// It is a leaf: it imports neither internal/ci nor internal/jobs, so the
// jobs completion gate and the ci command tree can both import it. It may
// import internal/nodes and pkg/*.
//
// Trust rules every consumer relies on:
//   - a record is read only from the canonical bytes that were signed
//     (DecodeCanonical); a caller-supplied Record value is never trusted;
//   - the signature covers "cascade.ci.attestation.v1\x1f" + canonical
//     bytes, so no other payload the node key signs can pass as a record;
//   - the attestor class is derived (DeriveClass), never read from the
//     record;
//   - verification never consumes a nonce.
//
// SPORT: internal.ci.attest/ADDED (P1-CI-04).
package attest
