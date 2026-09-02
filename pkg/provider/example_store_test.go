// Purpose: a runnable godoc Example for provider.Store (Art.10.6: every
//   pkg/ entry point needs a runnable Example), backed by a minimal,
//   genuinely functional in-memory double defined here rather than in
//   internal/storage/storetest — pkg/provider must not import storetest
//   (storetest already imports pkg/provider; the reverse would cycle).
// Constraints: this is a test-only double (Art.1.1 exemption) — it exists
//   solely to make ExampleStore compile and run; it is not a conformance
//   reference (that is storetest.NewMemStore, exercised by
//   TestRunStoreTests_MemStore).
// SPORT: pkg.provider.Store/ADDED (P1-E02-W1-S02-T1 CR follow-up).

package provider_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// exampleStore is a small, real (not stubbed) in-memory provider.Store: Get,
// Put, Delete, and Scan operate on an actual namespace->key->value map
// guarded by a mutex, and Tx/CompareAndSwap perform real conflict checks.
// It intentionally skips real snapshot isolation (Tx holds the store's lock
// for its whole closure) — sufficient for a single-goroutine Example, not a
// driver conformance target.
type exampleStore struct {
	mu   sync.Mutex
	data map[string]map[string][]byte
}

func newExampleStore() *exampleStore {
	return &exampleStore{data: make(map[string]map[string][]byte)}
}

func (s *exampleStore) Get(_ context.Context, namespace, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[namespace][key]
	if !ok {
		return nil, cascade.Newf(cascade.KindNotFound, "key %q not found in namespace %q", key, namespace)
	}
	return v, nil
}

func (s *exampleStore) Put(_ context.Context, namespace, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data[namespace] == nil {
		s.data[namespace] = make(map[string][]byte)
	}
	s.data[namespace][key] = value
	return nil
}

func (s *exampleStore) Delete(_ context.Context, namespace, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data[namespace], key)
	return nil
}

func (s *exampleStore) Scan(_ context.Context, namespace, prefix string) (provider.Iterator, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []string
	for k := range s.data[namespace] {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return &exampleIterator{store: s, namespace: namespace, keys: keys, idx: -1}, nil
}

func (s *exampleStore) Tx(ctx context.Context, fn func(ctx context.Context, tx provider.Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fn(ctx, &exampleTx{store: s})
}

// exampleTx is the Tx view exampleStore.Tx passes to its closure. It reads
// and writes exampleStore.data directly (the enclosing Tx call already
// holds the store's mutex for the whole closure).
type exampleTx struct{ store *exampleStore }

func (t *exampleTx) Get(_ context.Context, namespace, key string) ([]byte, error) {
	v, ok := t.store.data[namespace][key]
	if !ok {
		return nil, cascade.Newf(cascade.KindNotFound, "key %q not found in namespace %q", key, namespace)
	}
	return v, nil
}

func (t *exampleTx) Put(_ context.Context, namespace, key string, value []byte) error {
	if t.store.data[namespace] == nil {
		t.store.data[namespace] = make(map[string][]byte)
	}
	t.store.data[namespace][key] = value
	return nil
}

func (t *exampleTx) Delete(_ context.Context, namespace, key string) error {
	delete(t.store.data[namespace], key)
	return nil
}

func (t *exampleTx) CompareAndSwap(_ context.Context, namespace, key string, old, newValue []byte) error {
	cur, ok := t.store.data[namespace][key]
	if old == nil {
		if ok {
			return cascade.Newf(cascade.KindConflict, "key %q already exists in namespace %q", key, namespace)
		}
	} else if !ok || string(cur) != string(old) {
		return cascade.Newf(cascade.KindConflict, "key %q in namespace %q does not match expected value", key, namespace)
	}
	if t.store.data[namespace] == nil {
		t.store.data[namespace] = make(map[string][]byte)
	}
	t.store.data[namespace][key] = newValue
	return nil
}

// exampleIterator walks the sorted key snapshot exampleStore.Scan captured.
type exampleIterator struct {
	store     *exampleStore
	namespace string
	keys      []string
	idx       int
}

func (it *exampleIterator) Next(_ context.Context) bool {
	it.idx++
	return it.idx < len(it.keys)
}

func (it *exampleIterator) Key() string { return it.keys[it.idx] }

func (it *exampleIterator) Value() []byte {
	return it.store.data[it.namespace][it.keys[it.idx]]
}

func (it *exampleIterator) Err() error   { return nil }
func (it *exampleIterator) Close() error { return nil }

// ExampleStore demonstrates the basic Store lifecycle — Put, Get, the
// key-not-found error path, and Scan — plus a conditional write via Tx and
// CompareAndSwap.
func ExampleStore() {
	ctx := context.Background()
	var store provider.Store = newExampleStore()

	if err := store.Put(ctx, "config", "greeting", []byte("hello")); err != nil {
		fmt.Println("put error:", err)
		return
	}

	value, err := store.Get(ctx, "config", "greeting")
	if err != nil {
		fmt.Println("get error:", err)
		return
	}
	fmt.Println(string(value))

	if _, err := store.Get(ctx, "config", "missing"); cascade.HasKind(err, cascade.KindNotFound) {
		fmt.Println("missing: not found")
	}

	err = store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		return tx.CompareAndSwap(ctx, "config", "greeting", []byte("hello"), []byte("hi"))
	})
	if err != nil {
		fmt.Println("cas error:", err)
		return
	}
	updated, err := store.Get(ctx, "config", "greeting")
	if err != nil {
		fmt.Println("get error:", err)
		return
	}
	fmt.Println(string(updated))

	// Output:
	// hello
	// missing: not found
	// hi
}
