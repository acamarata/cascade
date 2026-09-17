package nodes

import "context"

// Purpose (this file): remote-dispatch failure semantics — deciding what
//
//	happens to work that was shipped to a node the controller then lost.
//
// THE THREE LOSS SIGNALS, AND WHY THEY ARE READ FAIL-CLOSED.
//
//	Liveness is three-state (R-21.225): reachable, unavailable, unknown.
//	Only `reachable` is evidence the node is there. Both of the others —
//	including "we have no idea" — are re-queue signals, because the
//	alternative is a controller that assumes health from an absence of bad
//	news and leaves work parked on a machine that is gone. The tunnel state
//	and the ship leg's own typed errors are read the same way.
//
// WHAT RE-QUEUE IS NOT. It is not a retry loop and it is not a bypass. The
//
//	replacement target goes back through the SAME placement engine with the
//	SAME requirement, so trust tier, sensitivity, liveness, connection and
//	drain are all re-applied. Recovery is the case where bypassing a filter
//	is most tempting and least defensible: the work is already late, and the
//	filter that would be skipped is the one that says this machine must not
//	see this work.
//
// AND IT IS FENCED. The replacement takes the NEXT attempt number from the
//
//	same register the original used, so it runs on its own branch and
//	worktree, and anything still arriving from the superseded attempt is
//	refused with ErrStaleAttempt (R-21.221). A partitioned-but-alive node
//	cannot race its own replacement.
//
// Inputs: the loss observation, the original requirement, the candidates.
// Outputs: a plan — re-queue onto a named node at a named attempt, or hold.
// SPORT: internal/nodes:requeue (ADD) — P1-E17-W4-S37-T3.

// LossSignal names what told the controller a dispatch may not finish.
type LossSignal string

const (
	// LossHeartbeat is liveness that is not reachable — either an explicit
	// negative or no recent evidence at all.
	LossHeartbeat LossSignal = "heartbeat"
	// LossTunnel is a transport that is not up.
	LossTunnel LossSignal = "tunnel"
	// LossChannel is a typed failure from the ship leg itself.
	LossChannel LossSignal = "channel"
)

// Disposition is what a lost dispatch's work does next.
type Disposition string

const (
	// DispositionRequeue means the work goes to another eligible node.
	DispositionRequeue Disposition = "requeue"
	// DispositionHold means nobody may decide this automatically.
	DispositionHold Disposition = "hold"
)

// LossObservation is what the controller saw about a node mid-dispatch.
type LossObservation struct {
	// Liveness is the node's three-state liveness at the observation.
	Liveness Liveness
	// Tunnel is the transport state at the observation.
	Tunnel TunnelState
	// ChannelErr is the ship leg's own error, if it produced one.
	ChannelErr error
}

// DetectLoss reports whether obs is a loss, and which signal it is.
//
// The signals are checked in the order an operator would want named: a
// channel error is the most specific thing the controller actually saw, the
// tunnel is the carrier, and liveness is the broadest. A node can fail all
// three at once and the most specific one is the useful answer.
func DetectLoss(obs LossObservation) (LossSignal, bool) {
	switch {
	case obs.ChannelErr != nil:
		return LossChannel, true
	case obs.Tunnel != TunnelUp:
		return LossTunnel, true
	case obs.Liveness != LivenessReachable:
		// Not `== LivenessUnavailable`: unknown is a loss too. Treating
		// "no evidence" as health is the failure this three-state type
		// exists to make impossible to write by accident.
		return LossHeartbeat, true
	}
	return "", false
}

// RequeueRequest is one lost dispatch, and everything needed to place its
// work again.
type RequeueRequest struct {
	// DispatchID names the dispatch being recovered.
	DispatchID string
	// LostNodeID is the node the work was on. It is excluded from the
	// replacement candidates explicitly rather than trusted to fail
	// placement: the device record's liveness is updated by a different
	// loop on a different schedule, and a recovery that placed work back
	// on the machine it just left would loop.
	LostNodeID string
	// Action is the work, carrying the idempotence declaration that
	// decides whether this can be automatic at all.
	Action Action
	// Requirement is the ORIGINAL placement demand, re-applied unchanged.
	Requirement Requirement
	// EntityID is the journal entity whose records carry the continuity.
	EntityID string
}

