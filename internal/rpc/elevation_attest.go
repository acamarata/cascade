package rpc

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"time"
	"unicode/utf8"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Attestation is the hardware-backed signed attestation the elevation
// helper (D/S-07.T6) posts back to satisfy an ELEVATION_REQUIRED
// challenge: {request_id, action_hash, method, nonce, pubkey_fingerprint,
// sig_b64, issued_unix, exp_unix}. Method names the RPC method the
// attestation authorizes; it is signed, so one attestation can never be
// presented for another verb whose params are byte-identical (P1-BF-R266).
type Attestation struct {
	RequestID         string `json:"request_id"`
	ActionHash        string `json:"action_hash"`
	Method            string `json:"method"`
	Nonce             string `json:"nonce"`
	PubkeyFingerprint string `json:"pubkey_fingerprint"`
	SigB64            string `json:"sig_b64"`
	IssuedUnix        int64  `json:"issued_unix"`
	ExpUnix           int64  `json:"exp_unix"`
}

// AttestationSignedBytes returns the exact bytes an attestation signer signs
// and VerifyAttestation checks SigB64 against. Every signer (the CLI, the
// elevate-helper, the in-process backup and install ceremonies) calls this
// one function, so the format exists once.
func AttestationSignedBytes(a Attestation) []byte { return signedFields(a) }

// signedFields returns the canonical (alphabetically-keyed, compact) JSON
// encoding of every Attestation field EXCEPT SigB64 itself:
// {action_hash, exp_unix, issued_unix, method, nonce, pubkey_fingerprint,
// request_id}. action_hash is ParamsHash of the raw params only; method is
// what binds the verb (P1-BF-R266). An attestation signed over the earlier
// six-field bytes (no method) cannot verify against these bytes, so the
// pre-method format is refused rather than migrated.
func signedFields(a Attestation) []byte {
	// Field order is fixed and alphabetical by JSON tag name, independent
	// of struct declaration order, so a future struct-field reorder can
	// never silently change the signed bytes.
	type signable struct {
		ActionHash        string `json:"action_hash"`
		ExpUnix           int64  `json:"exp_unix"`
		IssuedUnix        int64  `json:"issued_unix"`
		Method            string `json:"method"`
		Nonce             string `json:"nonce"`
		PubkeyFingerprint string `json:"pubkey_fingerprint"`
		RequestID         string `json:"request_id"`
	}
	b, err := json.Marshal(signable{
		ActionHash:        a.ActionHash,
		ExpUnix:           a.ExpUnix,
		IssuedUnix:        a.IssuedUnix,
		Method:            a.Method,
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

// VerifyAttestation checks att against every binding requirement, in
// order, returning a typed cascade error identifying the first failure:
//  1. the enrolled pubkey for att.PubkeyFingerprint exists (KindNotFound);
//  2. att.SigB64 decodes as valid base64 (KindInvalidInput);
//  3. the Ed25519 signature verifies over signedFields(att) against the
//     enrolled key (KindIntegrity — a verification failure, distinct from
//     malformed input);
//  4. att.ExpUnix has not passed as of now (KindTimeout);
//  5. the signed att.Method equals the method being dispatched, and
//     att.ActionHash equals paramsHash (KindInvalidInput) — the method is
//     checked from the signed bytes, not only through the ledger, so the
//     binding holds wherever an attestation is verified;
//  6. the ledger accepts a single-use Consume of att.Nonce bound to
//     {method, paramsHash} (whatever typed error Consume returns).
//
// Nothing here partially applies: Consume is the LAST check, so only a
// nonce whose attestation is otherwise fully valid is spent here (the
// middleware separately burns a claimed nonce after a failed verification).
func VerifyAttestation(att Attestation, trust TrustStore, ledger *NonceLedger, method, paramsHash string, now time.Time) error {
	if err := verifyAttestationSignature(att, trust); err != nil {
		return err
	}
	if now.After(time.Unix(att.ExpUnix, 0)) {
		return cascade.New(cascade.KindTimeout, "elevation: attestation expired")
	}
	if err := checkAttestationBinding(att, method, paramsHash); err != nil {
		return err
	}
	return ledger.Consume(att.Nonce, method, paramsHash, now)
}

// verifySignedAttestation is the ledger-free, expiry-free check for an
// attestation kept as a persisted record (P1-SEC-04's accepted baseline): the
// signature by an enrolled key over the signed bytes, then the signed method
// and action_hash against the expected method and params hash. It carries no
// replay or freshness guarantee, so it is never a substitute for
// VerifyAttestation on a live dispatch.
func verifySignedAttestation(att Attestation, trust TrustStore, method, paramsHash string) error {
	if err := verifyAttestationSignature(att, trust); err != nil {
		return err
	}
	return checkAttestationBinding(att, method, paramsHash)
}

// verifyAttestationSignature runs checks 1-3: enrolled key, base64, and the
// Ed25519 signature over signedFields(att).
func verifyAttestationSignature(att Attestation, trust TrustStore) error {
	if trust == nil {
		return cascade.New(cascade.KindNotFound, "elevation: no trust store")
	}
	pubkey, ok := trust.Lookup(att.PubkeyFingerprint)
	if !ok || len(pubkey) != ed25519.PublicKeySize {
		return cascade.New(cascade.KindNotFound, "elevation: unknown pubkey_fingerprint")
	}
	sig, err := base64.StdEncoding.DecodeString(att.SigB64)
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "elevation: sig_b64 is not valid base64")
	}
	if !ed25519.Verify(pubkey, signedFields(att), sig) {
		return cascade.New(cascade.KindIntegrity, "elevation: attestation signature verification failed")
	}
	return nil
}

// checkAttestationBinding refuses an attestation whose signed method is
// empty (the pre-method format), is not valid UTF-8 (json.Marshal would fold
// distinct invalid methods to the same signed bytes) or names another method
// than the one being verified, or whose action_hash covers other params.
func checkAttestationBinding(att Attestation, method, paramsHash string) error {
	if att.Method == "" || method == "" || !utf8.ValidString(att.Method) {
		return cascade.New(cascade.KindInvalidInput,
			"elevation: attestation names no method; an attestation must sign the method it authorizes")
	}
	if att.Method != method {
		return cascade.New(cascade.KindInvalidInput,
			"elevation: attestation method does not match the method being dispatched")
	}
	if att.ActionHash != paramsHash {
		return cascade.New(cascade.KindInvalidInput,
			"elevation: attestation action_hash does not match the pending request's params")
	}
	return nil
}
