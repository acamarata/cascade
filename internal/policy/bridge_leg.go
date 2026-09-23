// Package policy (bridge_leg.go): Purpose: the §5.24 PRODUCER leg — the
//
//	code that first sends an approval reference off-machine. It is the one
//	production caller of approval_token.go's BridgePayload: for one pending
//	entry, RemoteApprovabilityMatrix.CanBridge is checked BEFORE anything is
//	projected or handed to a sender, and on a pass exactly one BridgeRef
//	(request_id, nothing else) reaches a registered BridgeSender.
//
// Inputs: a PendingEntry (the SAME §5.24-safe projection GetPending
//
//	returns) and a BridgeSender the composition root registers.
//
// Outputs: BridgeLeg, BridgeSender, BridgeNotifier, ErrNotBridgeable.
// Constraints: FAIL CLOSED. A non-bridgeable class, an unknown class and the
//
//	invalid zero class all refuse identically — CanBridge is the single
//	authority (R-16.60c) and this file adds no classifier of its own. The
//	sender is never called on a refusal: "checked before anything is
//	projected or handed out" is the ordering the D4 ruling requires, not
//	advisory. Nothing but a BridgeRef ever crosses Dispatch's boundary: no
//	summary, no expiry, no action hash, no params (§5.24).
//
//	QUEUE-ID VS RECORD-ID (a declared, honest type seam): PendingEntry's
//	RequestID is the approval queue's own identifier space (32 lower-case
//	hex characters, approval_queue_enqueue.go's randomID) while
//	ApprovalRecord.RequestID is typed cascade.ID (26-character Crockford
//	base32, approval_token.go's H/S-16.T3 signing path — a subsystem with
//	no other production caller yet, per telegram_approval_wiring.go's own
//	header). BridgePayload only ever reads .RequestID.String() back out, so
//	this file converts with a plain string-to-ID cast rather than
//	cascade.ParseID: the queue's id is this package's OWN already-trusted
//	value (it just came out of GetPending/Enqueue), not untrusted bytes
//	arriving from outside the process, and ParseID's Crockford-alphabet
//	check would incorrectly refuse a syntactically fine hex id that is
//	simply spelled in a different, equally internal, alphabet.
//
// SPORT: internal/policy BridgeSender/ADDED, BridgeNotifier/ADDED,
//
//	BridgeLeg/ADDED, ErrNotBridgeable/ADDED (P1-E23-W5-S48-T4, T0 D4).
package policy

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// BridgeSender delivers one approval's bridge-safe reference to whatever
// remote surface it is registered for (Telegram, ...). Send receives ONLY a
// BridgeRef — this is the one signature internal/policy ever hands anything
// across a process boundary through, and it is exactly what BridgePayload
// projects: a request id and nothing else.
type BridgeSender interface {
	// Send delivers ref. An implementation must not add fields, must not
	// log the ref's contents anywhere a token or digest could later be
	// confused for, and a failure must be a plain error — never a panic.
	Send(ctx context.Context, ref BridgeRef) error
}

// BridgeNotifier is notified once per freshly admitted pending entry. It is
// the seam ApprovalQueueConfig.Bridge holds (approval_queue.go) and the
// interface *BridgeLeg satisfies, kept separate from BridgeSender so the
// queue never has to know a bridge sender's own shape — only that
// SOMETHING answers "dispatch this entry". verb is the entry's own
// capability name (approvalEntry.capability): CanBridgeVerb needs it
// because the class test alone cannot see that a verb is §5.14
// elevation-class and therefore local-only regardless of its class
// (approval_matrix.go, R-21.230 FLAG-1).
type BridgeNotifier interface {
	Dispatch(ctx context.Context, verb string, entry PendingEntry) error
}

// ErrNotBridgeable is returned for an entry whose class does not clear
// RemoteApprovabilityMatrix.CanBridge (§5.24): a non-bridgeable class, an
// unknown class and the invalid zero class all produce this identical
// refusal, fail-closed, before BridgePayload ever runs.
var ErrNotBridgeable = cascade.New(cascade.KindPermissionDenied,
	"policy: this action's class may not be decided across a bridge")

// BridgeLeg is the producer half of §5.24's bridge approval flow.
type BridgeLeg struct {
	matrix RemoteApprovabilityMatrix
	sender BridgeSender
}

// compile-time proof BridgeLeg satisfies the hook the queue calls.
var _ BridgeNotifier = (*BridgeLeg)(nil)

// NewBridgeLeg builds a leg over sender. A nil sender is refused at
// construction rather than causing every later Dispatch to fail for a
// reason that looks like a bridging refusal.
func NewBridgeLeg(sender BridgeSender) (*BridgeLeg, error) {
	if sender == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "policy: bridge leg requires a sender")
	}
	return &BridgeLeg{sender: sender}, nil
}

// Dispatch is the whole leg: CanBridgeVerb first, fail closed; on a pass,
// project entry to exactly one BridgeRef and call Send exactly once.
//
// verb is checked ALONGSIDE entry.ActionClass, not instead of it:
// CanBridgeVerb refuses a §5.14 elevation-class verb (e.g. "policy.set")
// even when its class is otherwise bridgeable, because the class test
// alone cannot see that a verb is local-only (approval_matrix.go). A
// non-bridgeable class, an unknown class, and an elevation-class verb all
// return ErrNotBridgeable and the sender is NEVER called — no nonce is
// minted, no message is sent, nothing is projected. This is the ordering
// the D4 ruling names as load-bearing, not incidental: "checked BEFORE
// anything is projected or handed out".
func (b *BridgeLeg) Dispatch(ctx context.Context, verb string, entry PendingEntry) error {
	if b == nil || b.sender == nil {
		return cascade.New(cascade.KindUnavailable, "policy: no bridge sender is wired")
	}
	if !b.matrix.CanBridgeVerb(verb, entry.ActionClass) {
		return ErrNotBridgeable
	}
	ref := BridgePayload(ApprovalRecord{RequestID: cascade.ID(entry.RequestID)})
	return b.sender.Send(ctx, ref)
}

// notifyBridge is Enqueue's (approval_queue_enqueue.go) one-line hook into
// this leg — the queue's own "pending-entry event". A DEDUPLICATED
// admission is not notified again: the original admission already tried,
// and a coalesced caller asking a second time must not re-mint a second
// set of buttons for the same request id.
//
// The result is deliberately discarded: ErrNotBridgeable is the expected,
// non-error outcome for most queued actions (askTier admits L2/L3 only,
// and ClassWorkspaceMutation is the sole ask-tier class on the bridge
// allow-list), and a transport failure must never fail admission — the
// entry is still validly queued and redeemable through the CLI whether or
// not a remote surface heard about it.
func (q *StoreApprovals) notifyBridge(ctx context.Context, res EnqueueResult, entry approvalEntry) {
	if q.cfg.Bridge == nil || res.Deduplicated {
		return
	}
	_ = q.cfg.Bridge.Dispatch(ctx, entry.capability, PendingEntry{
		RequestID: entry.requestID, Summary: entry.summary, ExpiresAt: entry.expires,
		ActionClass: q.classOf(ctx, entry.capability),
	})
}
