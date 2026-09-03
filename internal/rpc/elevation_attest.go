package rpc

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Attestation is the hardware-backed signed attestation the elevation
// helper (D/S-07.T6) posts back to satisfy an ELEVATION_REQUIRED
// challenge, in the wire shape this ticket's contract names verbatim:
// {request_id, action_hash, nonce, pubkey_fingerprint, sig_b64,
// issued_unix, exp_unix}.
type Attestation struct {
	RequestID         string `json:"request_id"`
	ActionHash        string `json:"action_hash"`
	Nonce             string `json:"nonce"`
	PubkeyFingerprint string `json:"pubkey_fingerprint"`
	SigB64            string `json:"sig_b64"`
	IssuedUnix        int64  `json:"issued_unix"`
	ExpUnix           int64  `json:"exp_unix"`
}

// signedFields returns the exact byte sequence VerifyAttestation checks
// SigB64 against: the canonical (alphabetically-keyed, compact) JSON
// encoding of every Attestation field EXCEPT SigB64 itself. This concrete
// byte format is a design decision this ticket makes explicit because
// S-07.T6 (the attestation producer) had not landed a canonical format as
// of this ticket — recorded here, matching the R-14.148/R-14.152 pattern of
// documenting a concrete decision where a consuming ticket needed one
// before its producer existed, so S-07.T6 must produce signatures over
// exactly this byte sequence when it lands.
func signedFields(a Attestation) []byte {
	// Field order is fixed and alphabetical by JSON tag name, independent
	// of struct declaration order, so a future struct-field reorder can
	// never silently change the signed bytes.
	type signable struct {
		ActionHash        string `json:"action_hash"`
		ExpUnix           int64  `json:"exp_unix"`
		IssuedUnix        int64  `json:"issued_unix"`
		Nonce             string `json:"nonce"`
		PubkeyFingerprint string `json:"pubkey_fingerprint"`
		RequestID         string `json:"request_id"`
	}
	b, err := json.Marshal(signable{
		ActionHash:        a.ActionHash,
		ExpUnix:           a.ExpUnix,
		IssuedUnix:        a.IssuedUnix,
		Nonce:             a.Nonce,
		PubkeyFingerprint: a.PubkeyFingerprint,
		RequestID:         a.RequestID,
	})
	if err != nil {
		// signable's fields are all primitive types (string/int64); this
		// is unreachable except by a future refactor mistake. Returning
		// nil (rather than panicking on untrusted-input-adjacent code)
		// makes ed25519.Verify fail closed instead of crashing the daemon.
		return nil
	}
	return b
}

// TrustStore resolves an enrolled Ed25519 public key by its fingerprint, in
// the S-07.T6 trust-store record format. This ticket depends only on the
// lookup shape, not on how S-07.T6 persists records.
type TrustStore interface {
	// Lookup returns the enrolled public key for fingerprint, and whether
	// an enrollment exists for it at all.
	Lookup(fingerprint string) (ed25519.PublicKey, bool)
}

// MapTrustStore is a minimal in-memory TrustStore, used by this ticket's
// own tests (a locally generated Ed25519 key, per the contract) and
// suitable as a placeholder until S-07.T6's real trust store lands.
type MapTrustStore map[string]ed25519.PublicKey

// Lookup implements TrustStore.
func (m MapTrustStore) Lookup(fingerprint string) (ed25519.PublicKey, bool) {
	key, ok := m[fingerprint]
	return key, ok
}

// VerifyAttestation checks att against every one of this ticket's binding
// requirements, in order, returning a typed cascade error identifying the
// first failure:
//  1. the enrolled pubkey for att.PubkeyFingerprint exists (KindNotFound);
//  2. att.SigB64 decodes as valid base64 (KindInvalidInput);
//  3. the Ed25519 signature verifies over signedFields(att) against the
//     enrolled key (KindIntegrity — a verification failure, distinct from
//     malformed input);
//  4. att.ExpUnix has not passed as of now (KindTimeout);
//  5. att.Method/action_hash match method/paramsHash for the pending
//     request (KindInvalidInput) — action_hash is checked against
//     paramsHash directly, since this ticket's params_hash binding field
//     IS the action hash for a plain RPC call (no separate action encoding
//     exists at this layer);
//  6. the ledger accepts a single-use Consume of att.Nonce bound to
//     {method, paramsHash} (whatever typed error Consume returns).
//
// Any failure is a typed denial; nothing here ever partially applies (no
// early nonce consumption on a signature failure, for example — Consume is
// the LAST check, so an invalid attestation never burns the nonce a valid
// retry would still need... except that Consume itself is single-use by
// design once reached, matching the contract's "invalid, expired, or
// replayed attestation yields a typed denial" without over-specifying
// whether a signature failure should also burn the nonce, which it
// deliberately does not: only a nonce that reaches Consume — i.e. one
// whose attestation is otherwise fully valid — is spent.
func VerifyAttestation(att Attestation, trust TrustStore, ledger *NonceLedger, method, paramsHash string, now time.Time) error {
	pubkey, ok := trust.Lookup(att.PubkeyFingerprint)
	if !ok {
		return cascade.New(cascade.KindNotFound, "elevation: unknown pubkey_fingerprint")
	}

	sig, err := base64.StdEncoding.DecodeString(att.SigB64)
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "elevation: sig_b64 is not valid base64")
	}

	if !ed25519.Verify(pubkey, signedFields(att), sig) {
		return cascade.New(cascade.KindIntegrity, "elevation: attestation signature verification failed")
	}

	if now.After(time.Unix(att.ExpUnix, 0)) {
		return cascade.New(cascade.KindTimeout, "elevation: attestation expired")
	}

	if att.ActionHash != paramsHash {
		return cascade.New(cascade.KindInvalidInput,
			"elevation: attestation action_hash does not match the pending request's params")
	}

	return ledger.Consume(att.Nonce, method, paramsHash, now)
}
