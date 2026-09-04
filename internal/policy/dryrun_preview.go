// Package policy (dryrun_preview.go): Purpose: the side-effect boundary a
//
//	simulation runs behind — the discarding approval sink the engine
//	writes through during a dry run, and the READ-ONLY half of the live
//	queue's admission question that sink asks on its behalf.
//
// Inputs: the live ApprovalQueue (optional) and the EnqueueRequest the
//
//	engine's ask-verdict path produces.
//
// Outputs: the admission answer the live queue WOULD have given, with no
//
//	entry filed, no token minted and no audit row written.
//
// Constraints: every method here either reads or refuses. previewEnqueue
//
//	runs the live queue's own admissible/hash checks — the same functions
//	Enqueue runs, not copies of them — and then answers the two questions
//	that need the lock by READING the queue's state under it. It never
//	calls pruneLocked, closeDueBatchLocked, mintLocked or record, which are
//	the only mutating steps on that path.
//
//	The two places this can disagree with a live Enqueue are named at their
//	sites, and both disagree in the same direction: the preview refuses
//	where the live path might have admitted, never the reverse. A dry run
//	is therefore never more permissive than the action.
//
//	A queue this package did not build cannot be previewed, because there
//	is no way to know that its Enqueue does not write. Such a queue is
//	refused rather than called: an unsimulatable sink downgrades the report
//	to deny, which is the fail-closed direction.
//
// SPORT: internal/policy StoreApprovals/CHANGED (P1-E09-W2-S18-T4).
package policy

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// approvalPreviewer is the read-only half of an approval queue: what it
// WOULD do with an action, and whether doing it would write an audit row.
//
// It is unexported, and so are its methods, so only a queue built in this
// package can satisfy it. That is the enforcement of the rule above: a
// foreign ApprovalQueue cannot accidentally opt in to being simulated by
// naming two methods.
type approvalPreviewer interface {
	// previewEnqueue answers admission without changing anything.
	previewEnqueue(ctx context.Context, req EnqueueRequest) (EnqueueResult, error)
	// wouldRecord reports whether an admitted action would produce an
	// audit row, i.e. whether an audit sink is attached.
	wouldRecord() bool
}

// errUnsimulatableQueue refuses an approval sink whose admission cannot be
// asked without the risk of writing. The message is fixed, so two
// simulations of the same action render identically.
func errUnsimulatableQueue() error {
	return cascade.New(cascade.KindUnsupported,
		"policy: this approval queue cannot be asked what it would do without acting on it")
}

// discardEffects is the sink an engine writes through during a dry run.
// It answers the engine's enqueue with the live queue's own read-only
// admission verdict and files nothing.
//
// The remaining ApprovalQueue methods are not reached by Evaluate. They
// are implemented as what they truthfully are for a queue that holds
// nothing: a simulation has no pending entries to list, expire, decide,
// cancel or redeem. Nothing here is a placeholder for behaviour that is
// coming later.
type discardEffects struct {
	// live is the queue whose admission is being previewed. It is nil
	// when the engine has no queue attached, in which case the engine
	// never reaches this sink at all.
	live ApprovalQueue
	// admitted records that the preview said the live queue would take
	// this action, which is exactly when the live path would have written
	// its approval.enqueue or approval.dedup row.
	admitted bool
	// recorder records whether an audit sink is attached to the live
	// queue, since a queue with no recorder enforces every rule and
	// writes nothing.
	recorder bool
}

// Enqueue implements ApprovalQueue by asking, not doing.
func (d *discardEffects) Enqueue(ctx context.Context, req EnqueueRequest) (EnqueueResult, error) {
	p, ok := d.live.(approvalPreviewer)
	if !ok {
		return EnqueueResult{}, errUnsimulatableQueue()
	}
	res, err := p.previewEnqueue(ctx, req)
	if err != nil {
		return EnqueueResult{}, err
	}
	d.admitted = true
	d.recorder = p.wouldRecord()
	// The request id is deliberately left as the preview found it: an
	// existing one for an action that would coalesce with a prompt
	// already being assembled, and EMPTY for one that would be filed
	// fresh. Minting an id here would hand the caller a reference to an
	// entry that does not exist and that nobody can ever approve.
	return res, nil
}

