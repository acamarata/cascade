// Package policy (approval_queue_batch.go): Purpose: the batching,
//
//	deduplication and audit-recording mechanics that sit under Enqueue.
//
// Inputs: an already-validated EnqueueRequest plus its derived digests,
//
//	under the queue's lock.
//
// Outputs: an EnqueueResult and a snapshot of the admitted entry.
// Constraints: BATCHING MUST NOT LAUNDER RISK. A batch is a presentation
//
//	grouping and nothing more — it carries no verdict, no rung and no
//	approval of its own, so joining a batch can never raise or lower what
//	an entry is. Each member keeps its own rung and its own token, and
//	approval_queue_decide.go decides members one at a time. Deduplication
//	is scoped to the OPEN batch, keyed on (action_hash, params_hash): a
//	duplicate returns the existing request id and mints no second token, so
//	one action never yields two redeemable approvals. The window and the
//	cap are read from the R-14.29 config numerics and the whole subsystem
//	is clock-driven, never timer-driven, so a seeded clock reproduces every
//	flush exactly.
//
// SPORT: internal/policy StoreApprovals/ADDED (P1-E09-W2-S18-T3).
package policy

import (
	"context"
	"time"

	"github.com/acamarata/cascade/internal/audit"
)

// admitLocked performs the admission steps that need the queue's lock:
// close a due batch, coalesce a duplicate, or mint and file a new entry.
// It returns a SNAPSHOT of the entry so the caller can write an audit row
// after releasing the lock.
func (q *StoreApprovals) admitLocked(
	ctx context.Context, req EnqueueRequest, actionHash, paramsHash string, granted bool,
) (EnqueueResult, approvalEntry, error) {
	now := q.cfg.Clock.Now().UTC()
	q.pruneLocked(now)
	flushed := q.closeDueBatchLocked(now)
	if existing := q.dedupLocked(actionHash, paramsHash); existing != nil {
		return EnqueueResult{
			RequestID:    existing.requestID,
			BatchID:      existing.batchID,
			Deduplicated: true,
			Flushed:      flushed,
			Level:        existing.level,
			Summary:      existing.summary,
		}, *existing, nil
	}
	if len(q.entries) >= q.pendingCeiling() {
		return EnqueueResult{}, approvalEntry{}, refuse(ErrApprovalQueueFull,
			"the queue already holds %d pending approvals, its ceiling of %d batches of %d",
			len(q.entries), maxRetainedBatches, q.cfg.Batching.Cap)
	}
	entry, token, err := q.mintLocked(ctx, req, actionHash, paramsHash, granted, now)
	if err != nil {
		return EnqueueResult{}, approvalEntry{}, err
	}
	if len(q.batch.members) >= q.cfg.Batching.Cap {
		q.batch = nil
		flushed = true
	}
	return EnqueueResult{
		RequestID: entry.requestID,
		BatchID:   entry.batchID,
		Flushed:   flushed,
		Level:     entry.level,
		Summary:   entry.summary,
		Token:     token,
	}, *entry, nil
}

// mintLocked mints a token, builds the entry, and files it in the open
// batch (opening one if none is open).
func (q *StoreApprovals) mintLocked(
	ctx context.Context, req EnqueueRequest, actionHash, paramsHash string, granted bool, now time.Time,
) (*approvalEntry, ApprovalToken, error) {
	token, err := q.cfg.Minter.Mint(ctx, ApprovalMintRequest{
		Subject:    req.Subject,
		ActionHash: actionHash,
		ParamsHash: paramsHash,
		Issued:     now,
		Expires:    q.expiryFor(now),
	})
	if err != nil {
		return nil, ApprovalToken{}, err
	}
	if err := validateNonce(token.Nonce); err != nil {
		return nil, ApprovalToken{}, err
	}
	if err := validateNonce(token.RequestID); err != nil {
		return nil, ApprovalToken{}, err
	}
	token.Expires = q.clampExpiry(now, token.Expires)
	q.openBatchLocked(now)
	q.seq++
	entry := &approvalEntry{
		seq: q.seq, requestID: token.RequestID, batchID: q.batch.id,
		subject: req.Subject, capability: req.Capability, level: safeLevel(req.Level),
		summary:    presentedSummary(req.Summary, safeLevel(req.Level)),
		actionHash: actionHash, paramsHash: paramsHash, nonce: token.Nonce,
		issued: now, expires: token.Expires, state: ApprovalPending, grantBacked: granted,
	}
	q.entries[entry.requestID] = entry
	q.order = append(q.order, entry.requestID)
	q.batch.members = append(q.batch.members, entry.requestID)
	return entry, token, nil
}

