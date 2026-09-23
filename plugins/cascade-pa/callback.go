// Purpose: the R-21.210 server-side one-use callback nonce — minted beside
//   an outbound approval prompt, consumed exactly once by W/S-48.T4 before
//   any token is redeemed.
//
// Inputs: the LIVE claim W/S-48.T4's handler builds from the callback it
//   just received: {request_id, bridge instance, paired subject id, chat
//   id, message id}. The nonce (the map key) is the sixth bound fact.
//
// Outputs: Store records a nonce; Consume reports whether the stored
//   record existed, was unexpired, and matched every claimed field, and
//   returns the STORED record (never a caller-asserted one) so its own
//   AllowedVerdict is read back rather than trusted from the wire.
//
// Constraints (T0 ruling, s48t4-t0-decisions.txt D1 items 2 and 7, amended
//   by D3, 2026-09-21, superseding this file's original eight-field/
//   delete-first design — an adversarial CR found both load-bearing):
//   - THE VERDICT IS NEVER CLAIMED, ONLY STORED. R-21.210's wire contract
//     is "<request_id>|<nonce>" — the verdict word that used to ride on
//     callback_data is gone, so CallbackClaim carries none: which decision
//     a tap makes is entirely a property of WHICH nonce it presents (a
//     future producer mints one nonce per button, each with its own
//     AllowedVerdict), never a word a forged payload could try to flip.
//   - NO ACTION DIGEST LIVES HERE AT ALL (D3 amendment): the original
//     design kept CallbackNonce.ActionDigest on the stored record for a
//     future producer to populate, but the confirming review's Q1 found it
//     an orphan — written only by tests, read nowhere, protecting nothing.
//     It is not needed: internal/policy's approval queue already binds a
//     request id to its action at mint (actionHash+paramsHash, dedup keys
//     on both) and re-hashes at redemption, refusing ErrApprovalMismatch on
//     any difference — the real binding, one layer down from this nonce.
//     Keeping a second, dead field here would be state that looks like a
//     security check and is not one, which is worse than no field. Field
//     removed. (Scope deviation: an in-package change to this S-48.T1
//     file, surfaced by its S-48.T4 consumer; PCI sent.)
//   - CONSUME MATCHES BEFORE IT DELETES. The earlier draft deleted
//     unconditionally and compared after: a legitimate owner's mismatched
//     tap (a stale button, a wrong verdict on a re-rendered message) then
//     PERMANENTLY burned the nonce, leaving the real request undecidable
//     over the bridge — proven by CR probe (d). A mismatch now leaves the
//     record exactly as it was, so the owner's next, correct tap still
//     redeems it; only a MATCH consumes. An expired record is still
//     reaped on the read that finds it, since nothing is ever gained by
//     keeping a record that can never match again.
//
// SPORT: plugins/cascade-pa CallbackNonce/ADDED, CallbackNonceStore/ADDED
//   (P1-E23-W5-S48-T1); CallbackClaim.ActionDigest/REMOVED,
//   CallbackClaim.Verdict/REMOVED, Consume match-before-delete/CHANGED
//   (P1-E23-W5-S48-T4 rework, T0 D1 items 2/7); CallbackNonce.ActionDigest/
//   REMOVED (orphan field, P1-E23-W5-S48-T4 narrow fix, T0 D3 amendment).

package cascadepa

import (
	"sync"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// CallbackNonce is one outbound approval prompt's server-side binding. It
// carries no action digest (T0 D3 amendment): the approval queue one layer
// down already binds a request id to its action at mint and re-hashes at
// redemption, so a second, bridge-side digest would be dead security state.
type CallbackNonce struct {
	Nonce           string
	RequestID       string
	BridgeInstance  string
	PairedSubjectID string
	ChatID          string
	MessageID       string
	ExpiresAt       time.Time
	AllowedVerdict  bool
}

// CallbackClaim is what a callback asserts about itself — the fields
// R-21.210's WIRE contract can actually carry ("<request_id>|<nonce>")
// plus the ones the LIVE envelope supplies (never the button payload).
// It exists as a struct rather than positional arguments because Consume
// compares every field: a positional call would let two same-typed ids be
// swapped silently, and the swap would read as a legitimate refusal.
type CallbackClaim struct {
	Nonce           string
	RequestID       string
	BridgeInstance  string
	PairedSubjectID string
	ChatID          string
	MessageID       string
}

// CallbackNonceStore holds every outstanding nonce, keyed by its own value.
type CallbackNonceStore struct {
	mu     sync.Mutex
	nonces map[string]CallbackNonce
}

// NewCallbackNonceStore constructs an empty store.
func NewCallbackNonceStore() *CallbackNonceStore {
	return &CallbackNonceStore{nonces: make(map[string]CallbackNonce)}
}

// Store records n under n.Nonce. A duplicate nonce value is refused rather
// than overwritten: two live records under one key would make Consume's
// "exactly once" guarantee ambiguous about which one it consumed.
func (s *CallbackNonceStore) Store(n CallbackNonce) error {
	if n.Nonce == "" {
		return cascade.New(cascade.KindInvalidInput, "cascade-pa: callback nonce must be non-empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.nonces[n.Nonce]; exists {
		return cascade.Newf(cascade.KindConflict, "cascade-pa: callback nonce %q already stored", n.Nonce)
	}
	s.nonces[n.Nonce] = n
	return nil
}

// Consume atomically spends claim.Nonce and reports whether the stored
// record existed, was unexpired at now, and matched every claimed field.
// A caller must treat any false as "refuse the callback" and must never
// tell the sender which field disagreed.
//
// MATCH BEFORE DELETE (T0 D1 item 7): an expired record is reaped on the
// read that finds it — it can never match again, so nothing is gained by
// keeping it — but a record that is merely MISMATCHED is left exactly as
// it was, so a legitimate retry (the same nonce, the correct live fields)
// can still redeem it. Only a genuine match consumes the record, which is
// what still makes it one-use: a matched Consume deletes before returning.
func (s *CallbackNonceStore) Consume(claim CallbackClaim, now time.Time) (CallbackNonce, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.nonces[claim.Nonce]
	if !ok {
		return CallbackNonce{}, false
	}
	if now.After(n.ExpiresAt) {
		delete(s.nonces, claim.Nonce)
		return CallbackNonce{}, false
	}
	if !n.matches(claim) {
		return CallbackNonce{}, false
	}
	delete(s.nonces, claim.Nonce)
	return n, true
}

// matches compares every field CallbackClaim can actually carry.
// AllowedVerdict is not compared here — there is no live, wire-carried
// counterpart for it to be compared against. It is still returned to the
// caller on a match, read from the STORED record, never asserted by the
// claim.
func (n CallbackNonce) matches(claim CallbackClaim) bool {
	return n.RequestID == claim.RequestID &&
		n.BridgeInstance == claim.BridgeInstance &&
		n.PairedSubjectID == claim.PairedSubjectID &&
		n.ChatID == claim.ChatID &&
		n.MessageID == claim.MessageID
}
