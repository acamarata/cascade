package nodes

import (
	"context"
	"errors"
	"fmt"
)

// Purpose (this file): the consumer that joins the ship leg to the
//
//	recovery decision — the thing that makes a lost node actually cost a
//	re-queue rather than a logged error.
//
// WHY IT IS ITS OWN ENTRY POINT rather than a branch inside Ship. Ship's
//
//	contract is one attempt on one node: it pushes, calls, verifies, fetches
//	and tears its worktree down, and every one of its failure paths is
//	typed. Recovery is a different decision on a different set of
//	collaborators — placement, a journal, an attention queue — and folding
//	it into Ship would make the single-attempt path depend on all of them.
//	A caller that wants one attempt calls Ship; a caller that wants the work
//	to survive a lost node calls this.
//
// ONE REPLACEMENT, NOT A RETRY LOOP. A failure on the replacement is
//
//	returned, not recovered from again. A loop here would turn a systemic
//	fault — a bad payload, an unreachable remote — into a walk across every
//	enrolled node, and the second failure is the one that tells an operator
//	it is not the node's fault.
//
// Inputs: the ship deps, the recovery deps, and the same request.
// Outputs: the replacement's outcome, or the typed failure.
// SPORT: internal/nodes:requeue-run (ADD) — P1-E17-W4-S37-T3.

// ShipWithRecovery ships req to node and, if that node is lost, re-queues
// the work onto an eligible replacement.
//
// Only a LOSS is recovered from. A refusal, a policy denial, a bad payload
// or a signature failure are the node telling the controller something
// true, and moving that work to a second machine would just get the same
// answer from a different place — after doing whatever damage the first
// answer was warning about.
func ShipWithRecovery(
	ctx context.Context, deps ShipDeps, recovery RequeueDeps, node DeviceRecord, req ShipRequest,
) (ShipOutcome, error) {
	outcome, shipErr := Ship(ctx, deps, node, req)
	if shipErr == nil {
		return outcome, nil
	}
	signal, lost := DetectLoss(LossObservation{
		Liveness: recovery.livenessOf(node.NodeID),
		Tunnel:   recovery.tunnelOf(node.NodeID),
		ChannelErr: func() error {
			if isLossShaped(shipErr) {
				return shipErr
			}
			return nil
		}(),
	})
	if !lost {
		return ShipOutcome{}, shipErr
	}

	plan, planErr := PlanRequeue(ctx, recovery, RequeueRequest{
		DispatchID:  req.DispatchID,
		LostNodeID:  node.NodeID,
		Action:      req.Work,
		Requirement: Requirement{Capabilities: req.Capabilities, Sensitivity: req.Sensitivity},
		EntityID:    req.EntityID,
	}, signal)
	if planErr != nil {
		// Both, and in that order: the operator needs to know the node was
		// lost AND why the work could not go elsewhere. Reporting only the
		// second reads as a placement problem on work that was running
		// fine until a machine disappeared.
		return ShipOutcome{}, fmt.Errorf(
			"nodes: dispatch %s lost node %q (%s: %v); re-queue: %w",
			req.DispatchID, node.NodeID, signal, shipErr, planErr)
	}
	return shipAttempt(ctx, deps, plan.Node, req, Attempt{
		DispatchID: req.DispatchID,
		Attempt:    plan.Attempt,
		NodeID:     plan.Node.NodeID,
		Branch:     plan.Branch,
		StartedAt:  deps.now(),
		// The point of reading the journal at all. Dropping it here left
		// the replacement to start from its own empty action log, which
		// is the failure continuity exists to prevent and which durable
		// dedup cannot catch when the replacement is a different machine.
		Resume: plan.Resume,
	})
}

// isLossShaped reports whether err is one of the ship leg's own
// node-is-gone errors, as opposed to a refusal the node meant.
func isLossShaped(err error) bool {
	return errors.Is(err, ErrDispatchUnreachable) || errors.Is(err, ErrDispatchTunnelDropped)
}

// livenessOf reads the lost node's liveness, defaulting to unknown — the
// fail-closed value — when no source is wired.
func (d RequeueDeps) livenessOf(nodeID string) Liveness {
	if d.Liveness == nil {
		return LivenessUnknown
	}
	return d.Liveness(nodeID)
}

// tunnelOf reads the lost node's tunnel state, defaulting to down when no
// source is wired, for the same reason.
func (d RequeueDeps) tunnelOf(nodeID string) TunnelState {
	if d.Placement.Tunnels == nil {
		return TunnelDown
	}
	return d.Placement.Tunnels(nodeID)
}
