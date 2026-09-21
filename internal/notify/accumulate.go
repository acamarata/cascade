// Purpose: queueSet's away-mode accumulation half — the gate every producer
//
//	passes through (enqueue), its on/off switch, the buffer's atomic drain,
//	its length probe, and the in-place stall reclassification. Split out of
//	dispatch.go so this ticket's addition to the S-49.T1 dispatcher lives in
//	its own file instead of pushing dispatch.go against Art.10.3's 300-line
//	cap (declared in docs/architecture.md § Away mode, files_scope
//	deviation).
//
// Inputs: every Notification any producer emits — router.go's bus-decode
//
//	path, Notifier.Deliver (inbox.go) and Dispatcher.Drain's own retry
//	re-queue all arrive here, because enqueue is the one call they share.
//
// Outputs: with the gate off, n continues to enqueueNow (dispatch.go) and
//
//	its priority queue; with the gate on, n is buffered until
//	drainAccumulated takes it.
//
// Constraints:
//   - Switching the gate off does NOT flush the buffer. drainAccumulated is
//     the only way an accumulated notification leaves it, so the away
//     controller's return transition drains as well as flips, inside the
//     same critical section (away.go observeActivityLocked).
//   - enqueueNow deliberately BYPASSES this gate, for the two callers that
//     must not re-accumulate: router.requeue (a re-queue must land in the
//     queue it is being returned to) and Notifier.DeliverNow (an
//     already-closed episode's return digest must not be buffered into the
//     next episode and reduced to a count inside it).
//
// SPORT: internal.notify.Dispatcher/CHANGED (P1-E23-W5-S49-T2).

package notify

import "sort"

// enqueue is the accumulation gate every producer passes through: while
// away-mode accumulation is on, n is buffered instead of queued, so a
// Dispatcher.Drain sees nothing new to fan out no matter which producer
// emitted n. It is deliberately the ONLY enqueue path router.go and
// Notifier.Deliver call.
func (q *queueSet) enqueue(n Notification) {
	q.accumMu.Lock()
	if q.accumulating {
		q.accumBuf = append(q.accumBuf, n)
		q.accumMu.Unlock()
		return
	}
	q.accumMu.Unlock()
	q.enqueueNow(n)
}

// setAccumulate switches the gate on or off. Switching it off does NOT
// flush the buffer: drainAccumulated is the only way accumulated items
// leave it.
func (q *queueSet) setAccumulate(accumulate bool) {
	q.accumMu.Lock()
	defer q.accumMu.Unlock()
	q.accumulating = accumulate
}

// isAccumulating reports whether the gate is currently buffering.
func (q *queueSet) isAccumulating() bool {
	q.accumMu.Lock()
	defer q.accumMu.Unlock()
	return q.accumulating
}

// accumulatedLen reports how many notifications are buffered right now,
// without draining them. It exists for the atomicity proof: sampled at the
// instant the return transition journals its KindAck — i.e. from inside that
// transition's own critical section — it is 0 only if the drain really
// happened in there, and not after the unlock.
func (q *queueSet) accumulatedLen() int {
	q.accumMu.Lock()
	defer q.accumMu.Unlock()
	return len(q.accumBuf)
}

// drainAccumulated atomically returns every buffered notification in
// priority order (Urgent, High, Normal, Low; stable within a priority so
// arrival order is preserved) and clears the buffer. It does not change the
// accumulating flag.
func (q *queueSet) drainAccumulated() []Notification {
	q.accumMu.Lock()
	defer q.accumMu.Unlock()
	out := make([]Notification, len(q.accumBuf))
	copy(out, q.accumBuf)
	q.accumBuf = nil
	sort.SliceStable(out, func(i, j int) bool { return out[i].Priority < out[j].Priority })
	return out
}

// reclassifyAccumulated sets Priority to Urgent on every still-buffered
// notification whose ID or CorrelationID equals matchID, in place, and
// returns how many it changed. An empty matchID matches nothing and
// returns 0, which the caller reports rather than swallowing.
func (q *queueSet) reclassifyAccumulated(matchID string) int {
	if matchID == "" {
		return 0
	}
	q.accumMu.Lock()
	defer q.accumMu.Unlock()
	n := 0
	for i := range q.accumBuf {
		if q.accumBuf[i].ID == matchID || q.accumBuf[i].CorrelationID == matchID {
			q.accumBuf[i].Priority = PriorityUrgent
			n++
		}
	}
	return n
}
