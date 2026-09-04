// Package policy (approval_queue_errors.go): Purpose: the named refusals
//
//	the approval queue and its single-use ledger return. Every one of them
//	is a DENY: there is no error value in this file a caller may read as
//	"proceed", and no path in the queue that returns a usable result
//	alongside a non-nil error.
//
// Inputs: a format string naming which refusal fired and why.
// Outputs: the comparison-target sentinels (ErrLocalOnly, ErrNotAskTier,
//
//	ErrUnknownRequest, ErrTokenExpired, ErrTokenReplayed,
//	ErrApprovalMismatch, ErrApprovalNotApproved, ErrApprovalDecided,
//	ErrApprovalCanceled, ErrApprovalRungMismatch, ErrInvalidParamsHash,
//	ErrApprovalQueueFull) and *ApprovalError values carrying a detail
//	message.
//
// Constraints: the pkg/cascade taxonomy is FROZEN at fourteen kinds
//
//	(R-14.3), so none of these contract identifiers can be a Kind, and
//	cascade.Error's own Is compares KIND ALONE — which means six refusals
//	that all present as KindPolicyDenied would be indistinguishable to
//	errors.Is if they were bare taxonomy errors. That is not acceptable
//	here: a caller that cannot tell "expired" from "replayed" from
//	"the action changed" cannot tell the user what happened, and a test
//	that cannot tell them apart cannot prove the queue refused for the
//	right reason. So this file reuses errors.go's ClassifyError shape
//	(R-14.152): the stable identifier lives on the error as Code, Is
//	matches on Code, and Unwrap exposes a taxonomy error so
//	cascade.KindOf and errors.Is(err, cascade.ErrPolicyDenied) still work.
//
// SPORT: internal/policy ApprovalError/ADDED, ErrLocalOnly/ADDED,
//
//	ErrTokenExpired/ADDED, ErrTokenReplayed/ADDED,
//	ErrApprovalMismatch/ADDED (P1-E09-W2-S18-T3).
package policy

import "github.com/acamarata/cascade/pkg/cascade"

// The stable identifier strings. They appear in error messages and audit
// rows and must not change once shipped.
const (
	// CodeApprovalLocalOnly marks an action §5.14 reserves for the local
	// elevation helper: an elevation-class verb or a deny-list action.
	// Such an action is never queued, because queueing it would make a
	// remote-approvable prompt out of something that must be authorized
	// on the machine it runs on.
	CodeApprovalLocalOnly = "approval-local-only"
	// CodeApprovalNotAskTier marks an action whose rung is not L2 or L3.
	// L0/L1 need no approval and L4 is deny-list; neither belongs in a
	// queue whose only purpose is to ask.
	CodeApprovalNotAskTier = "approval-not-ask-tier"
	// CodeApprovalUnknownRequest marks a decision or a redemption naming
	// a request id the queue does not hold.
	CodeApprovalUnknownRequest = "approval-unknown-request"
	// CodeApprovalTokenExpired marks a token whose exp has passed.
	CodeApprovalTokenExpired = "approval-token-expired"
	// CodeApprovalTokenReplayed marks a nonce the single-use ledger
	// already holds.
	CodeApprovalTokenReplayed = "approval-token-replayed"
	// CodeApprovalMismatch marks a redemption whose action, parameters or
	// nonce differ from the ones the approval was issued against — the
	// mutated-request case.
	CodeApprovalMismatch = "approval-mismatch"
	// CodeApprovalNotApproved marks a redemption of an entry nobody
	// approved: still pending, or denied.
	CodeApprovalNotApproved = "approval-not-approved"
	// CodeApprovalDecided marks a second decision on an entry that was
	// already decided — the replayed-decision case.
	CodeApprovalDecided = "approval-already-decided"
	// CodeApprovalCanceled marks an entry the caller withdrew.
	CodeApprovalCanceled = "approval-canceled"
	// CodeApprovalRungMismatch marks an approval recorded against a lower
	// rung than the entry actually carries — the batch-laundering case.
	CodeApprovalRungMismatch = "approval-rung-mismatch"
	// CodeApprovalInvalidParamsHash marks a caller-supplied params digest
	// that is malformed or does not describe the parameters it came with.
	CodeApprovalInvalidParamsHash = "approval-invalid-params-hash"
	// CodeApprovalQueueFull marks an Enqueue refused because the pending
	// set is already at its ceiling.
	CodeApprovalQueueFull = "approval-queue-full"
)

