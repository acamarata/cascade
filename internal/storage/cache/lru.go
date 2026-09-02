// Purpose: the in-memory LRU recency/capacity index Cache uses to decide
//   which entries to evict under item-count or byte-ceiling pressure. The
//   index tracks (namespace,key) -> size only; the value itself lives in
//   provider.Store (Cache's persistence layer), so the index losing an
//   entry (process restart, a second Cache instance over the same Store)
//   never loses data — Get always re-derives from Store and self-heals the
//   index on a hit (see cache.go's Get).
// Constraints: container/list + map only, no external LRU library
//   (ticket's own task text: "doubly linked list + map, no external lib").
//   Not goroutine-safe on its own; Cache's mutex (cache.go) serializes
//   every call into this type.
// SPORT: internal.storage.cache.Cache/ADDED (P1-E02-W1-S02-T4).

package cache

import "container/list"

// lruKey identifies one tracked entry across every namespace a Cache
// instance serves.
type lruKey struct {
	namespace string
	key       string
}

// lruEntry is the container/list element payload: which key this is and
// how many bytes its value occupies, for the byte-ceiling accounting.
type lruEntry struct {
	key  lruKey
	size int64
}

// lruIndex is the doubly-linked-list-plus-map recency index. The zero
// value is not usable; construct with newLRUIndex.
type lruIndex struct {
	order    *list.List // front = most recently used, back = least
	elements map[lruKey]*list.Element
	bytes    int64
}

func newLRUIndex() *lruIndex {
	return &lruIndex{
		order:    list.New(),
		elements: make(map[lruKey]*list.Element),
	}
}

// touch records k as most-recently-used with the given size, inserting it
// if absent or moving+resizing it if already tracked. It returns the
// number of items and total bytes now under tracking.
func (l *lruIndex) touch(k lruKey, size int64) {
	if el, ok := l.elements[k]; ok {
		l.bytes += size - el.Value.(*lruEntry).size
		el.Value.(*lruEntry).size = size
		l.order.MoveToFront(el)
		return
	}
	el := l.order.PushFront(&lruEntry{key: k, size: size})
	l.elements[k] = el
	l.bytes += size
}

// remove drops k from tracking, if present. Removing an untracked key is a
// no-op.
func (l *lruIndex) remove(k lruKey) {
	el, ok := l.elements[k]
	if !ok {
		return
	}
	l.bytes -= el.Value.(*lruEntry).size
	l.order.Remove(el)
	delete(l.elements, k)
}

// removeNamespace drops every key tracked under namespace.
func (l *lruIndex) removeNamespace(namespace string) {
	for k := range l.elements {
		if k.namespace == namespace {
			l.remove(k)
		}
	}
}

// len reports how many entries are currently tracked.
func (l *lruIndex) len() int {
	return len(l.elements)
}

// oldest returns the least-recently-used key, and false if the index is
// empty.
func (l *lruIndex) oldest() (lruKey, bool) {
	back := l.order.Back()
	if back == nil {
		return lruKey{}, false
	}
	return back.Value.(*lruEntry).key, true
}
