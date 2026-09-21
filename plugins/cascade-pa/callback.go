// Purpose: the R-21.210 server-side one-use callback nonce — minted beside
//   an outbound approval prompt, consumed exactly once by W/S-48.T4 before
//   any token is redeemed.
//
// Inputs: the eight binding fields R-21.210 enumerates: {request_id,
//   action digest, bridge instance, paired subject id, chat id, message id,
//   expiry, allowed verdict}.
//
// Outputs: Store records a nonce; Consume deletes it and reports whether
//   every one of the eight bound facts matched.
//
// Constraints: ALL EIGHT FIELDS ARE COMPARED, the verdict included. The
//   earlier draft bound seven and never compared AllowedVerdict, so a
//   nonce minted for a deny-only prompt was consumed ok=true by a callback
//   claiming approval — the exact confusion a verdict binding exists to
//   stop. Consume therefore takes the claimed verdict as an argument.
//   A nonce is spent by being CHECKED, not by passing: the record is
//   deleted before any comparison runs, so a wrong-field retry cannot be
//   ground down field by field.
//
// SPORT: plugins/cascade-pa CallbackNonce/ADDED, CallbackNonceStore/ADDED
//   (P1-E23-W5-S48-T1).

package cascadepa

import (
	"sync"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// CallbackNonce is one outbound approval prompt's server-side binding.
type CallbackNonce struct {
	Nonce           string
	RequestID       string
	ActionDigest    string
	BridgeInstance  string
	PairedSubjectID string
	ChatID          string
	MessageID       string
	ExpiresAt       time.Time
	AllowedVerdict  bool
}

// CallbackClaim is what a callback asserts about itself. It exists as a
// struct rather than eight positional arguments because Consume compares
// every field: a positional call would let two same-typed ids be swapped
// silently, and the swap would read as a legitimate refusal.
type CallbackClaim struct {
	Nonce           string
	RequestID       string
	ActionDigest    string
	BridgeInstance  string
	PairedSubjectID string
	ChatID          string
	MessageID       string
	Verdict         bool
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
// record existed, was unexpired at now, and matched every one of the eight
// R-21.210 fields — the allowed verdict included. A caller must treat any
// false as "refuse the callback" and must never tell the sender which
// field disagreed.
func (s *CallbackNonceStore) Consume(claim CallbackClaim, now time.Time) (CallbackNonce, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.nonces[claim.Nonce]
	if !ok {
		return CallbackNonce{}, false
	}
	delete(s.nonces, claim.Nonce)
	if now.After(n.ExpiresAt) {
		return CallbackNonce{}, false
	}
	if !n.matches(claim) {
		return CallbackNonce{}, false
	}
	return n, true
}

// matches compares every bound field. AllowedVerdict is compared like any
// other: a nonce minted for verdict=false authorises no approval.
func (n CallbackNonce) matches(claim CallbackClaim) bool {
	return n.RequestID == claim.RequestID &&
		n.ActionDigest == claim.ActionDigest &&
		n.BridgeInstance == claim.BridgeInstance &&
		n.PairedSubjectID == claim.PairedSubjectID &&
		n.ChatID == claim.ChatID &&
		n.MessageID == claim.MessageID &&
		n.AllowedVerdict == claim.Verdict
}
