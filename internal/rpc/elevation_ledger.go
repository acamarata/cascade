package rpc

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// nonceTTL is the single-use ledger's per-entry expiry, per this ticket's
// contract ("5-min expiry").
const nonceTTL = 5 * time.Minute

// nonceLen is the byte length read from crypto/rand for each nonce (256
// bits), hex-encoded to a 64-character string.
const nonceLen = 32

// pendingElevation is one in-flight elevation the ledger tracks between
// ELEVATION_REQUIRED and the attestation that (attempts to) satisfy it:
// the binding fields {method, params_hash, exp} the eventual attestation
// must match, per this ticket's contract.
type pendingElevation struct {
	method     string
	paramsHash string
	expires    time.Time
}

// NonceLedger is the in-memory single-use elevation-nonce ledger. Per this
// ticket's contract, the persistent audit-domain ledger integration is
// H/S-16 and I/S-18's (W2) — this in-memory ledger is the complete W1
// implementation, deliberately not durable across a daemon restart.
type NonceLedger struct {
	mu      sync.Mutex
	clock   runtime.Clock
	entries map[string]pendingElevation
}

// NewNonceLedger returns an empty ledger driven by clock — NEVER
// time.Now/time.Since directly (R-14.132's forbidigo + AST alias gate;
// clock is the injected internal/runtime.Clock, matching internal/daemon's
// own convention).
func NewNonceLedger(clock runtime.Clock) *NonceLedger {
	return &NonceLedger{clock: clock, entries: make(map[string]pendingElevation)}
}

// Issue generates a fresh crypto/rand nonce bound to method and paramsHash,
// records it in the ledger with a nonceTTL expiry from the ledger's clock,
// and returns the nonce string. If crypto/rand.Read errors (OS entropy
// failure — vanishingly rare, but not ignorable for a security nonce) Issue
// returns a typed cascade.KindUnavailable error and issues NO nonce; it
// never falls back to a weaker source (math/rand is both forbidden by
// R-14.132's AST gate for domain logic and would defeat the point of a
// cryptographic nonce).
func (l *NonceLedger) Issue(method, paramsHash string) (string, error) {
	buf := make([]byte, nonceLen)
	// nolint:forbidigo // this is crypto/rand.Read (cryptographically
	// secure), not math/rand.Read — the two packages share the selector
	// text "rand.Read" and forbidigo matches text, not the resolved
	// import (the same class of limitation R-14.132 documents for
	// forbidigo's clock/rand rules); math/rand is not imported anywhere
	// in this package, and crypto/rand is exactly what a security nonce
	// requires.
	if _, err := rand.Read(buf); err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err,
			"elevation: crypto/rand unavailable, refusing to issue a weak nonce")
	}
	nonce := hex.EncodeToString(buf)

	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries[nonce] = pendingElevation{
		method:     method,
		paramsHash: paramsHash,
		expires:    l.clock.Now().Add(nonceTTL),
	}
	return nonce, nil
}

// Consume validates and single-use-consumes nonce against the binding
// fields {method, paramsHash, exp}. It returns a typed cascade error (never
// a bare error) for every rejection path so callers can distinguish:
//   - KindNotFound: nonce was never issued, already consumed (replay), or
//     — because Consume deletes on read of an expired entry too — was
//     issued but has since expired and been reaped by a prior Consume call.
//   - KindTimeout: nonce exists and matches but is expired as of now.
//   - KindInvalidInput: nonce exists, is not expired, but method or
//     paramsHash does not match what it was issued for (cross-method /
//     cross-request replay attempt).
//
// In every rejection case the entry is deleted (if present) before
// returning, so a nonce is single-use even under a failed verification —
// an attacker cannot retry a slightly-wrong attestation against the same
// nonce.
func (l *NonceLedger) Consume(nonce, method, paramsHash string, now time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	entry, ok := l.entries[nonce]
	if ok {
		delete(l.entries, nonce)
	}
	if !ok {
		return cascade.New(cascade.KindNotFound, "elevation: nonce not found (unissued, already consumed, or reaped)")
	}
	if now.After(entry.expires) {
		return cascade.New(cascade.KindTimeout, "elevation: nonce expired")
	}
	if entry.method != method || entry.paramsHash != paramsHash {
		return cascade.New(cascade.KindInvalidInput,
			"elevation: nonce binding mismatch (method/params_hash do not match the pending request)")
	}
	return nil
}

// Len reports the number of currently-pending (unconsumed, unexpired-or-not)
// entries. Test-only introspection; production code has no use for it.
func (l *NonceLedger) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}
