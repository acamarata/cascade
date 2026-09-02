// Purpose: Queue.Ack and Queue.Nack — the two receipt-consuming halves of
//   provider.Queue, split out from queue.go (Enqueue/Dequeue) to keep each
//   file under Art.10.3's 300-line cap (R-14.117 authorizes in-package
//   splits for exactly this reason).
// SPORT: internal.storage.queue.Queue/ADDED (P1-E02-W1-S02-T4).

package queue

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Ack implements provider.Queue.
func (q *Queue) Ack(ctx context.Context, namespace, receipt string) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	st := q.namespaceLocked(namespace)
	st.sweepExpiredLocked(q.clock.Now())

	id, ok := st.receipts[receipt]
	if !ok {
		return cascade.Newf(cascade.KindTimeout, "queue.Ack: receipt %q in namespace %q is stale or unknown", receipt, namespace)
	}
	st.releaseClaim(id)
	if err := q.store.Delete(ctx, namespace, msgKey(id)); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "queue.Ack: removing message %q in namespace %q", id, namespace)
	}
	return nil
}

// Nack implements provider.Queue.
func (q *Queue) Nack(_ context.Context, namespace, receipt string) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	st := q.namespaceLocked(namespace)
	st.sweepExpiredLocked(q.clock.Now())

	id, ok := st.receipts[receipt]
	if !ok {
		return cascade.Newf(cascade.KindTimeout, "queue.Nack: receipt %q in namespace %q is stale or unknown", receipt, namespace)
	}
	st.releaseClaim(id)
	// Push to the front: an explicit Nack is a deliberate "try this again
	// now" signal from the caller, distinct from a passive visibility
	// timeout (state.go's sweepExpiredLocked, which appends to the back to
	// preserve FIFO fairness among naturally expired claims).
	st.ready = append([]string{id}, st.ready...)
	return nil
}