// RequeueDeps are the collaborators the decision needs.
type RequeueDeps struct {
	// Placement re-decides eligibility. A zero Engine places nothing,
	// which is the safe failure for an unwired composition root.
	Placement Engine
	// Candidates are the enrolled nodes to choose from.
	Candidates []Candidate
	// Attempts is the same fencing register the original attempt used.
	Attempts *AttemptRegister
	// Continuity reads what the lost attempt already recorded.
	Continuity JournalContinuityReader
	// Attention receives a held outcome. Required only when one is held.
	Attention AttentionFiler
	// Liveness reports the three-state liveness for a node id. Nil reads
	// as LivenessUnknown — the fail-closed value — so a composition root
	// that forgot to wire it treats every ship failure as a possible loss
	// rather than as proof of health.
	Liveness func(nodeID string) Liveness
}

// RequeuePlan is the decision.
type RequeuePlan struct {
	// Disposition is what happens to the work.
	Disposition Disposition
	// Signal is the loss that triggered this.
	Signal LossSignal
	// Node is the replacement target. Zero when held.
	Node DeviceRecord
	// Attempt is the replacement's fencing number. Zero when held.
	Attempt uint64
	// Branch is where the replacement attempt ships.
	Branch string
	// Resume is where the replacement picks up.
	Resume ResumePoint
}

// PlanRequeue decides what a lost dispatch's work does next.
//
// A non-idempotent action is held BEFORE placement is consulted. Placing
// work that must not be re-run is wasted at best, and at worst it is the
// shape of a bug where a later edit drops the idempotence check and the
// placement result is already sitting there ready to be used.
func PlanRequeue(
	ctx context.Context, deps RequeueDeps, req RequeueRequest, signal LossSignal,
) (RequeuePlan, error) {
	if req.DispatchID == "" || req.Action.ID == "" {
		return RequeuePlan{}, errUnidentifiedHold()
	}
	if !req.Action.Idempotent {
		held := HeldOutcome{
			DispatchID: req.DispatchID, ActionID: req.Action.ID, NodeID: req.LostNodeID,
			Attempt: currentAttempt(deps.Attempts, req.DispatchID),
			Signal:  signal, Reason: heldReason(signal, req.LostNodeID),
		}
		return RequeuePlan{Disposition: DispositionHold, Signal: signal},
			HoldUnknownOutcome(ctx, deps.Attention, held)
	}

	node, err := placeReplacement(deps, req)
	if err != nil {
		return RequeuePlan{}, err
	}
	resume, err := ResumeFrom(ctx, deps.Continuity, req.EntityID)
	if err != nil {
		return RequeuePlan{}, err
	}
	if deps.Attempts == nil {
		return RequeuePlan{}, errNoAttemptRegister(req.DispatchID)
	}
	attempt := deps.Attempts.Next(req.DispatchID)
	return RequeuePlan{
		Disposition: DispositionRequeue,
		Signal:      signal,
		Node:        node,
		Attempt:     attempt,
		Branch:      DispatchBranch(req.DispatchID, attempt),
		Resume:      resume,
	}, nil
}

// placeReplacement re-runs the ORIGINAL requirement through the placement
// engine over every candidate except the node that was lost.
//
// No filter is relaxed and none is skipped. If nothing is eligible the
// engine's own error is returned unchanged — "nowhere left to run this" is
// a real answer, and the one thing recovery must never do is decide that
// the controller will run it instead.
func placeReplacement(deps RequeueDeps, req RequeueRequest) (DeviceRecord, error) {
	remaining := make([]Candidate, 0, len(deps.Candidates))
	for _, c := range deps.Candidates {
		if c.Record.NodeID == req.LostNodeID {
			continue
		}
		remaining = append(remaining, c)
	}
	eligible, err := deps.Placement.Eligible(req.Requirement, remaining)
	if err != nil {
		return DeviceRecord{}, err
	}
	return eligible[0], nil
}

// currentAttempt reports the attempt the lost dispatch was on, for the
// held record. A nil register reads as zero rather than panicking: a hold
// must still reach a person on a controller whose wiring is broken.
func currentAttempt(reg *AttemptRegister, dispatchID string) uint64 {
	if reg == nil {
		return 0
	}
	return reg.Current(dispatchID)
}
