// Purpose: the bridge's DURABLE state seam. One pkg-facing interface
//   (BridgeState) carries every persisted bridge fact — the pairing
//   binding and its per-user allowlist, the outstanding pairing code's
//   DIGEST, the wrong-attempt counter R-16.37 locks out on, the long-poll
//   offset, and a bounded set of recently seen update ids — plus
//   UpdateLedger, the replay guard built on it.
//
// Inputs: a subject id (one per bridge instance: a Telegram bot token
//   digest, a WhatsApp number) and a BridgeState implementation the host
//   composition root supplies over the daemon's sqlite (internal/bridge).
//
// Outputs: SubjectState round-trips through Load/Save; UpdateLedger.Accept
//   answers "is this update_id new?" and advances the persisted offset in
//   the same write.
//
// Constraints, and why they are shaped this way:
//   - FAIL CLOSED WITH NO STORE. unconfiguredBridgeState refuses both
//     verbs, so a partially wired host binds nothing and dispatches
//     nothing. There is deliberately NO in-memory production
//     implementation: an in-process map made "pairing identity survives
//     restart" untestable and made a restart silently re-admit 24h of
//     Telegram's unconfirmed updates. The in-memory implementation lives
//     in _test.go as a fake only.
//   - THE CODE ITSELF IS NEVER PERSISTED, only a KEYED digest of its
//     canonical (upper-cased) form — HMAC-SHA256 under a key derived from the
//     bridge's own credential, which is never stored (see paircode_key.go).
//     Issuance and verification both happen in the DAEMON, which is what lets
//     the key stay in one process; the row is durable so a restart does not
//     forget an outstanding code or reset a lockout counter. Verification is
//     an exact match, so a digest loses nothing.
//   - SeenUpdateIDs is BOUNDED (MaxSeenUpdateIDs). Telegram redelivers
//     unconfirmed updates for 24h, so the window only has to cover a
//     restart's worth of redelivery, and an unbounded set would grow
//     forever in a row nothing prunes.
//
// SPORT: plugins/cascade-pa BridgeState/ADDED, SubjectState/ADDED,
//   UpdateLedger/ADDED (P1-E23-W5-S48-T1).

package cascadepa

