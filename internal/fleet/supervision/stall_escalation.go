package supervision

// Purpose (this file): the two escalation-ladder seams this ticket's own
// full_desc assigns to it — "(3) supervisor-task escalation (enqueue
// StallEvent to the S-39.T1 attention queue), (4) human escalation
// (inject an approval request into the I/S-18.T3 approval queue)" — as
// governor.SupervisorCreator and governor.HumanNotifier implementations.
// Retryer and ContextEnricher (rungs 1 and 2) are NOT assigned to this
// ticket by its full_desc (they name S-26.T2 and the retrieval/context
// package respectively, neither in this ticket's depends_on); Detector's
// constructor (stall.go) takes them as injected interfaces, satisfied at
// the daemon composition root, exactly as governor's own seam docs
// already say ("Satisfied at runtime by ...", "Satisfied at composition
// root by ...").
//
// CONTRADICTION (the human seam does not import internal/policy
// directly). internal/policy.ApprovalQueue.Enqueue requires a Subject, a
// registered Capability, and a RiskLevel (L2 or L3) — three fields a
// stall escalation has no natural, contract-given value for: a stall is
// not an ask-tier ACTION with a capability and a principal, it is a
// session that stopped making progress. Inventing a capability name and
// a risk level for this here would be a policy decision this ticket's
// contract never makes, not a wiring detail. ApprovalRequester below is
// a narrower, package-local seam instead — the composition root adapts
// the real *policy.StoreApprovals into this shape once the ask-tier
// framing for a stall event is decided, exactly as the ticket's own
// full_desc already frames every seam here ("consumed here as
// interfaces").
//
// SPORT: fleet.supervision.stall/ADDED (P1-E18-W4-S39-T5).

import (
	"context"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/fleet/governor"
)

// ApprovalRequester is the minimal seam the human-escalation step needs
// from the I/S-18.T3 approval queue: file one approval request for a
// stalled session and report its request id. See this file's
// CONTRADICTION above for why this is not internal/policy.ApprovalQueue
// itself.
type ApprovalRequester interface {
	// RequestApproval files an approval request describing event for
	// sessionID, and reports the request id the approval surface will
	// use to answer it.
	RequestApproval(ctx context.Context, sessionID string, event StallEvent) (requestID string, err error)
}

// stallLookup resolves the most recent StallEvent this detector recorded
// for a session, for stallSupervisor/stallNotifier to describe what they
// are escalating. A miss (nil StallEvent) means the ladder is advancing
// past the current rung independent of THIS ticket's own detection —
// e.g. an attempt-budget advance — so both seams below fall back to a
// StallKindUnknown event rather than fabricating detail they do not have.
type stallLookup func(sessionID string) (StallEvent, bool)

// stallSupervisor implements governor.SupervisorCreator by pushing a
// KindStall AttentionItem into the existing S-39.T1 Store. Store.Push is
// idempotent on (Kind, SourceRef) (attention_store.go), which is what
// makes repeated arrivals at RungSupervisorTask for the same session a
// no-op rather than a duplicate queue entry — this ticket's own
// idempotence requirement, discharged by the collaborator it reuses
// rather than reimplemented here.
type stallSupervisor struct {
	store  *Store
	lookup stallLookup
}

var _ governor.SupervisorCreator = (*stallSupervisor)(nil)

// CreateSupervisor implements governor.SupervisorCreator.
func (s *stallSupervisor) CreateSupervisor(ctx context.Context, entityID string) (string, error) {
	item := AttentionItem{
		Kind:      KindStall,
		SourceRef: entityID,
		ScopeRef:  scope.Ref{Kind: scope.ScopeKindSession, ID: entityID},
	}
	pushed, err := s.store.Push(ctx, item)
	if err != nil {
		return "", err
	}
	return pushed.ID, nil
}

// stallNotifier implements governor.HumanNotifier by filing an approval
// request through the injected ApprovalRequester.
type stallNotifier struct {
	requester ApprovalRequester
	lookup    stallLookup
}

var _ governor.HumanNotifier = (*stallNotifier)(nil)

// Notify implements governor.HumanNotifier.
func (n *stallNotifier) Notify(ctx context.Context, event governor.EscalationEvent) error {
	stallEvent, ok := n.lookup(event.EntityID)
	if !ok {
		stallEvent = StallEvent{SessionID: event.EntityID, StallKind: StallKindUnknown}
	}
	_, err := n.requester.RequestApproval(ctx, event.EntityID, stallEvent)
	return err
}
