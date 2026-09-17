package sync

import (
	"sort"
	"sync"
)

// Purpose (this file): the conflict journal — the record of every merge
//
//	that had to choose, and of what it chose against.
//
// WHY A LOSING WRITE IS NEVER JUST DROPPED. A server-primary merge's whole
//
//	point is that the server wins, which means somebody's local edit does
//	not. That is correct and it is also a small data loss, so the losing
//	side is written down with both sides' identities and content hashes.
//	An operator who wonders where their change went gets an answer, and one
//	who wonders whether the merge is doing the right thing can check.
//
// PROVENANCE, NOT JUST THE OUTCOME. Each entry names the domain, the
//
//	strategy that decided, both sides and the resolution. "config conflict"
//	with no strategy would leave a reader unable to tell a deliberate
//	server-primary overwrite from a bug.
//
// Inputs: entries from the merge strategies.
// Outputs: a queryable, deterministically ordered journal.
// SPORT: internal/sync conflict journal (ADD) — P1-E17-W4-S38-T2.

// Resolution is what a conflict's outcome was.
type Resolution string

const (
	// ResolutionServerWon means the server-primary side was kept and a
	// local write was discarded.
	ResolutionServerWon Resolution = "server-won"
	// ResolutionOrderWon means the total order picked a winner between
	// two peers with no server authority between them.
	ResolutionOrderWon Resolution = "order-won"
	// ResolutionTombstoneWon means a delete dominated a concurrent
	// update.
	ResolutionTombstoneWon Resolution = "tombstone-won"
	// ResolutionRefused means nothing was merged: the sides diverged in a
	// way this strategy will not resolve, and a person must.
	ResolutionRefused Resolution = "refused"
)

// Side is one party to a conflict, as the journal records it.
type Side struct {
	// NodeID is who wrote it.
	NodeID string
	// Revision and HLC are its position in the total order.
	Revision uint64
	HLC      uint64
	// Hash is the content hash, so the journal identifies WHAT was lost
	// and not only that something was. A reader with the hash can find
	// the bytes; a reader with a revision number cannot.
	Hash string
	// Ref is set instead of Hash for git-carried domains, where the thing
	// that diverged is a commit rather than a record.
	Ref string
}

// Conflict is one journaled merge decision.
type Conflict struct {
	// Domain and Subkind name what was merged.
	Domain  string
	Subkind string
	// Strategy is which merge decided, so a reader can tell a deliberate
	// server-primary overwrite from an unexpected one.
	Strategy StrategyName
	// RecordID identifies the record, or the ref name for git carriage.
	RecordID string
	// Winner and Loser are the two sides. On a refusal neither is a
	// winner, and Resolution says so.
	Winner Side
	Loser  Side
	// Resolution is the outcome.
	Resolution Resolution
	// Detail is one sentence for an operator, when the fields above do
	// not say enough on their own (a divergence, mostly).
	Detail string
}

// ConflictJournal is the queryable store `sync conflicts list` reads.
//
// In memory, and deliberately so at this layer: it is the merge's own
// record of what it decided, and the durable store behind it is the
// caller's to supply. Making it a concrete type here rather than an
// interface keeps the merges from each inventing a shape.
type ConflictJournal struct {
	mu      sync.Mutex
	entries []Conflict
}

// Record appends one conflict.
func (j *ConflictJournal) Record(c Conflict) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.entries = append(j.entries, c)
}

// List returns every journaled conflict, ordered deterministically by
// domain, subkind and record id.
//
// Ordered, not insertion-ordered: a merge walks a map, so insertion order
// is Go's iteration order, and a list surface whose order changes run to
// run is one no test can pin and no operator can diff.
func (j *ConflictJournal) List() []Conflict {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]Conflict, len(j.entries))
	copy(out, j.entries)
	sort.Slice(out, func(a, b int) bool {
		x, y := out[a], out[b]
		if x.Domain != y.Domain {
			return x.Domain < y.Domain
		}
		if x.Subkind != y.Subkind {
			return x.Subkind < y.Subkind
		}
		return x.RecordID < y.RecordID
	})
	return out
}

// Len reports how many conflicts are journaled.
func (j *ConflictJournal) Len() int {
	if j == nil {
		return 0
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.entries)
}