import (
	"context"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// MaxSeenUpdateIDs bounds the replay window one subject remembers. 256 is
// far more than Telegram delivers in one getUpdates batch (100 is its own
// documented ceiling) and small enough to stay a cheap row.
const MaxSeenUpdateIDs = 256

// SubjectState is everything one bridge subject persists. It is a value
// type on purpose: Load hands back a copy, the caller mutates it, Save
// replaces the row — so no caller holds a live pointer into the store.
type SubjectState struct {
	// Subject is the bridge instance id this row belongs to.
	Subject string
	// TrustTier is the binding's tier, always trustTierPairedDevice once
	// bound and empty while unbound.
	TrustTier string
	// PairedAt is when the most recent Bind landed. Zero while unbound.
	PairedAt time.Time
	// AllowedFrom is the per-user allowlist (R-21.227): the sender ids
	// this bound subject admits. Empty means the subject is not bound.
	AllowedFrom []string
	// CodeDigest is the HMAC-SHA256 of the outstanding pairing code under
	// the key DerivePairCodeKey derives from the bridge credential (never
	// persisted), hex-encoded: a DB read alone verifies nothing. Empty
	// means no code is outstanding.
	CodeDigest string
	// CodeExpiresAt is the outstanding code's TTL boundary.
	CodeExpiresAt time.Time
	// WrongAttempts counts candidates refused against the outstanding
	// code. It survives a restart so the lockout cannot be reset by
	// bouncing the daemon.
	WrongAttempts int
	// Offset is the next getUpdates offset to request.
	Offset int64
	// SeenUpdateIDs is the bounded recently-processed window.
	SeenUpdateIDs []int64
	// Version is the host store's COMPARE-AND-SWAP token, opaque to this
	// package: Load hands back whatever the store recorded, the caller
	// carries it unchanged through its modification, and Save refuses the
	// write if the stored row has moved on since (ErrStateConflict). Zero
	// means "no row existed when I read". Nothing here computes it, and no
	// decision is ever made on its value.
	Version int64
}

// Bound reports whether this subject has a pairing binding at all.
func (s SubjectState) Bound() bool { return len(s.AllowedFrom) > 0 }

// Allows reports whether senderID is on the allowlist.
func (s SubjectState) Allows(senderID string) bool {
	for _, id := range s.AllowedFrom {
		if id == senderID {
			return true
		}
	}
	return false
}

// admit adds senderID to the allowlist if it is not already there.
func (s *SubjectState) admit(senderID string) {
	if s.Allows(senderID) {
		return
	}
	s.AllowedFrom = append(s.AllowedFrom, senderID)
}

// seen reports whether updateID is already in the replay window.
func (s SubjectState) seen(updateID int64) bool {
	for _, id := range s.SeenUpdateIDs {
		if id == updateID {
			return true
		}
	}
	return false
}

// remember appends updateID to the replay window, trimming the oldest
// entries so the window never exceeds MaxSeenUpdateIDs.
func (s *SubjectState) remember(updateID int64) {
	s.SeenUpdateIDs = append(s.SeenUpdateIDs, updateID)
	if over := len(s.SeenUpdateIDs) - MaxSeenUpdateIDs; over > 0 {
		s.SeenUpdateIDs = s.SeenUpdateIDs[over:]
	}
}

// BridgeState is the durable store every bridge adapter reads and writes
// its subject state through. The host composition root implements it over
// the daemon's cascade.db (internal/bridge); no implementation lives in
// this package, because a plugin package cannot open the daemon's store
// and an in-process one would be a lie about durability.
type BridgeState interface {
	// Load returns subject's persisted state. A subject with no row is
	// (zero, false, nil) — "nothing stored yet" is a valid state, never
	// an error.
	Load(ctx context.Context, subject string) (SubjectState, bool, error)
	// Save replaces subject's row atomically, under COMPARE-AND-SWAP on
	// state.Version: an implementation MUST refuse the write with
	// ErrStateConflict (or any KindConflict error carrying the same marker
	// — see IsStateConflict) when the stored row has changed since the
	// caller's Load, rather than overwriting it. A store that ignores
	// Version reverts whichever concurrent writer finished first, which
	// for this row means silently unpairing a bound bot.
	Save(ctx context.Context, state SubjectState) error
}

// ErrNoBridgeState is the refusal every verb returns when no host store is
// wired. It is exported so a host wiring test can assert the fail-closed
// default by identity rather than by message shape.
var ErrNoBridgeState = cascade.New(cascade.KindUnavailable,
	"cascade-pa: no bridge state store is wired; the bridge refuses to pair or dispatch")

// unconfiguredBridgeState is the fail-closed default: both verbs refuse,
// so an unwired host cannot bind a device or admit an update.
type unconfiguredBridgeState struct{}

func (unconfiguredBridgeState) Load(context.Context, string) (SubjectState, bool, error) {
	return SubjectState{}, false, ErrNoBridgeState
}

func (unconfiguredBridgeState) Save(context.Context, SubjectState) error { return ErrNoBridgeState }

// orUnconfigured substitutes the fail-closed default for a nil store, so
// every constructor in this package can accept nil without any of them
// having to decide what nil means.
func orUnconfigured(state BridgeState) BridgeState {
	if state == nil {
		return unconfiguredBridgeState{}
	}
	return state
}

// UpdateLedger is the durable replay guard: it answers whether an inbound
// update id has already been processed and advances the persisted
// long-poll offset in the same write.
//
// Both facts live in one row and are written together on purpose. An
// offset advanced without recording the id would re-admit that id after a
// restart (Telegram redelivers everything unacknowledged), and an id
// recorded without advancing the offset would refetch it forever.
type UpdateLedger struct {
	state BridgeState
	// locks is the per-subject mutex table. NewStores replaces it with the
	// one every other store in the bundle shares, so Accept cannot
	// interleave with IssueCode or Bind on the same subject.
	locks *subjectLocks
}

// NewUpdateLedger builds a ledger over state (nil -> fail-closed).
func NewUpdateLedger(state BridgeState) *UpdateLedger {
	return &UpdateLedger{state: orUnconfigured(state), locks: newSubjectLocks()}
}

// Offset returns the next getUpdates offset for subject, 0 for a subject
// that has never polled.
func (l *UpdateLedger) Offset(ctx context.Context, subject string) (int64, error) {
	unlock := l.locks.lock(subject)
	defer unlock()
	st, _, err := l.state.Load(ctx, subject)
	if err != nil {
		return 0, err
	}
	return st.Offset, nil
}

// Accept records updateID as processed and reports whether it is NEW.
// A duplicate or replayed id returns false and writes nothing, so the
// caller never runs a handler twice for one Telegram update.
func (l *UpdateLedger) Accept(ctx context.Context, subject string, updateID int64) (bool, error) {
	return mutate(l.locks, subject, func() (bool, error) { return l.acceptOnce(ctx, subject, updateID) })
}

// acceptOnce is Accept's read-modify-write, run under the subject lock and
// retried once by mutate if the durable store refuses a stale write. It
// re-Loads on every attempt, which is what makes the retry correct rather
// than a second push of the same stale row.
func (l *UpdateLedger) acceptOnce(ctx context.Context, subject string, updateID int64) (bool, error) {
	st, _, err := l.state.Load(ctx, subject)
	if err != nil {
		return false, err
	}
	st.Subject = subject
	if st.seen(updateID) {
		return false, nil
	}
	st.remember(updateID)
	if updateID >= st.Offset {
		st.Offset = updateID + 1
	}
	if err := l.state.Save(ctx, st); err != nil {
		return false, err
	}
	return true, nil
}
