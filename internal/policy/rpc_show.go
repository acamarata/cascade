// Purpose: approval.show — one entry's bridge-safe fields and, when the
//
//	entry source is wired, its state, so an operator and the entry's owner
//	can see approved versus consumed. Moved from rpc.go under Art.10.3's
//	300-line cap.
//
// Inputs: a request id.
// Outputs: approvalShow, ApprovalShowParams, ApprovalShowResult.
// Constraints: the result carries exactly request_id, action_summary, exp,
//
//	action_class and state: no nonce, token, signed record or digest. An
//	unknown id is KindNotFound; any other read error is returned as it is,
//	never read as not found.
//
// SPORT: internal/policy rpc-handlers/ADDED (P1-E09-W2-S18-T6), approval.show state/CHANGED (P1-SEC-41).

package policy

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ApprovalShowParams names one queued entry.
type ApprovalShowParams struct {
	// RequestID is the entry's identifier.
	RequestID string `json:"request_id"`
}

// ApprovalShowResult is one retained entry: the PendingEntry fields plus
// the entry's state (pending, approved, consuming, consumed, denied,
// expired or canceled).
type ApprovalShowResult struct {
	PendingEntry
	// State is the entry's state now.
	State string `json:"state"`
}

// approvalShow returns one entry. An id that names nothing is
// KindNotFound; an empty id is KindInvalidInput, because an absent id is a
// malformed request rather than a miss. Without an entry source only a
// pending entry is found.
func (d RPCDeps) approvalShow(ctx context.Context, params json.RawMessage) (any, error) {
	var p ApprovalShowParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if p.RequestID == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "policy: approval.show requires a request_id")
	}
	if d.Entries != nil {
		return d.showEntry(ctx, p.RequestID)
	}
	queue, err := d.requireQueue()
	if err != nil {
		return nil, err
	}
	pending, err := queue.GetPending(ctx)
	if err != nil {
		return nil, err
	}
	for _, entry := range pending {
		if entry.RequestID == p.RequestID {
			return entry, nil
		}
	}
	return nil, cascade.Newf(cascade.KindNotFound,
		"policy: no pending approval has request id %q", sanitize(p.RequestID))
}

// showEntry reads any retained entry through the entry source.
func (d RPCDeps) showEntry(ctx context.Context, requestID string) (any, error) {
	view, err := d.Entries.EntryView(ctx, requestID)
	if errors.Is(err, ErrUnknownRequest) {
		return nil, cascade.Newf(cascade.KindNotFound,
			"policy: no approval has request id %q", sanitize(requestID))
	}
	if err != nil {
		return nil, err
	}
	return ApprovalShowResult{
		PendingEntry: PendingEntry{RequestID: view.RequestID, Summary: view.Summary, ExpiresAt: view.ExpiresAt,
			ActionClass: d.classOf(ctx, view.Capability)},
		State: view.State.String(),
	}, nil
}

// classOf resolves a capability's action class through the registry, as
// GetPending does: an unresolvable capability reads as the invalid zero
// class, which a bridge already refuses.
func (d RPCDeps) classOf(ctx context.Context, capability string) ActionClass {
	if d.Registry == nil {
		return ActionClass(0)
	}
	reg, err := d.Registry.Lookup(ctx, capability)
	if err != nil {
		return ActionClass(0)
	}
	return reg.Class()
}
