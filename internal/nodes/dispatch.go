package nodes

import (
	"context"
	"sync"
	"time"
)

// Purpose (this file): the dispatch orchestration core — the attempt
//
//	fencing register, the node-side durable dedup of action ids, and the
//	sensitivity/trust admission the ship leg must pass before any byte
//	leaves the controller.
//
// Inputs: a dispatch request and the placement decision behind it.
// Outputs: an attempt to ship, or a fail-closed refusal.
// Constraints: this file decides; dispatch_git.go moves bytes and
//
//	dispatch_frame.go proves identity. Keeping the decisions here is what
//	lets every rule below be tested without a repository, a socket or a
//	second machine.
//
//	Recovery is NOT here (S-37.T3 owns it). What is here is the
//	classification recovery needs: which failures are safe to retry, and
//	which outcome must be held for a human instead.
//
// SPORT: internal/nodes:dispatch (ADD) — P1-E17-W4-S37-T2.

// AdmitDispatch reports whether work at this sensitivity may be shipped to
// a node at this trust tier.
//
// Sensitivity and its three values are declared by placement_trust.go
// (S-37.T1), which also owns ResolveSensitivity — the fail-closed
// normalizer that maps an unrecognized raw string to the MOST restrictive
// class. This function is the second half of that pair: the normalizer
// turns what it cannot identify into the safest class, and this refuses
// what it still cannot identify at all. Neither is redundant, and neither
// re-declares the type.
//
// The rank comparison is NOT reimplemented here: it goes through
// trust.Satisfies, which is R-21.220's single source of truth for what a
// tier clears. A second copy of "rank >= worker-trusted" in this package
// would be a second thing to get wrong, and the one that drifts is the one
// nobody is reading when it matters.
//
// Three rules, all fail-closed:
//
//   - local-only NEVER ships. GateLocalOnly is an identity check against
//     the controller machine, so no remote node can ever satisfy it; this
//     is the second, independent refusal after placement's, so a caller
//     that bypassed placement still cannot ship local-only work.
//   - restricted requires a tier clearing GateRestricted.
//   - an unrecognized sensitivity or an unrecognized tier is REFUSED
//     rather than read as normal. An unresolvable tier is exactly where
//     guessing is most likely to be wrong and most costly to be wrong
//     about.
func AdmitDispatch(work Sensitivity, nodeTier Tier) error {
	if _, ok := Rank(nodeTier); !ok {
		return errUnresolvableTrust(string(nodeTier))
	}
	switch work {
	case SensitivityLocalOnly:
		return errLocalOnlyNeverShips()
	case SensitivityRestricted:
		if !Satisfies(nodeTier, GateRestricted) {
			return errRestrictedNeedsTrust(string(nodeTier))
		}
		return nil
	case SensitivityNormal:
		return nil
	default:
		return errUnresolvableSensitivity(string(work))
	}
}

// AttemptRegister is the controller's fencing authority: the current
// attempt number for every live dispatch.
//
// In-process and deliberately so, matching SequenceStore's own reasoning:
// the controller process's lifetime is the fencing window. A restart
// resets to zero, which is conservative rather than permissive — a node's
// in-flight attempt cannot be confused for a NEW dispatch, because the
// dispatch id is what identifies it and a fresh controller has no record
// of that id at all.
type AttemptRegister struct {
	mu       sync.Mutex
	attempts map[string]uint64
}

// NewAttemptRegister builds an empty register.
func NewAttemptRegister() *AttemptRegister {
	return &AttemptRegister{attempts: map[string]uint64{}}
}

// Next supersedes any previous attempt for dispatchID and returns the new
// attempt number. It is the only way an attempt number is minted, so two
// concurrent retries cannot mint the same one.
func (r *AttemptRegister) Next(dispatchID string) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.attempts == nil {
		r.attempts = map[string]uint64{}
	}
	r.attempts[dispatchID]++
	return r.attempts[dispatchID]
}

// Current reports the attempt the controller considers live.
func (r *AttemptRegister) Current(dispatchID string) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attempts[dispatchID]
}

// Forget drops a dispatch once it reaches a terminal outcome.
func (r *AttemptRegister) Forget(dispatchID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.attempts, dispatchID)
}

// ActionLog is the node-side durable dedup record (R-21.221).
//
// The contract's ordering is the whole point: an action id is recorded
// BEFORE the action runs, never after. Recording afterwards would leave a
// window where a crash between executing and recording lets a redelivery
// run the action a second time — which for a non-idempotent action means a
// duplicated external side effect, the exact thing this exists to prevent.
type ActionLog interface {
	// Reserve records actionID as started. It reports already=true when
	// the id was already recorded, which is the refusal case.
	Reserve(ctx context.Context, actionID string) (already bool, err error)
	// Complete records the terminal outcome for actionID.
	Complete(ctx context.Context, actionID string, outcome DispatchOutcome) error
}

// Action is one unit of dispatched work.
type Action struct {
	// ID is stable across redeliveries of the same work. It is what makes
	// dedup possible, so a caller minting a fresh id per delivery defeats
	// it entirely.
	ID string
	// Idempotent declares whether re-running this action is safe.
	//
	// It is declared, never inferred. The node cannot tell from the
	// outside whether an action charges a card or writes a file, so the
	// caller says, and an unset value means "not safe" — the conservative
	// reading.
	Idempotent bool
}

// ReserveAction is the node-side admission for one action.
//
// A duplicate is refused rather than re-run, and the refusal is typed so
// the controller can tell it apart from a failure: a refused duplicate
// means the work already happened, which is success from the caller's
// point of view, not an error to retry.
func ReserveAction(ctx context.Context, log ActionLog, action Action) error {
	if log == nil {
		return errNoActionLog()
	}
	if action.ID == "" {
		return errUnidentifiedAction()
	}
	already, err := log.Reserve(ctx, action.ID)
	if err != nil {
		return err
	}
	if already {
		return ErrDuplicateAction(action.ID)
	}
	return nil
}

// ResolveAmbiguousOutcome decides what to do about an action that ran but
// whose acknowledgement never arrived.
//
// An idempotent action is safe to re-queue: running it twice is
// indistinguishable from running it once. A non-idempotent one is HELD —
// re-running may duplicate an external effect and abandoning may lose work
// that succeeded, and nothing in the system can tell which, so a human
// decides. Returning an error here rather than a bool is deliberate: the
// caller cannot accidentally proceed past it.
func ResolveAmbiguousOutcome(dispatchID string, action Action) error {
	if action.Idempotent {
		return nil
	}
	return ErrUnknownOutcome(dispatchID, action.ID)
}

// Attempt is one shipping attempt of a dispatch.
type Attempt struct {
	DispatchID string
	Attempt    uint64
	NodeID     string
	Branch     string
	StartedAt  time.Time
	// Resume is where this attempt picks up, and is set only on a
	// REPLACEMENT: a first attempt has nothing to resume from, and its
	// zero value is the cold start it actually is.
	//
	// It lives on the attempt rather than being passed alongside it
	// because the attempt is what travels — minted by the controller,
	// handed to the node by the claim verb, quoted back on the execute
	// call. A resume point carried on any other path could arrive with a
	// different attempt's number attached to it.
	Resume ResumePoint
}

// NewAttempt mints the next attempt for a dispatch.
func NewAttempt(reg *AttemptRegister, dispatchID, nodeID string, now time.Time) Attempt {
	n := reg.Next(dispatchID)
	return Attempt{
		DispatchID: dispatchID,
		Attempt:    n,
		NodeID:     nodeID,
		Branch:     DispatchBranch(dispatchID, n),
		StartedAt:  now,
	}
}
