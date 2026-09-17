package sync

// Purpose (this file): the total order two copies of one synced record are
//
//	compared by — and, just as importantly, what is NOT compared.
//
// WALL TIME IS NEVER COMPARED (R-21.223). The ordering is the
//
//	server-assigned monotonic revision, then the persisted hybrid logical
//	clock, then the writer's node id. Wall time is carried for provenance
//	and nothing reads it for a decision, because a laptop whose clock is a
//	day fast would otherwise win every conflict against the server for a
//	day — silently, and in the direction that loses the authoritative copy.
//
// IT IS A TOTAL ORDER, and that is load-bearing rather than tidy: taking
//
//	the maximum per record id is commutative, associative and idempotent by
//	construction, which is what makes a merge converge no matter which peer
//	runs it or in what order. The node-id tie-break exists so two writers
//	with the same revision and the same HLC still have a deterministic
//	answer; without it the merge would be non-deterministic exactly where
//	two peers disagree.
//
// Inputs: two records' ordering keys.
// Outputs: >0, 0 or <0.
// SPORT: internal/sync ordering (ADD) — P1-E17-W4-S38-T2.

// OrderKey is the ordering triple every synced record carries.
type OrderKey struct {
	// Revision is the server-assigned monotonic revision. It is the
	// primary key precisely because the server assigns it: it cannot be
	// forged forward by a peer the way a clock reading can.
	Revision uint64
	// HLC is the persisted hybrid logical clock, the causal tie-break
	// between two writers at the same revision.
	HLC uint64
	// NodeID is the writer, and the final deterministic tie-break.
	NodeID string
	// WallClock is unix seconds, carried for PROVENANCE ONLY. Nothing
	// compares it. It is in this struct rather than omitted so that an
	// operator reading a journaled conflict can see what each side
	// believed the time was — which is how a skewed clock gets noticed.
	WallClock int64
}

// Compare returns >0 when a strictly outranks b, 0 when they are identical
// under this ordering, and <0 otherwise.
func Compare(a, b OrderKey) int {
	switch {
	case a.Revision != b.Revision:
		return sign(a.Revision, b.Revision)
	case a.HLC != b.HLC:
		return sign(a.HLC, b.HLC)
	case a.NodeID != b.NodeID:
		if a.NodeID > b.NodeID {
			return 1
		}
		return -1
	default:
		return 0
	}
}

// sign returns 1 when a > b and -1 otherwise. Callers check equality first.
func sign(a, b uint64) int {
	if a > b {
		return 1
	}
	return -1
}