// expiryFor returns the exp a fresh token gets: the §5.24 ceiling.
//
// The batch WINDOW and the token's LIFETIME are deliberately different
// quantities. The window is how long the queue keeps collecting before it
// presents a prompt — ten seconds by default, far too short to be a
// deadline for a human answer. The lifetime is how long that answer stays
// redeemable, which §5.24 caps at five minutes and does not let an operator
// raise. Tying exp to the window would expire tokens before the prompt they
// belong to had even been read.
func (q *StoreApprovals) expiryFor(now time.Time) time.Time {
	return q.clampExpiry(now, now.Add(MaxApprovalTTL))
}

// clampExpiry enforces the MaxApprovalTTL ceiling on any proposed exp,
// including one a minter chose for itself. The ceiling is not
// configurable, so this is the only place exp is decided.
func (q *StoreApprovals) clampExpiry(now, proposed time.Time) time.Time {
	ceiling := now.Add(MaxApprovalTTL)
	if proposed.IsZero() || proposed.After(ceiling) {
		return ceiling
	}
	return proposed
}

// presentedSummary composes the exact string a surface must display: the
// caller's description with the rung appended. The rung lives IN the
// summary rather than in a fourth PendingEntry field so that the string
// shown to the human is the same string a decision is checked against —
// there is no second channel a rung could travel by and disagree.
func presentedSummary(summary string, level RiskLevel) string {
	return summary + " [" + level.String() + "]"
}

// openBatchLocked opens a collection window if none is open.
func (q *StoreApprovals) openBatchLocked(now time.Time) {
	if q.batch != nil {
		return
	}
	q.seq++
	q.batch = &approvalBatch{id: batchID(q.seq), openedAt: now}
}

// batchID renders a batch identifier from the queue's monotonic sequence,
// so batch names are stable and ordered across a run without a second id
// source.
func batchID(seq uint64) string {
	return "batch-" + formatSeq(seq)
}

// formatSeq renders a sequence number without importing a formatter for
// one call site.
func formatSeq(seq uint64) string {
	if seq == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for seq > 0 {
		i--
		buf[i] = byte('0' + seq%10)
		seq /= 10
	}
	return string(buf[i:])
}

// closeDueBatchLocked closes the open batch when its window has elapsed or
// it is already at the cap, and reports whether it closed one.
func (q *StoreApprovals) closeDueBatchLocked(now time.Time) bool {
	if q.batch == nil {
		return false
	}
	full := len(q.batch.members) >= q.cfg.Batching.Cap
	elapsed := !now.Before(q.batch.openedAt.Add(q.window()))
	if !full && !elapsed {
		return false
	}
	q.batch = nil
	return true
}

// dedupLocked returns the open batch's entry for these digests, or nil.
// Scoping to the OPEN batch is the point: a duplicate arriving while the
// prompt is still being assembled joins the question already being asked,
// while the same action arriving after that prompt closed is a NEW request
// the user has not yet been asked about.
func (q *StoreApprovals) dedupLocked(actionHash, paramsHash string) *approvalEntry {
	if q.batch == nil {
		return nil
	}
	for _, id := range q.batch.members {
		e, ok := q.entries[id]
		if !ok || e.state != ApprovalPending {
			continue
		}
		if e.actionHash == actionHash && e.paramsHash == paramsHash {
			return e
		}
	}
	return nil
}

// pendingCeiling bounds the pending set at the operator's own per-batch cap
// times the retained-batch limit, so the ceiling scales with the sizing the
// operator chose rather than with an unrelated constant.
func (q *StoreApprovals) pendingCeiling() int {
	return q.cfg.Batching.Cap * maxRetainedBatches
}

// record writes one audit row for entry. A queue with no recorder still
// enforces every rule; it simply writes nothing. A recorder failure is
// deliberately not propagated to the decision path: the decision has
// already been made under the lock, and turning a log failure into an
// approval failure would make the audit sink able to deny actions.
func (q *StoreApprovals) record(ctx context.Context, kind audit.Kind, entry approvalEntry, outcome string) {
	if q.cfg.Recorder == nil || entry.requestID == "" {
		return
	}
	_, _ = q.cfg.Recorder.Append(ctx, audit.Event{
		Kind:       kind,
		Actor:      entry.subject.String(),
		Action:     entry.capability,
		ParamsHash: entry.paramsHash,
		RiskLevel:  entry.level.String(),
		Verdict:    VerdictAsk.String(),
		Outcome:    outcome,
	})
}
