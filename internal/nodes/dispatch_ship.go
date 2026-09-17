package nodes

import (
	"context"
	"time"
)

// Purpose (this file): the ship sequence itself — admit, mint the attempt,
//
//	push, call the node, verify its signed answer, fetch results, tear
//	down. Split from dispatch.go to stay under Art.10.3's 300-line cap.
//
// Inputs: a ShipRequest, the target node's record, and the collaborators.
// Outputs: the commit the results landed at, or a typed fail-closed error.
// Constraints: there is NO controller-local fallback on any path here.
//
//	Work placed on a node either runs there or fails visibly; quietly
//	running it locally instead would break the one promise placement
//	makes — that the work ran somewhere with the capabilities it asked
//	for.
//
// SPORT: internal/nodes:dispatch-ship (ADD) — P1-E17-W4-S37-T2.

// NodeCaller performs the controller→node leg over the S-36 RPC channel.
//
// It returns the node's SIGNED frame; verification is the caller's, not
// this seam's, so a transport that is compromised or merely wrong cannot
// hand back a frame that skipped the signature check.
type NodeCaller interface {
	// Call ships one attempt to a node and returns the frame it answers
	// with.
	Call(ctx context.Context, nodeID string, attempt Attempt) (DispatchFrame, error)
}

// ShipDeps are the collaborators one dispatch needs.
type ShipDeps struct {
	// Git performs the push/fetch legs.
	Git dispatchGit
	// Caller reaches the node.
	Caller NodeCaller
	// Attempts is the fencing authority.
	Attempts *AttemptRegister
	// Sequences tracks per-node frame sequences for replay refusal.
	Sequences *SequenceStore
	// Remote is the git remote the node fetches from and pushes to.
	Remote string
	// Clock stamps the attempt. Injected rather than read from time.Now
	// so an attempt's timing is reproducible in a test (Art.7.3).
	Clock Clock
}

// ShipRequest is one unit of work to place on a node.
type ShipRequest struct {
	// DispatchID identifies the work across attempts.
	DispatchID string
	// Head is the commit the work branch is cut from.
	Head string
	// Work is the action being dispatched.
	Work Action
	// Sensitivity is the work's resolved class.
	Sensitivity Sensitivity
	// Payload is the work description the node receives. It is checked
	// for static key material before anything ships.
	Payload []byte
	// Capabilities are the capability names the work needs, carried so a
	// re-queue can re-apply the ORIGINAL placement demand rather than a
	// weaker one reconstructed from what is left (S-37.T3).
	Capabilities []string
	// EntityID is the journal entity this dispatch's records stream to.
	// Recovery reads it to find where the lost attempt got to; without it
	// a replacement would start the work over.
	EntityID string
}

// ShipOutcome is what one completed dispatch produced.
//
// The ATTEMPT is returned rather than read back from the register, because
// a terminal dispatch forgets its attempt — there is nothing left to fence
// against once the work is done. The caller still needs the number: journal
// records carry it, so without it a caller cannot correlate the commit it
// got with the records that describe how it was produced.
type ShipOutcome struct {
	// Commit is where the node's results landed.
	Commit string
	// Attempt is the fencing number this dispatch shipped under.
	Attempt uint64
}

// Ship places one dispatch on node and returns the commit its results
// landed at.
//
// The order is the contract, and every step fails closed:
//
//  1. ADMIT before anything moves. A refusal here costs no egress, and
//     local-only work never reaches the push leg at all.
//  2. MINT the attempt, which supersedes any previous one — so a
//     partitioned node's in-flight attempt is already stale before this
//     one ships a byte.
//  3. PUSH the work branch.
//  4. CALL the node and VERIFY its signed answer against the attempt just
//     minted. An unverified or superseded frame is refused here.
//  5. FETCH results only after that verification, never before: fetching
//     first would pull content named by an unverified frame.
//  6. TEAR DOWN the worktree on any terminal outcome, success or not.
//
// There is deliberately NO controller-local fallback on any failure path.
// Work placed on a node either runs there or fails visibly; running it
// locally instead would silently break the one promise placement makes.
func Ship(ctx context.Context, deps ShipDeps, node DeviceRecord, req ShipRequest) (outcome ShipOutcome, err error) {
	if err := AdmitDispatch(req.Sensitivity, node.Tier); err != nil {
		return ShipOutcome{}, err
	}
	// Defense in depth behind PlanCredentials: the plan never puts a
	// static key on a dispatch, and the payload is checked as well, so a
	// key arriving by some path nobody anticipated is refused rather than
	// shipped.
	if err := AssertNoStaticKey(req.DispatchID, req.Payload); err != nil {
		return ShipOutcome{}, err
	}
	if deps.Caller == nil || deps.Attempts == nil {
		return ShipOutcome{}, ErrNodeUnreachable(node.NodeID, errShipUnwired())
	}

	return shipAttempt(ctx, deps, node, req,
		NewAttempt(deps.Attempts, req.DispatchID, node.NodeID, deps.now()))
}

