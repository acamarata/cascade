// Package policy (approval_queue_expire.go): Purpose: the expiry half of
//
//	the approval queue — the prune that runs on every read, the
//	rate-limited background sweep, and the retention that keeps a
//	just-spent entry recognisable.
//
// Inputs: the injected clock, and the entries the queue holds.
// Outputs: the count of entries retired, and staged audit snapshots.
// Constraints: NO APPROVAL OUTLIVES ITS WINDOW. Pruning runs
//
//	unconditionally on every GetPending and on every admission, so a lapsed
//	entry can never be read back as pending; the sweep exists only as a
//	freshness floor for a queue nobody is reading, which is why it is
//	rate-limited rather than the primary path. Expiry is TERMINAL: the
//	queue does not re-ask, because silently re-asking would turn a question
//	the user declined to answer into one they are asked again until they
//	give in. And a recorder is never called while the lock is held, so the
//	rows are staged here and written by the caller after it is released.
//
// SPORT: internal/policy StoreApprovals/ADDED (P1-E09-W2-S18-T3).
package policy

import (
	"context"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// approvalRetention is how long a terminal entry is kept after its exp, so
// a second redemption of a just-spent token is answered "replayed" rather
// than "unknown". Beyond it the entry is evicted and the durable ledger is
// the only remaining record — which still denies.
const approvalRetention = MaxApprovalTTL

// Expire implements ApprovalQueue: the background sweep, rate-limited to
// at most one pass a minute. GetPending prunes unconditionally, so this is
// a freshness floor for an idle queue rather than the primary path.
func (q *StoreApprovals) Expire(ctx context.Context) (int, error) {
	if q == nil {
		return 0, cascade.New(cascade.KindInvalidInput, "policy: nil approval queue")
	}
	q.mu.Lock()
	now := q.cfg.Clock.Now().UTC()
	if !q.lastSweep.IsZero() && now.Sub(q.lastSweep) < approvalSweepInterval {
		q.mu.Unlock()
		return 0, nil
	}
	q.lastSweep = now
	n := q.pruneLocked(now)
	q.mu.Unlock()
	q.recordExpired(ctx)
	return n, nil
}

// pruneLocked retires every entry whose exp has passed and evicts terminal
// entries older than the retention window. It returns how many entries it
// retired, and stages their snapshots for the audit row the caller writes
// after releasing the lock.
func (q *StoreApprovals) pruneLocked(now time.Time) int {
	retired := 0
	kept := q.order[:0]
	for _, id := range q.order {
		e, ok := q.entries[id]
		if !ok {
			continue
		}
		if (e.state == ApprovalPending || e.state == ApprovalApproved) && !now.Before(e.expires) {
			e.state = ApprovalExpired
			q.expiredAudit = append(q.expiredAudit, *e)
			retired++
		}
		if e.state != ApprovalPending && now.After(e.expires.Add(approvalRetention)) {
			delete(q.entries, id)
			continue
		}
		kept = append(kept, id)
	}
	q.order = kept
	return retired
}

// recordExpired writes the approval.expire rows staged by pruneLocked.
func (q *StoreApprovals) recordExpired(ctx context.Context) {
	q.mu.Lock()
	staged := q.expiredAudit
	q.expiredAudit = nil
	q.mu.Unlock()
	for _, e := range staged {
		q.record(ctx, audit.KindApprovalExpire, e, ApprovalExpired.String())
	}
}
