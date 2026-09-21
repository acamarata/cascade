// Purpose (this file): the pairing code's KEYED digest — the HKDF derivation
//
//	that turns a bridge instance's own credential into an HMAC key, and the
//	digest function every stored and compared code goes through.
//
// WHY IT IS ITS OWN FILE: pairing.go holds the verifier's state machine and is
//
//	at Art.10.3's 300-line cap; the key is a separate concern with separate
//	tests (same code + different credential = different digest).
//
// Inputs: a bridge credential (a Telegram bot token) for derivation; a key and
//
//	a candidate code for the digest.
//
// Outputs: a 32-byte key that is never persisted, and a hex HMAC-SHA256
//
//	digest — or ErrNoPairCodeKey, because there is no unkeyed fallback.
//
// Constraints: see pairing.go's header for WHY the digest is keyed (a bare
//
//	sha256 of 40 bits of Crockford base32 is brute-forceable inside the code's
//	own 10-minute TTL, so a stolen cascade.db would be enough to pair).
//
// SPORT: plugins/cascade-pa DerivePairCodeKey/ADDED, CodeDigest/CHANGED
//
//	(P1-E23-W5-S48-T1).

package cascadepa

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// pairCodeKeyInfo is the HKDF context string separating this key from every
// other secret derived from the same token. It is part of the key's identity:
// changing it invalidates every outstanding code, which is why it is a
// constant and not a parameter.
const pairCodeKeyInfo = "cascade-pa/pair-code"

// pairCodeKeyLen is the derived key length in bytes (HMAC-SHA256's block-
// independent 256-bit key size).
const pairCodeKeyLen = 32

// ErrNoPairCodeKey is the refusal a store with no derived key returns from
// both verbs. Exported so a wiring test can assert the fail-closed default by
// identity rather than by message shape.
var ErrNoPairCodeKey = cascade.New(cascade.KindUnavailable,
	"cascade-pa: no pairing-code key is derived; the bridge refuses to issue or verify a code")

// DerivePairCodeKey derives the pairing-code HMAC key from a bridge
// instance's own credential (a Telegram bot token) with HKDF-SHA256.
//
// The credential is the input keying material and never leaves this call: the
// caller keeps only the derived key, in memory, and nothing persists it. Two
// bots therefore produce two different digests for the same code, so a digest
// lifted from one host's database says nothing about another's — and a
// database read WITHOUT the token cannot verify any code at all.
func DerivePairCodeKey(credential string) ([]byte, error) {
	if strings.TrimSpace(credential) == "" {
		return nil, cascade.New(cascade.KindInvalidInput,
			"cascade-pa: deriving a pairing-code key needs the bridge credential")
	}
	key, err := hkdf.Key(sha256.New, []byte(credential), nil, pairCodeKeyInfo, pairCodeKeyLen)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "cascade-pa: derive the pairing-code key")
	}
	return key, nil
}

// CodeDigest is the canonical digest a code is stored and compared under:
// HMAC-SHA256 of its upper-cased form under key, hex-encoded. Exported so a
// host or a test can seed a pending code without inventing a second hashing
// rule. An empty key is refused — see this file's header on why there is no
// unkeyed fallback.
func CodeDigest(key []byte, code string) (string, error) {
	if len(key) == 0 {
		return "", ErrNoPairCodeKey
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(strings.ToUpper(strings.TrimSpace(code))))
	return hex.EncodeToString(mac.Sum(nil)), nil
}
