// Purpose: MemStore — a real, goroutine-safe in-memory reference
//   implementation of provider.Store, including Tx and
//   conditional-update/CAS semantics. It is the shared test substrate for
//   the Cache/Queue implementations (S-02.T4) and the localvector driver
//   (S-03.T4), not a stub (Art.1).
// Inputs: namespace/key/value triples via the provider.Store surface.
// Outputs: values, an Iterator, or a pkg/cascade taxonomy error — the same
//   contract any real driver must honor.
// Constraints: all state changes go through a single mutex, so a Tx holds
//   it for its whole closure — correct-by-construction over concurrent
//   access, at the cost of no intra-store parallelism (acceptable for a
//   reference/test implementation; production drivers are free to do
//   better). Tx failure rolls back via an undo log recorded as writes
//   happen, so a MemStore observed mid-Tx by nothing else (the mutex
//   forbids that) never shows a partial write once Tx returns.
// SPORT: internal.storage.storetest.MemStore/ADDED (P1-E02-W1-S02-T1).

package storetest

import (
	"bytes"
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// MemStore is a functional in-memory provider.Store. The zero value is not
// usable; construct with NewMemStore.
type MemStore struct {
	mu   sync.Mutex
	data map[string]map[string][]byte // namespace -> key -> value
}

// NewMemStore returns an empty, ready-to-use MemStore. It satisfies
// StoreFactory's shape (modulo the *testing.T parameter storetest's own
// tests don't need), so driver authors can wrap it directly:
// storetest.NewMemStore() implements provider.Store.
func NewMemStore() *MemStore {
	return &MemStore{data: make(map[string]map[string][]byte)}
}

// namespaceLocked returns (creating if absent) the key/value map for ns.
// Caller MUST hold m.mu.
func (m *MemStore) namespaceLocked(ns string) map[string][]byte {
	nsMap, ok := m.data[ns]
	if !ok {
		nsMap = make(map[string][]byte)
		m.data[ns] = nsMap
	}
	return nsMap
}

// Get implements provider.Store.
func (m *MemStore) Get(_ context.Context, namespace, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[namespace][key]
	if !ok {
		return nil, cascade.Newf(cascade.KindNotFound, "key %q not found in namespace %q", key, namespace)
	}
	return append([]byte(nil), v...), nil
}

// Put implements provider.Store.
func (m *MemStore) Put(_ context.Context, namespace, key string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.namespaceLocked(namespace)[key] = append([]byte(nil), value...)
	return nil
}

// Delete implements provider.Store.
func (m *MemStore) Delete(_ context.Context, namespace, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data[namespace], key)
	return nil
}

// Scan implements provider.Store. It returns a point-in-time snapshot taken
// under the store's lock, so concurrent writes after Scan returns never
// affect the returned Iterator.
func (m *MemStore) Scan(_ context.Context, namespace, prefix string) (provider.Iterator, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var keys []string
	for k := range m.data[namespace] {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	values := make([][]byte, len(keys))
	for i, k := range keys {
		values[i] = append([]byte(nil), m.data[namespace][k]...)
	}
	return &memIterator{keys: keys, values: values, pos: -1}, nil
}

// Tx implements provider.Store. It holds the store's mutex for fn's entire
// duration; a non-nil return from fn rolls back every write fn made through
// tx via the recorded undo log, in reverse order.
func (m *MemStore) Tx(ctx context.Context, fn func(ctx context.Context, tx provider.Tx) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	tx := &memTx{store: m}
	if err := fn(ctx, tx); err != nil {
		tx.rollback()
		return err
	}
	return nil
}

// memIterator implements provider.Iterator over a Scan snapshot.
type memIterator struct {
	keys   []string
	values [][]byte
	pos    int
}

func (it *memIterator) Next(context.Context) bool {
	it.pos++
	return it.pos < len(it.keys)
}

func (it *memIterator) Key() string {
	return it.keys[it.pos]
}

func (it *memIterator) Value() []byte {
	return it.values[it.pos]
}

func (it *memIterator) Err() error {
	return nil
}

func (it *memIterator) Close() error {
	return nil
}

// undoEntry records enough state to revert one write made during a Tx, in
// the reference implementation's rollback log.
type undoEntry struct {
	namespace string
	key       string
	hadValue  bool
	oldValue  []byte
}

// memTx implements provider.Tx against the MemStore whose mutex its
// enclosing Store.Tx call already holds. Writes apply directly to the
// store's map (there is no separate staging area); memTx's only job beyond
// that is recording undoEntry values so Store.Tx can roll back on error.
type memTx struct {
	store *MemStore
	undo  []undoEntry
}

// recordUndo captures key's pre-write state in namespace, once, before this
// memTx overwrites or deletes it for the first time in this transaction.
func (tx *memTx) recordUndo(namespace, key string) {
	current, existed := tx.store.data[namespace][key]
	entry := undoEntry{namespace: namespace, key: key, hadValue: existed}
	if existed {
		entry.oldValue = append([]byte(nil), current...)
	}
	tx.undo = append(tx.undo, entry)
}

// Get implements provider.Tx.
func (tx *memTx) Get(_ context.Context, namespace, key string) ([]byte, error) {
	v, ok := tx.store.data[namespace][key]
	if !ok {
		return nil, cascade.Newf(cascade.KindNotFound, "key %q not found in namespace %q", key, namespace)
	}
	return append([]byte(nil), v...), nil
}

// Put implements provider.Tx.
func (tx *memTx) Put(_ context.Context, namespace, key string, value []byte) error {
	tx.recordUndo(namespace, key)
	tx.store.namespaceLocked(namespace)[key] = append([]byte(nil), value...)
	return nil
}

// Delete implements provider.Tx.
func (tx *memTx) Delete(_ context.Context, namespace, key string) error {
	tx.recordUndo(namespace, key)
	delete(tx.store.data[namespace], key)
	return nil
}

// CompareAndSwap implements provider.Tx.
func (tx *memTx) CompareAndSwap(_ context.Context, namespace, key string, old, newValue []byte) error {
	current, exists := tx.store.data[namespace][key]
	switch {
	case old == nil && exists:
		return cascade.Newf(cascade.KindConflict, "key %q already exists in namespace %q", key, namespace)
	case old != nil && (!exists || !bytes.Equal(current, old)):
		return cascade.Newf(cascade.KindConflict, "value for key %q in namespace %q does not match expected", key, namespace)
	}
	tx.recordUndo(namespace, key)
	tx.store.namespaceLocked(namespace)[key] = append([]byte(nil), newValue...)
	return nil
}

// rollback reverts every write memTx made, in reverse order, restoring each
// key's pre-transaction state exactly once.
func (tx *memTx) rollback() {
	for i := len(tx.undo) - 1; i >= 0; i-- {
		e := tx.undo[i]
		if e.hadValue {
			tx.store.data[e.namespace][e.key] = e.oldValue
		} else {
			delete(tx.store.data[e.namespace], e.key)
		}
	}
}