// shipAttempt is Ship's body, at an attempt somebody has already minted.
//
// Split out because recovery needs it: S-37.T3's re-queue mints the
// replacement's attempt as part of DECIDING to replace (that number is the
// fence against the attempt it supersedes), and shipping it must use that
// number rather than minting a second one and skipping the first.
func shipAttempt(
	ctx context.Context, deps ShipDeps, node DeviceRecord, req ShipRequest, attempt Attempt,
) (outcome ShipOutcome, err error) {
	defer func() {
		// Terminal outcome: the worktree goes, whatever happened.
		_ = deps.Git.RemoveWorktree(ctx, req.DispatchID, attempt.Attempt)
	}()

	if err := deps.Git.PushWork(ctx, deps.Remote, req.DispatchID, req.Head, attempt.Attempt); err != nil {
		return ShipOutcome{}, err
	}

	frame, callErr := deps.Caller.Call(ctx, node.NodeID, attempt)
	if callErr != nil {
		// The call failed after the branch was pushed, so work may already
		// be running: this is a DROPPED TUNNEL, not an unreachable node,
		// and S-37.T3's recovery has to tell those apart.
		return ShipOutcome{}, ErrTunnelDropped(node.NodeID, req.DispatchID, callErr)
	}

	lastSeq := uint64(0)
	if deps.Sequences != nil {
		lastSeq = deps.Sequences.Last(node.NodeID)
	}
	if err := VerifyDispatchFrame(frame, node, lastSeq, deps.Attempts.Current(req.DispatchID)); err != nil {
		return ShipOutcome{}, err
	}
	if deps.Sequences != nil {
		deps.Sequences.Advance(node.NodeID, frame.Sequence)
	}

	if err := interpretOutcome(frame.Outcome, node.NodeID, req); err != nil {
		return ShipOutcome{}, err
	}

	commit, err := deps.Git.FetchResults(ctx, deps.Remote, req.DispatchID, attempt.Attempt)
	if err != nil {
		return ShipOutcome{}, err
	}
	deps.Attempts.Forget(req.DispatchID)
	return ShipOutcome{Commit: commit, Attempt: attempt.Attempt}, nil
}

// now reads the injected clock, defaulting to none rather than to a bare
// time.Now: an attempt stamped by an unspecified clock is an attempt whose
// timing no test can pin.
func (d ShipDeps) now() time.Time {
	if d.Clock == nil {
		return time.Time{}
	}
	return d.Clock.Now()
}

// interpretOutcome maps a verified frame's outcome onto this package's
// typed errors. Extracted from Ship to stay under Art.10.3's 50-line
// function cap, and because the mapping is the part worth reading on its
// own: each branch is a different thing having happened to the work.
func interpretOutcome(outcome DispatchOutcome, nodeID string, req ShipRequest) error {
	switch outcome {
	case OutcomeSucceeded:
		return nil
	case OutcomeRefused:
		// The node already ran this action id. That is success from the
		// caller's side — the work happened — not a failure to retry.
		return ErrDuplicateAction(req.Work.ID)
	case OutcomeFailed:
		return errDispatchRanAndFailed(req.DispatchID, nodeID)
	case OutcomeAccepted:
		// Acknowledged but not finished: the outcome is unknown, and
		// whether it may be retried depends on the action's own
		// idempotence declaration.
		return ResolveAmbiguousOutcome(req.DispatchID, req.Work)
	default:
		// Unreachable: VerifyDispatchFrame refuses an unrecognized
		// outcome before this is called. Fail closed anyway rather than
		// treating an unknown outcome as success.
		return errDispatchRanAndFailed(req.DispatchID, nodeID)
	}
}
