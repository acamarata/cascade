// Package policy (approval_queue_types.go): Purpose: the value vocabulary
//
//	of the approval queue — the state a queued action can be in, the
//	daemon-memory token, the three seams the queue is built over
//	(TokenMinter, DenyLister, ApprovalRecorder), and the three-field
//	payload that is the ONLY shape any caller ever receives.
//
// Inputs: none; this file is declarations plus total functions over its
//
//	own enum.
//
// Outputs: ApprovalState, ApprovalToken, ApprovalMintRequest, TokenMinter,
//
//	DenyLister, ApprovalRecorder, PendingEntry.
//
// Constraints: the enum is FAIL CLOSED on the same terms as RiskLevel and
//
//	Verdict — its iota starts at 1, so an entry whose state was never set
//	does not read as approved. PendingEntry is the §5.24 remote-approvable
//	payload and carries three fields: adding a fourth is a security change,
//	not a convenience, because everything this struct holds can cross a
//	bridge. The rung an approver saw therefore lives INSIDE Summary rather
//	than beside it, so the string displayed to a human is the same string a
//	decision is checked against.
//
// SPORT: internal/policy ApprovalState/ADDED, ApprovalToken/ADDED,
//
//	TokenMinter/ADDED, PendingEntry/ADDED (P1-E09-W2-S18-T3).
package policy

import (
	"context"
	"time"

	"github.com/acamarata/cascade/internal/audit"
)

// ApprovalState is where one queued action stands. The zero value is
// deliberately not a member, so an entry whose state was never set does not
// read as approved.
type ApprovalState uint8

// The states, in the order an entry can reach them.
const (
	_ ApprovalState = iota // 0 is deliberately not a valid state

	// ApprovalPending is awaiting a human decision.
	ApprovalPending
	// ApprovalApproved has a recorded yes and an unredeemed token.
	ApprovalApproved
	// ApprovalDenied has a recorded no. It is terminal.
	ApprovalDenied
	// ApprovalConsumed has had its token redeemed. It is terminal, and it
	// is what makes a second redemption a replay.
	ApprovalConsumed
	// ApprovalExpired passed its exp before it was redeemed. It is
	// terminal: the queue does NOT re-ask.
	ApprovalExpired
	// ApprovalCanceled was withdrawn by the caller. It is terminal.
	ApprovalCanceled
)

// approvalStateNames holds each state's stable name, indexed by value.
var approvalStateNames = [...]string{
	"", "pending", "approved", "denied", "consumed", "expired", "canceled",
}

// String returns the state's stable name, e.g. "approved".
func (s ApprovalState) String() string {
	if s < ApprovalPending || s > ApprovalCanceled {
		return "invalid-approval-state"
	}
	return approvalStateNames[s]
}

// ApprovalToken is the daemon-memory proof that one specific action was
// queued for approval. It never leaves the daemon: GetPending returns a
// PendingEntry, which carries none of these fields.
type ApprovalToken struct {
	// RequestID identifies the queued action. It is the ONLY field a
	// bridge may carry (§5.24).
	RequestID string `json:"request_id"`
	// Nonce is the single-use value the ledger records at redemption.
	Nonce string `json:"nonce"`
	// ActionHash is the digest of the action string.
	ActionHash string `json:"action_hash"`
	// ParamsHash is the digest of the action's parameters.
	ParamsHash string `json:"params_hash"`
	// Issued is when the token was minted.
	Issued time.Time `json:"issued"`
	// Expires is when it stops being redeemable, never later than
	// Issued.Add(MaxApprovalTTL).
	Expires time.Time `json:"exp"`
}

// ApprovalMintRequest is what a TokenMinter is asked to mint.
type ApprovalMintRequest struct {
	// Subject is the principal the approval will be issued to.
	Subject Subject
	// ActionHash and ParamsHash are the digests the token binds to.
	ActionHash string
	ParamsHash string
	// Issued and Expires are the already-clamped lifetime bounds.
	Issued  time.Time
	Expires time.Time
}

// TokenMinter mints an approval token. It is the seam H/S-16.T3's
// Ed25519-signing minter drops into: this queue never signs anything
// itself, it only asks for a token and binds it to the digests above.
type TokenMinter interface {
	// Mint returns a token for req. It must produce a fresh, unguessable
	// RequestID and Nonce on every call.
	Mint(ctx context.Context, req ApprovalMintRequest) (ApprovalToken, error)
}

// DenyLister answers whether an action is on the deny-list. It is the seam
// S-17.T4's deny-list engine drops into; the queue refuses a deny-listed
// action at Enqueue in ADDITION to whatever the engine does upstream.
type DenyLister interface {
	// Denied reports whether action is deny-listed. An error denies.
	Denied(ctx context.Context, action string) (bool, error)
}

// ApprovalRecorder is the audit sink the queue writes approval.enqueue,
// approval.dedup, approval.expire, approval.grant and approval.deny rows
// to. *audit.Log satisfies it directly; the dependency direction is policy
// to audit and never back.
type ApprovalRecorder interface {
	// Append writes one audit event.
	Append(ctx context.Context, event audit.Event) (audit.Record, error)
}

// PendingEntry is the ONLY shape the queue hands any caller, bridge paths
// included (§5.24). It carries three fields and there is no method on it
// that reaches the rest of the entry: the token, the nonce and the action
// hash stay in daemon memory, and a bridge carries the request id alone.
//
// The rung the approver saw is inside Summary, not a fourth field, so the
// string a surface displays IS the thing a decision is checked against.
type PendingEntry struct {
	// RequestID identifies the queued action.
	RequestID string `json:"request_id"`
	// Summary is the human-readable description, with the rung appended.
	Summary string `json:"action_summary"`
	// ExpiresAt is when the approval stops being redeemable.
	ExpiresAt time.Time `json:"exp"`
}
