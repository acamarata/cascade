package nodes

import (
	"errors"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the typed, taxonomy-mapped failures the dispatch leg
//
//	owns — an unreachable node, a failed push or fetch, a dropped tunnel, a
//	worktree that could not be made, and a result arriving from a
//	superseded attempt.
//
// Inputs: the dispatch identity and the underlying cause.
// Outputs: *cascade.Error values carrying a Kind from the frozen taxonomy.
// Constraints: every one of these is FAIL-CLOSED. None of them has a
//
//	controller-local fallback: work placed on a node either runs there or
//	fails visibly. Silently running it on the controller instead would
//	break the only promise placement makes — that the work ran somewhere
//	with the capabilities it asked for.
//
//	Recovery is NOT here. Re-queue and journal continuity on node loss
//	belong to S-37.T3; this file's job is to give that ticket failures it
//	can tell apart.
//
// SPORT: internal/nodes:dispatch-errors (ADD) — P1-E17-W4-S37-T2.

// ErrStaleAttempt reports a frame from a superseded dispatch attempt.
//
// It is the fencing rule's refusal (R-21.221). A node that was partitioned
// but still alive keeps working and will eventually try to push its result;
// without this, that result would land on the same branch as the
// replacement attempt's and one would silently overwrite the other. The
// attempt number is monotonic, so the controller can always tell which of
// the two is current.
var ErrStaleAttempt = errors.New("nodes: dispatch frame from a superseded attempt")

// ErrNodeUnreachable reports a node that could not be reached at all.
func ErrNodeUnreachable(nodeID string, cause error) error {
	return cascade.Wrapf(cascade.KindUnavailable, cause,
		"nodes: node %q is unreachable; the dispatch was not shipped", nodeID)
}

// ErrTunnelDropped reports a tunnel that closed mid-dispatch.
//
// Distinct from ErrNodeUnreachable on purpose: unreachable means nothing
// was shipped, while a dropped tunnel means work may already be running on
// the node. S-37.T3's recovery has to treat those differently, and a
// caller that cannot tell them apart would either re-queue work that is
// still running or abandon work that never started.
func ErrTunnelDropped(nodeID, dispatchID string, cause error) error {
	return cascade.Wrapf(cascade.KindUnavailable, cause,
		"nodes: the tunnel to node %q dropped during dispatch %s; the outcome is unknown", nodeID, dispatchID)
}

// ErrPushFailed reports a failed push of the work branch.
func ErrPushFailed(dispatchID string, cause error) error {
	return cascade.Wrapf(cascade.KindUnavailable, cause,
		"nodes: pushing the work branch for dispatch %s failed", dispatchID)
}

// ErrFetchFailed reports a failed fetch of the result branch.
func ErrFetchFailed(dispatchID string, cause error) error {
	return cascade.Wrapf(cascade.KindUnavailable, cause,
		"nodes: fetching results for dispatch %s failed", dispatchID)
}

// ErrWorktreeFailed reports a worktree that could not be created or removed.
func ErrWorktreeFailed(dispatchID string, cause error) error {
	return cascade.Wrapf(cascade.KindUnavailable, cause,
		"nodes: preparing the worktree for dispatch %s failed", dispatchID)
}

// ErrStaleAttemptf reports a frame whose attempt has been superseded.
//
// It wraps ErrStaleAttempt so callers can match it with errors.Is while
// still reading which attempt arrived and which one is current.
func ErrStaleAttemptf(dispatchID string, got, current uint64) error {
	return cascade.Wrapf(cascade.KindConflict, ErrStaleAttempt,
		"nodes: dispatch %s attempt %d is superseded by attempt %d", dispatchID, got, current)
}

// ErrUnverifiedSigner reports a dispatch frame whose signature did not
// verify against the node's enrolled identity key.
//
// Fail-closed and deliberately terse about the cause: an attacker learns
// nothing from the message beyond the fact that it was refused.
func ErrUnverifiedSigner(nodeID string) error {
	return cascade.Newf(cascade.KindPermissionDenied,
		"nodes: a dispatch frame claiming node %q failed signature verification", nodeID)
}

// ErrDuplicateAction reports an action id the node has already executed.
//
// This is the node-side durable dedup refusal (R-21.221). A redelivered
// dispatch must never produce a second external side effect, so the node
// records an action id BEFORE executing it and refuses the replay
// afterwards — the record has to precede the work, or a crash between
// executing and recording would let the replay through.
func ErrDuplicateAction(actionID string) error {
	return cascade.Newf(cascade.KindConflict,
		"nodes: action %s has already been executed on this node", actionID)
}

// ErrUnknownOutcome reports a non-idempotent action whose outcome could not
// be established.
//
// This is HELD for a human rather than retried. The action may have run and
// had its effect, with only the acknowledgement lost; re-running it would
// duplicate that effect, and abandoning it would lose work that succeeded.
// Neither is safe to choose automatically, so the dispatch stays in this
// state until someone decides (R-21.221; S-37.T3 owns the path that
// consumes it).
func ErrUnknownOutcome(dispatchID, actionID string) error {
	return cascade.Newf(cascade.KindConflict,
		"nodes: dispatch %s action %s executed-but-unacknowledged and is not idempotent; "+
			"held for a decision rather than re-queued", dispatchID, actionID)
}

// ErrStaticKeyOnDispatch reports an attempt to ship a static API key.
//
// §D-11 and 06 §5.22 make this unconditional: static keys are relay-only
// through the controller's Conductor. A key that reaches a node is a key on
// another machine's disk forever, so this refusal is not configurable.
func ErrStaticKeyOnDispatch(dispatchID string) error {
	return cascade.Newf(cascade.KindPolicyDenied,
		"nodes: dispatch %s carries a static API key; static keys are relayed through the "+
			"controller and never shipped to a node", dispatchID)
}

// errUnboundGrant reports a token grant missing one of the bindings that
// make it safe to mint. A grant without all of them is a token that
// out-scopes the dispatch it was issued for.
func errUnboundGrant(missing string) error {
	return cascade.Newf(cascade.KindInvalidInput,
		"nodes: a per-dispatch token needs a %s binding; an unbound token is not scoped to anything", missing)
}

// errLocalOnlyNeverShips refuses local-only work on the dispatch leg. It
// is unconditional: "local" names one machine, so no node's trust tier can
// ever satisfy it.
func errLocalOnlyNeverShips() error {
	return cascade.New(cascade.KindPolicyDenied,
		"nodes: local-only work is never dispatched to a node")
}

// errRestrictedNeedsTrust refuses restricted work on an insufficiently
// trusted node.
func errRestrictedNeedsTrust(tier string) error {
	return cascade.Newf(cascade.KindPolicyDenied,
		"nodes: restricted work needs a node clearing the restricted gate; this node is %q", tier)
}

// errUnresolvableTrust refuses a node whose trust tier is not one of the
// ratified three. Fail-closed: an unreadable tier is never read as the
// lowest one, which would silently admit work the operator never granted.
func errUnresolvableTrust(tier string) error {
	return cascade.Newf(cascade.KindPolicyDenied,
		"nodes: node trust_tier %q is unrecognized; dispatch refuses rather than assuming a tier", tier)
}

// errUnresolvableSensitivity refuses work whose sensitivity is not one of
// the three known tiers.
func errUnresolvableSensitivity(tier string) error {
	return cascade.Newf(cascade.KindPolicyDenied,
		"nodes: work sensitivity %q is unrecognized; dispatch refuses rather than assuming normal", tier)
}

// errNoActionLog reports a node-side dedup log that was never wired. A
// dispatch without one could redeliver an action and duplicate its effect,
// so it refuses rather than running unprotected.
func errNoActionLog() error {
	return cascade.New(cascade.KindInternal,
		"nodes: no action log is wired; a dispatch cannot be deduplicated without one")
}

// errUnidentifiedAction reports an action with no stable id. Dedup is
// impossible without one, so it is refused rather than run unprotected.
func errUnidentifiedAction() error {
	return cascade.New(cascade.KindInvalidInput,
		"nodes: a dispatched action needs a stable id; without one a redelivery cannot be detected")
}

// errShipUnwired reports a dispatch attempted without its collaborators.
func errShipUnwired() error {
	return cascade.New(cascade.KindInternal, "nodes: the dispatch path is not wired")
}

// errDispatchRanAndFailed reports work that reached a node, ran, and
// failed there. Distinct from every transport failure above: the work
// happened, so re-running it is a product decision, not a retry.
func errDispatchRanAndFailed(dispatchID, nodeID string) error {
	return cascade.Newf(cascade.KindUnavailable,
		"nodes: dispatch %s ran on node %q and failed there", dispatchID, nodeID)
}
