// Purpose: in-memory causal lineage for published events. A publisher that
//
//	acts on behalf of an earlier event (a hook's runner, today) carries a
//	Cause on its context; Publish records that Cause against the new
//	event's (namespace, Seq) so a later consumer can ask which root event
//	a chain started from and how deep it already is.
//
// Inputs: a context carrying a Cause (WithCause) at Publish time.
//
// Outputs: CauseOf answers for the newest causeTableSize published events
//
//	that carried a Cause; older entries are evicted oldest first.
//
// Constraints: lineage is memory only. It is never written to the Store and
//
//	never changes the persisted Event encoding (types.go), so a restart
//	forgets every chain. Consumers that bound work by lineage must treat
//	that as a documented limit, not as a guarantee across restarts.

package events

import "context"

// causeTableSize is the number of newest caused events CauseOf remembers.
const causeTableSize = 4096

// Cause is one event's position in a causal chain: the event the chain
// started from and how many hops separate this event from it.
type Cause struct {
	// RootNamespace and RootSeq identify the chain's first event.
	RootNamespace string
	RootSeq       uint64
	// Depth is the number of hops from the root: 0 for work done directly
	// on the root event.
	Depth int
}

// causeCtxKey is the private context key a Cause travels under.
type causeCtxKey struct{}

// WithCause returns a copy of ctx carrying c. Publish records it against
// every event published with the returned context.
func WithCause(ctx context.Context, c Cause) context.Context {
	return context.WithValue(ctx, causeCtxKey{}, c)
}

// CauseFrom returns the Cause ctx carries, if any.
func CauseFrom(ctx context.Context) (Cause, bool) {
	c, ok := ctx.Value(causeCtxKey{}).(Cause)
	return c, ok
}

// causeKey identifies one published event.
type causeKey struct {
	namespace string
	seq       uint64
}

// causeTable is a bounded map from event to Cause. order is a ring of the
// keys in insertion order; next is the slot the next insert overwrites once
// the ring is full. Both are allocated on first use, so a Bus that never
// sees a caused publish pays nothing. It is guarded by the owning Bus's mu.
type causeTable struct {
	entries map[causeKey]Cause
	order   []causeKey
	count   int
	next    int
}

// record stores c for k, evicting the oldest entry when the table is full.
// Caller MUST hold the owning Bus's mu.
func (t *causeTable) record(k causeKey, c Cause) {
	if t.entries == nil {
		t.entries = make(map[causeKey]Cause)
		t.order = make([]causeKey, causeTableSize)
	}
	if t.count == causeTableSize {
		delete(t.entries, t.order[t.next])
	} else {
		t.count++
	}
	t.order[t.next] = k
	t.next = (t.next + 1) % causeTableSize
	t.entries[k] = c
}

// CauseOf returns the Cause recorded for the event at seq in namespace. It
// reports false for an event published without a Cause, one this process
// never published, and one evicted from the table.
func (b *Bus) CauseOf(namespace string, seq uint64) (Cause, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.causes.entries[causeKey{namespace: namespace, seq: seq}]
	return c, ok
}
