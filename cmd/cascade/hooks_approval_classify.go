package main

// Purpose: decide what a stored completion-approval row means after its
//
//	completion was refused (P1-PBD-09): still pending, denied by a human,
//	approved and ready for the owner's one redemption, or stale and due
//	for a fresh request.
//
// Inputs: the stored row, the queue entry's EntryView state and the
//
//	ConsumeToken refusal, matched by sentinel identity.
//
// Outputs: storedClass; the row is marked denied or deleted as a side
//
//	effect.
//
// Constraints: never reads the refusal's reason text; never refiles a row
//
//	marked denied; never puts the row's nonce into an error.

import (
	"context"
	"errors"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/pkg/cascade"
)

// completionApprovalStore is the slice of the jobs row store the
// completion gate classifies against.
type completionApprovalStore interface {
	CompletionApprovalFor(ctx context.Context, jobID, action string) (jobs.CompletionApproval, bool, error)
	MarkCompletionApprovalDenied(ctx context.Context, jobID, action string) error
	DeleteCompletionApproval(ctx context.Context, jobID, action string) error
}

// storedClass is what a stored completion request means now.
type storedClass uint8

const (
	_ storedClass = iota // the zero value is never a verdict
	// storedPending waits on a human decision.
	storedPending
	// storedDenied was refused; the row stays and is never refiled.
	storedDenied
	// storedApproved holds a recorded yes for the owner to redeem.
	storedApproved
	// storedRefile was deleted; the caller files a fresh request.
	storedRefile
)

// String names the class for tests and logs.
func (c storedClass) String() string {
	switch c {
	case storedPending:
		return "pending"
	case storedDenied:
		return "denied"
	case storedApproved:
		return "approved"
	case storedRefile:
		return "refile"
	}
	return "invalid"
}

// staleRefusal reports whether a ConsumeToken refusal says the entry can
// never be redeemed: gone, lapsed or already spent.
func staleRefusal(err error) bool {
	return err != nil && (errors.Is(err, policy.ErrUnknownRequest) ||
		errors.Is(err, policy.ErrTokenExpired) || errors.Is(err, policy.ErrTokenReplayed))
}

// classifyStored classifies row, whose queue entry is requestID, after
// ConsumeToken refused it with consumeErr (nil when it was not tried).
// A denied row stays denied whatever the queue says. An entry the queue no
// longer knows, a consumed, expired or canceled entry, or a stale refusal
// deletes the row exactly once and returns storedRefile.
func classifyStored(ctx context.Context, store completionApprovalStore, entries policy.ApprovalEntrySource,
	row jobs.CompletionApproval, requestID string, consumeErr error) (storedClass, error) {
	if store == nil || entries == nil {
		return 0, cascade.New(cascade.KindUnavailable, "completion approval: store or queue not wired")
	}
	if row.Denied {
		return storedDenied, nil
	}
	view, err := entries.EntryView(ctx, requestID)
	switch {
	case errors.Is(err, policy.ErrUnknownRequest):
		return refileStored(ctx, store, row)
	case err != nil:
		return 0, err
	case view.State == policy.ApprovalDenied:
		if err := store.MarkCompletionApprovalDenied(ctx, row.JobID, row.Action); err != nil {
			return 0, err
		}
		return storedDenied, nil
	case staleRefusal(consumeErr):
		return refileStored(ctx, store, row)
	}
	switch view.State {
	case policy.ApprovalPending, policy.ApprovalConsuming:
		return storedPending, nil // a redemption in flight is never refiled
	case policy.ApprovalApproved:
		return storedApproved, nil
	case policy.ApprovalConsumed, policy.ApprovalExpired, policy.ApprovalCanceled:
		return refileStored(ctx, store, row)
	case policy.ApprovalDenied:
		// Handled before this switch; reaching here is an integrity fault.
	}
	return 0, cascade.New(cascade.KindIntegrity, "completion approval: queue entry is in no valid state")
}

// refileStored deletes row once so the caller can file a fresh request.
func refileStored(ctx context.Context, store completionApprovalStore, row jobs.CompletionApproval) (storedClass, error) {
	if err := store.DeleteCompletionApproval(ctx, row.JobID, row.Action); err != nil {
		return 0, err
	}
	return storedRefile, nil
}
