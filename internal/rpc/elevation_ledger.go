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

// maxPendingNonces bounds the ledger: Issue refuses once this many unexpired
// nonces are pending. Anonymous callers can mint nonces on any elevated verb,
// so the table must not grow with their patience.
const maxPendingNonces = 1024

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
	// claimed is set while one attested call is verifying the nonce, so a
	// concurrent call on the same nonce is refused before it writes a record.
	claimed bool
	// burned marks a tombstone: the nonce failed a verification and can never
	// succeed, but it keeps its slot until it expires, so recycling failed
	// nonces cannot lift the bound on anonymous audit appends.
	burned bool
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
// and returns the nonce string. Expired entries are reaped first; when
// maxPendingNonces unexpired entries are still pending it issues nothing and
// returns a typed cascade.KindUnavailable error. If crypto/rand.Read errors (OS entropy
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
	now := l.clock.Now()
	for n, e := range l.entries {
		if now.After(e.expires) {
			delete(l.entries, n)
		}
	}
	if len(l.entries) >= maxPendingNonces {
		return "", cascade.New(cascade.KindUnavailable, "elevation: too many pending challenges; retry shortly")
	}
	l.entries[nonce] = pendingElevation{
		method:     method,
		paramsHash: paramsHash,
		expires:    now.Add(nonceTTL),
	}
	return nonce, nil
}

// revoke drops a nonce Issue handed out but the caller could not deliver
// (the attempt record was not appended). Unknown nonces are ignored. It is
// for a nonce no one else has seen; a nonce that failed verification is burnt
// instead, so it keeps its slot.
func (l *NonceLedger) revoke(nonce string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, nonce)
}

// claim reserves a pending nonce for one verification. It returns Consume's
// KindNotFound error for a nonce that was never issued, is burnt, or is
// already claimed by a concurrent call, so only one attested call per nonce
// reaches the audit log. It does not check the binding; Consume does.
func (l *NonceLedger) claim(nonce string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[nonce]
	if !ok || e.burned || e.claimed {
		return cascade.New(cascade.KindNotFound, "elevation: nonce not found (unissued, already consumed, or reaped)")
	}
	e.claimed = true
	l.entries[nonce] = e
	return nil
}

// release undoes claim for a call that refused before verifying (the attempt
// record could not be appended), so the nonce survives for a retry.
func (l *NonceLedger) release(nonce string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.entries[nonce]; ok && !e.burned {
		e.claimed = false
		l.entries[nonce] = e
	}
}

// burn turns a failed nonce into a tombstone: no later claim or Consume
// accepts it, and it holds its slot against maxPendingNonces until it
// expires. Unknown nonces are ignored.
func (l *NonceLedger) burn(nonce string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.entries[nonce]; ok {
		e.burned, e.claimed = true, false
		l.entries[nonce] = e
	}
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
// A success deletes the entry. An expired entry is deleted. A binding
// mismatch burns the entry (a tombstone that keeps its slot until it
// expires), so a nonce is single-use even under a failed verification — an
// attacker cannot retry a slightly-wrong attestation against the same nonce,
// and cannot recycle the slot to mint more audit records. A burnt entry
// answers KindNotFound like an unissued one.
func (l *NonceLedger) Consume(nonce, method, paramsHash string, now time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	entry, ok := l.entries[nonce]
	if !ok || entry.burned {
		return cascade.New(cascade.KindNotFound, "elevation: nonce not found (unissued, already consumed, or reaped)")
	}
	if now.After(entry.expires) {
		delete(l.entries, nonce)
		return cascade.New(cascade.KindTimeout, "elevation: nonce expired")
	}
	if entry.method != method || entry.paramsHash != paramsHash {
		entry.burned, entry.claimed = true, false
		l.entries[nonce] = entry
		return cascade.New(cascade.KindInvalidInput,
			"elevation: nonce binding mismatch (method/params_hash do not match the pending request)")
	}
	delete(l.entries, nonce)
	return nil
}

// Len reports the number of entries the ledger holds: pending nonces plus
// burnt tombstones that have not yet expired. Test-only introspection; production code has no use for it.
func (l *NonceLedger) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}