// GetPending implements ApprovalQueue. A simulation queued nothing, so
// there is nothing pending.
func (d *discardEffects) GetPending(_ context.Context) ([]PendingEntry, error) {
	return nil, nil
}

// Decide implements ApprovalQueue. There is nothing to decide about.
func (d *discardEffects) Decide(_ context.Context, _ []DecisionRequest) ([]DecisionOutcome, error) {
	return nil, errSimulationHoldsNothing("decide")
}

// Cancel implements ApprovalQueue. There is nothing to withdraw.
func (d *discardEffects) Cancel(_ context.Context, _ string) error {
	return errSimulationHoldsNothing("cancel")
}

// ConsumeToken implements ApprovalQueue. A simulation mints no token, so
// there is none to redeem — and a simulation that could mint a redeemable
// one would be a way to manufacture approvals.
func (d *discardEffects) ConsumeToken(_ context.Context, _ ConsumeRequest) (ConsumeResult, error) {
	return ConsumeResult{}, errSimulationHoldsNothing("redeem an approval from")
}

// Expire implements ApprovalQueue. Nothing was filed, so nothing retires.
func (d *discardEffects) Expire(_ context.Context) (int, error) { return 0, nil }

// wouldEmitAudit reports whether the live path would have written an audit
// row for the action just simulated: it would, exactly when the action was
// admissible and an audit sink is attached.
func (d *discardEffects) wouldEmitAudit() bool { return d.admitted && d.recorder }

// errSimulationHoldsNothing refuses an operation that only makes sense
// against a real queue.
func errSimulationHoldsNothing(verb string) error {
	return cascade.New(cascade.KindNotFound,
		"policy: a simulation queues nothing, so there is no entry to "+verb)
}

var _ ApprovalQueue = (*discardEffects)(nil)
var _ approvalPreviewer = (*StoreApprovals)(nil)

// previewEnqueue answers what Enqueue would do, without doing it.
//
// The first half is Enqueue's own first half, called rather than copied:
// admissible runs every lock-free refusal in the contract's order, and the
// digests are computed by the same hashApproval and verified by the same
// checkSuppliedParamsHash. The second half reads the two lock-held answers
// — would this coalesce, and is there room — directly, because the live
// versions of those steps are entangled with mutation.
func (q *StoreApprovals) previewEnqueue(ctx context.Context, req EnqueueRequest) (EnqueueResult, error) {
	if q == nil {
		return EnqueueResult{}, cascade.New(cascade.KindInvalidInput, "policy: nil approval queue")
	}
	if err := q.admissible(ctx, req); err != nil {
		return EnqueueResult{}, err
	}
	actionHash := hashApproval([]byte(req.Action))
	paramsHash := hashApproval(req.Params)
	if err := checkSuppliedParamsHash(req.ParamsHash, paramsHash); err != nil {
		return EnqueueResult{}, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	// DIVERGENCE 1, conservative: the live path closes a batch whose
	// window has elapsed before it looks for a duplicate, and this read
	// does not. So a preview taken after the window elapsed may report a
	// coalesce the live call would not make. It cannot change the
	// VERDICT — a coalesced ask and a fresh ask are both an ask — it only
	// changes whether an existing request id is quoted.
	if existing := q.dedupLocked(actionHash, paramsHash); existing != nil {
		return EnqueueResult{
			RequestID:    existing.requestID,
			BatchID:      existing.batchID,
			Deduplicated: true,
			Level:        existing.level,
			Summary:      existing.summary,
		}, nil
	}
	// DIVERGENCE 2, conservative: the live path prunes entries past their
	// retention window before this test and this read does not, so while
	// the queue holds retired entries that have not yet been swept, the
	// preview can report the queue full where the live call would have
	// made room. It errs toward reporting a deny for an action that would
	// have been asked about, never the reverse.
	if len(q.entries) >= q.pendingCeiling() {
		return EnqueueResult{}, refuse(ErrApprovalQueueFull,
			"the queue already holds %d pending approvals, its ceiling of %d batches of %d",
			len(q.entries), maxRetainedBatches, q.cfg.Batching.Cap)
	}
	return EnqueueResult{
		Level:   safeLevel(req.Level),
		Summary: presentedSummary(req.Summary, req.Level),
	}, nil
}

// wouldRecord reports whether this queue has an audit sink attached.
func (q *StoreApprovals) wouldRecord() bool { return q != nil && q.cfg.Recorder != nil }
