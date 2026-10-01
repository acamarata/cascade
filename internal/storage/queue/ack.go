// Purpose: Queue.Ack and Queue.Nack — the two receipt-consuming halves of
//   provider.Queue, split out from queue.go (Enqueue/Dequeue) to keep each
//   file under the 300-line file cap.
// Constraints: in-memory bookkeeping (st.receipts,
//   st.records, st.ready) is mutated ONLY after the corresponding
//   Store.Tx has actually committed — never before. This ordering is the
//   audit's exact fix: the pre-fix code released a claim's tracking
//   before attempting the Store delete, so a failed delete stranded the
//   body while a retry with the same receipt looked "unknown" (falsely
//   stale) instead of retrying the still-pending delete.
// SPORT: internal.storage.queue.Queue/CHANGED.

package queue

import (
	"bytes"
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Ack implements provider.Queue.
func (q *Queue) Ack(ctx context.Context, namespace, receipt string) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	st, err := q.namespaceLocked(ctx, namespace)
	if err != nil {
		return err
	}
	st.sweepExpiredLocked(q.clock.Now())

	id, ok := st.receipts[receipt]
	if !ok {
		return cascade.Newf(cascade.KindTimeout, "queue.Ack: receipt %q in namespace %q is stale or unknown", receipt, namespace)
	}
	tr := st.records[id]

	// delete fencing: tx.Get reads inside the ONE Tx, compared
	// against tr.knownBytes — the prior bytes this transition started
	// from, taken from memory (no separate Tx call reads it) — then
	// tx.Delete, because provider.Tx has no conditional delete.
	conflict := false
	txErr := q.store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		cur, getErr := tx.Get(ctx, namespace, msgKey(id))
		if getErr != nil && !cascade.HasKind(getErr, cascade.KindNotFound) {
			return getErr // a storage failure is not evidence the record changed
		}
		if getErr != nil || !bytes.Equal(cur, tr.knownBytes) {
			conflict = true
			return cascade.Newf(cascade.KindConflict, "queue.Ack: message %q in namespace %q changed since claim", id, namespace)
		}
		return tx.Delete(ctx, namespace, msgKey(id))
	})
	switch {
	case txErr == nil:
		delete(st.receipts, receipt)
		delete(st.records, id)
		return nil
	case conflict:
		// Pinned: the record changed underneath a
		// KNOWN, still-tracked receipt — never reported as KindTimeout,
		// which would read as "stale receipt, safe to retry/ignore" and
		// invite a caller to move on from bytes that were tampered with or
		// concurrently mutated outside this transition. KindTimeout stays
		// reserved for the "receipt unknown to this instance" branch above.
		return cascade.Newf(cascade.KindConflict, "queue.Ack: message %q in namespace %q changed since claim (receipt %q refused)", id, namespace, receipt)
	default:
		return cascade.Wrapf(cascade.KindUnavailable, txErr, "queue.Ack: removing message %q in namespace %q", id, namespace)
	}
}

// Nack implements provider.Queue.
func (q *Queue) Nack(ctx context.Context, namespace, receipt string) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	st, err := q.namespaceLocked(ctx, namespace)
	if err != nil {
		return err
	}
	st.sweepExpiredLocked(q.clock.Now())

	id, ok := st.receipts[receipt]
	if !ok {
		return cascade.Newf(cascade.KindTimeout, "queue.Nack: receipt %q in namespace %q is stale or unknown", receipt, namespace)
	}
	tr := st.records[id]
	newBytes := encodeRecord(record{attempts: tr.attempts, payload: tr.payload})

	txErr := q.store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		return tx.CompareAndSwap(ctx, namespace, msgKey(id), tr.knownBytes, newBytes)
	})
	switch {
	case txErr == nil:
		delete(st.receipts, receipt)
		tr.knownBytes, tr.claimed = newBytes, false
		// Push to the front: an explicit Nack is a deliberate "try this
		// again now" signal from the caller, distinct from a passive
		// visibility timeout (state.go's sweepExpiredLocked, which appends
		// to the back to preserve FIFO fairness among naturally expired
		// claims).
		st.ready = append([]string{id}, st.ready...)
		return nil
	case cascade.HasKind(txErr, cascade.KindConflict):
		return cascade.Newf(cascade.KindTimeout, "queue.Nack: receipt %q in namespace %q is stale or unknown", receipt, namespace)
	default:
		return cascade.Wrapf(cascade.KindUnavailable, txErr, "queue.Nack: releasing message %q in namespace %q", id, namespace)
	}
}
