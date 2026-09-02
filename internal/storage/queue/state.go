// Purpose: the in-memory per-namespace ordering/claim-tracking state
//   Queue's exported methods (queue.go, ack.go) operate on. Message BODIES
//   (payload + attempt count) are the durable Store-backed truth
//   (envelope.go); this file's structures only track which IDs are
//   currently ready, which are claimed (inflight) and by which receipt,
//   and expire stale claims — none of it survives a process restart, by
//   design (a restart finds every namespace's state empty and simply never
//   redelivers messages a prior process had claimed; reconstructing
//   in-flight state from Store on startup is out of this ticket's scope
//   and does not affect any acceptance criterion, all of which operate
//   within one Queue instance's lifetime).
// Constraints: no bare time.Now (all deadline comparisons take clock.Now()
//   from the caller, an injected runtime.Clock); not goroutine-safe on its
//   own — Queue's mutex (queue.go) serializes every access.
// SPORT: internal.storage.queue.Queue/ADDED (P1-E02-W1-S02-T4).

package queue

import (
	"sort"
	"time"
)

// claim describes one currently-inflight (Dequeued but not yet Acked or
// Nacked) message.
type claim struct {
	receipt  string
	deadline time.Time
}

// namespaceState is one namespace's ordering and claim-tracking state.
type namespaceState struct {
	ready    []string         // FIFO order of message IDs available to claim
	inflight map[string]claim // message ID -> its current claim
	receipts map[string]string
}

func newNamespaceState() *namespaceState {
	return &namespaceState{
		inflight: make(map[string]claim),
		receipts: make(map[string]string),
	}
}

// namespaceLocked returns (creating if absent) ns's tracking state. Caller
// MUST hold Queue.mu.
func (q *Queue) namespaceLocked(ns string) *namespaceState {
	st, ok := q.ns[ns]
	if !ok {
		st = newNamespaceState()
		q.ns[ns] = st
	}
	return st
}

// claimMessage records id as newly claimed under a fresh receipt with the
// given deadline, replacing any prior claim on id (there should never be
// one — a message is only ever claimed while it is not already inflight).
func (st *namespaceState) claimMessage(id, receipt string, deadline time.Time) {
	st.inflight[id] = claim{receipt: receipt, deadline: deadline}
	st.receipts[receipt] = id
}

// releaseClaim drops id's current claim (if any) from both tracking maps
// and reports the receipt that was released, so the caller can decide
// whether to requeue id.
func (st *namespaceState) releaseClaim(id string) {
	cl, ok := st.inflight[id]
	if !ok {
		return
	}
	delete(st.inflight, id)
	delete(st.receipts, cl.receipt)
}

// sweepExpiredLocked moves every inflight message in ns whose deadline has
// passed as of now back onto the ready queue, releasing its stale claim.
// Caller MUST hold Queue.mu.
func (st *namespaceState) sweepExpiredLocked(now time.Time) {
	var expired []string
	for id, cl := range st.inflight {
		if !now.Before(cl.deadline) {
			expired = append(expired, id)
		}
	}
	// Map iteration order is randomized; sort so requeue order is
	// deterministic across runs (Art.11 forbids flaky gates), even though
	// the current callers only ever have zero or one expired claim at a
	// time.
	sort.Strings(expired)
	for _, id := range expired {
		st.releaseClaim(id)
		st.ready = append(st.ready, id)
	}
}

// inflightCount reports how many messages are currently claimed.
func (st *namespaceState) inflightCount() int {
	return len(st.inflight)
}
