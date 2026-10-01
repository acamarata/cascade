// Purpose: the in-memory per-namespace ordering/claim-tracking state
//   Queue's exported methods (queue.go, ack.go) operate on, plus
//   the "prior bytes" baseline every Store.Tx
//   transition CAS/delete-fences against. A trackedRecord's knownBytes is
//   this Queue instance's belief about what is currently persisted at its
//   Store key, updated ONLY after a transition's Store.Tx has actually
//   committed, never before -- the exact ordering fix for the audit's
//   "retry must not become accidentally stale while the body is
//   stranded" finding: a failed Store operation must not first mutate
//   in-memory reachability. persistence.go populates this state from
//   Store the first time a Queue instance touches a namespace (recovery);
//   nothing in this file reads or writes Store directly.
// Constraints: no bare time.Now (all deadline comparisons take clock.Now()
//   from the caller, an injected runtime.Clock); not goroutine-safe on its
//   own -- Queue's mutex (queue.go) serializes every access.
// SPORT: internal.storage.queue.Queue/CHANGED.

package queue

import (
	"context"
	"sort"
	"time"
)

// trackedRecord is everything namespaceState remembers about one message
// id: its logical claim state, the exact bytes this Queue instance
// believes Store currently holds for it (knownBytes -- every later
// transition's CAS/delete-fencing "prior bytes"), and enough decoded
// fields (attempts, payload, receipt, deadline) to build the next
// transition's bytes without a redundant Store.Get.
type trackedRecord struct {
	knownBytes []byte
	attempts   uint32
	payload    []byte
	claimed    bool
	receipt    string    // valid iff claimed
	deadline   time.Time // valid iff claimed
}

// namespaceState is one namespace's ordering and claim-tracking state,
// populated once per Queue instance by persistence.go's recoverNamespace.
type namespaceState struct {
	ready    []string                  // FIFO order of ready (or expired-claimed) ids
	records  map[string]*trackedRecord // every id this Queue instance currently tracks
	receipts map[string]string         // receipt -> id, for currently-claimed ids only
}

func newNamespaceState() *namespaceState {
	return &namespaceState{
		records:  make(map[string]*trackedRecord),
		receipts: make(map[string]string),
	}
}

// namespaceLocked returns ns's tracking state, recovering it from Store
// (persistence.go's recoverNamespace) the first time this Queue instance
// touches ns. Caller MUST hold Queue.mu.
func (q *Queue) namespaceLocked(ctx context.Context, ns string) (*namespaceState, error) {
	if st, ok := q.ns[ns]; ok {
		return st, nil
	}
	st, err := recoverNamespace(ctx, q.store, q.clock, ns)
	if err != nil {
		return nil, err
	}
	q.ns[ns] = st
	return st, nil
}

// trackRecovered records one record recoverNamespace decoded (or
// migrated) for id, in Scan's key order: a claimed record whose deadline
// has not yet passed as of now goes to inflight tracking; everything else
// (never claimed, or a claim whose deadline already passed while this
// process was down) becomes ready, preserving Scan's key order -- which
// is original enqueue order (persistence.go's own doc comment).
func (st *namespaceState) trackRecovered(id string, rec record, knownBytes []byte, now time.Time) {
	tr := &trackedRecord{knownBytes: knownBytes, attempts: rec.attempts, payload: rec.payload}
	if rec.claimed && now.Before(rec.deadline) {
		tr.claimed = true
		tr.receipt = rec.receipt
		tr.deadline = rec.deadline
		st.receipts[rec.receipt] = id
	} else {
		st.ready = append(st.ready, id)
	}
	st.records[id] = tr
}

// sweepExpiredLocked moves every tracked record in ns whose claim deadline
// has passed as of now back onto the ready queue, without touching Store
// -- knownBytes still (correctly) reflects the claimed record Store
// actually holds; the next successful claim CAS-overwrites it from
// exactly those bytes. Caller MUST hold Queue.mu.
func (st *namespaceState) sweepExpiredLocked(now time.Time) {
	var expired []string
	for id, tr := range st.records {
		if tr.claimed && !now.Before(tr.deadline) {
			expired = append(expired, id)
		}
	}
	// Map iteration order is randomized; sort so requeue order is
	// deterministic across runs (a flaky gate is not allowed).
	sort.Strings(expired)
	for _, id := range expired {
		tr := st.records[id]
		delete(st.receipts, tr.receipt)
		tr.claimed = false
		st.ready = append(st.ready, id)
	}
}