// ApprovalError is an approval-queue refusal. Code is the stable
// identifier; Cause is the taxonomy error the refusal presents as.
type ApprovalError struct {
	// Code is one of this file's Code* constants.
	Code string
	// Cause is the taxonomy error this refusal presents as.
	Cause *cascade.Error
}

// Error renders the refusal. A sentinel with no detail still renders its
// code, so a bare sentinel is never an empty message.
func (e *ApprovalError) Error() string {
	if e == nil || e.Cause == nil {
		return CodeApprovalMismatch
	}
	return e.Cause.Error()
}

// Unwrap exposes the taxonomy error, so cascade.KindOf and
// errors.Is(err, cascade.ErrPolicyDenied) both work on a refusal.
func (e *ApprovalError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Is matches on Code alone. This is the whole point of the type: several
// of these refusals share a taxonomy Kind, and cascade.Error's own Is
// compares kinds, so without this a test for "expired" would pass on a
// "the action changed" refusal.
func (e *ApprovalError) Is(target error) bool {
	t, ok := target.(*ApprovalError)
	if !ok {
		return false
	}
	return e != nil && t != nil && e.Code == t.Code
}

// newApprovalSentinel builds one comparison target.
func newApprovalSentinel(kind cascade.Kind, code string) *ApprovalError {
	return &ApprovalError{Code: code, Cause: cascade.New(kind, code)}
}

// The comparison targets. Kinds are chosen so a caller that only knows the
// taxonomy still classifies each correctly: a refusal of the action itself
// is KindPolicyDenied, a malformed request is KindInvalidInput, an unknown
// id is KindNotFound, a second decision or a replay is KindConflict, an
// elevation-class action is KindElevationRequired, and a full queue is
// KindQuotaExhausted.
var (
	// ErrLocalOnly is returned for an elevation-class or deny-list action.
	ErrLocalOnly = newApprovalSentinel(cascade.KindElevationRequired, CodeApprovalLocalOnly)
	// ErrNotAskTier is returned for a rung outside L2/L3.
	ErrNotAskTier = newApprovalSentinel(cascade.KindInvalidInput, CodeApprovalNotAskTier)
	// ErrUnknownRequest is returned for an unrecognised request id.
	ErrUnknownRequest = newApprovalSentinel(cascade.KindNotFound, CodeApprovalUnknownRequest)
	// ErrTokenExpired is returned for a token past its exp.
	ErrTokenExpired = newApprovalSentinel(cascade.KindPolicyDenied, CodeApprovalTokenExpired)
	// ErrTokenReplayed is returned for a nonce already in the ledger.
	ErrTokenReplayed = newApprovalSentinel(cascade.KindConflict, CodeApprovalTokenReplayed)
	// ErrApprovalMismatch is returned when the redeemed action is not the
	// action the approval was issued against.
	ErrApprovalMismatch = newApprovalSentinel(cascade.KindPolicyDenied, CodeApprovalMismatch)
	// ErrApprovalNotApproved is returned for an entry nobody approved.
	ErrApprovalNotApproved = newApprovalSentinel(cascade.KindPolicyDenied, CodeApprovalNotApproved)
	// ErrApprovalDecided is returned for a repeated decision.
	ErrApprovalDecided = newApprovalSentinel(cascade.KindConflict, CodeApprovalDecided)
	// ErrApprovalCanceled is returned for a withdrawn entry.
	ErrApprovalCanceled = newApprovalSentinel(cascade.KindPolicyDenied, CodeApprovalCanceled)
	// ErrApprovalRungMismatch is returned when the rung an approver saw is
	// lower than the rung the entry carries.
	ErrApprovalRungMismatch = newApprovalSentinel(cascade.KindPolicyDenied, CodeApprovalRungMismatch)
	// ErrInvalidParamsHash is returned for a malformed or non-matching
	// caller-supplied params digest.
	ErrInvalidParamsHash = newApprovalSentinel(cascade.KindInvalidInput, CodeApprovalInvalidParamsHash)
	// ErrApprovalQueueFull is returned when the pending set is at its
	// ceiling.
	ErrApprovalQueueFull = newApprovalSentinel(cascade.KindQuotaExhausted, CodeApprovalQueueFull)
)

// refuse builds a detailed refusal against one of the sentinels above. The
// Code is taken from the sentinel rather than from a constant, so the value
// a caller compares against and the value this builds can never drift.
func refuse(sentinel *ApprovalError, format string, args ...any) *ApprovalError {
	return &ApprovalError{
		Code: sentinel.Code,
		Cause: cascade.Wrapf(sentinel.Cause.Kind, sentinel.Cause,
			"policy: "+sentinel.Code+": "+format, args...),
	}
}
