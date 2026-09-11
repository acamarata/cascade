package journal

// Purpose: a minimal in-memory provider.Store double used only to inject
//
//	storage failures the real SQLite driver cannot deterministically
//	produce (a Put or Delete failing at one specific key while Get still
//	succeeds elsewhere, or a Get failing on the very first recovery scan
//	for a brand-new entity). Everything else behaves like a real store so
//	a test using it still exercises this package's real logic, not the
//	double's.
//
// Constraints: no isolation semantics — Tx runs fn directly against the
//
//	same shared map, so no test using this double may depend on rollback.
//	Every failure-producing test below only needs one write to fail and
//	observes the propagated error, never a partial-vs-rolled-back state.
//
// SPORT: internal.fleet.journal.Store/ADDED (tests).

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// fakeStore is the provider.Store double. The zero value is not usable;
// construct with newFakeStore.
type fakeStore struct {
	mu   sync.Mutex
	data map[string][]byte

	failGet    map[string]error
	failPut    map[string]error
	failDelete map[string]error
	failScan   error
	iterErr    error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		data:       make(map[string][]byte),
		failGet:    make(map[string]error),
		failPut:    make(map[string]error),
		failDelete: make(map[string]error),
	}
}

func fkey(namespace, key string) string { return namespace + "\x00" + key }

func (f *fakeStore) Get(_ context.Context, namespace, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.failGet[key]; ok {
		return nil, err
	}
	v, ok := f.data[fkey(namespace, key)]
	if !ok {
		return nil, cascade.New(cascade.KindNotFound, "fakeStore: not found")
	}
	return v, nil
}

func (f *fakeStore) Put(_ context.Context, namespace, key string, value []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.failPut[key]; ok {
		return err
	}
	f.data[fkey(namespace, key)] = append([]byte(nil), value...)
	return nil
}

func (f *fakeStore) Delete(_ context.Context, namespace, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.failDelete[key]; ok {
		return err
	}
	delete(f.data, fkey(namespace, key))
	return nil
}

func (f *fakeStore) Scan(_ context.Context, namespace, prefix string) (provider.Iterator, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failScan != nil {
		return nil, f.failScan
	}
	var keys []string
	for k := range f.data {
		parts := strings.SplitN(k, "\x00", 2)
		if len(parts) == 2 && parts[0] == namespace && strings.HasPrefix(parts[1], prefix) {
			keys = append(keys, parts[1])
		}
	}
	sort.Strings(keys)
	return &fakeIterator{store: f, namespace: namespace, keys: keys, iterErr: f.iterErr}, nil
}

func (f *fakeStore) Tx(ctx context.Context, fn func(ctx context.Context, tx provider.Tx) error) error {
	return fn(ctx, &fakeTx{store: f})
}

// fakeIterator is the provider.Iterator double Scan returns. iterErr, when
// set, is surfaced from Err() after Next() reports no more results, the
// same shape a real iterator uses to report a mid-scan failure.
type fakeIterator struct {
	store     *fakeStore
	namespace string
	keys      []string
	idx       int
	cur       string
	iterErr   error
}

func (it *fakeIterator) Next(_ context.Context) bool {
	if it.idx >= len(it.keys) {
		return false
	}
	it.cur = it.keys[it.idx]
	it.idx++
	return true
}

func (it *fakeIterator) Key() string { return it.cur }

func (it *fakeIterator) Value() []byte {
	it.store.mu.Lock()
	defer it.store.mu.Unlock()
	return it.store.data[fkey(it.namespace, it.cur)]
}

func (it *fakeIterator) Err() error   { return it.iterErr }
func (it *fakeIterator) Close() error { return nil }

// fakeTx implements provider.Tx over the same fakeStore map.
type fakeTx struct{ store *fakeStore }

func (tx *fakeTx) Get(ctx context.Context, namespace, key string) ([]byte, error) {
	return tx.store.Get(ctx, namespace, key)
}

func (tx *fakeTx) Put(ctx context.Context, namespace, key string, value []byte) error {
	return tx.store.Put(ctx, namespace, key, value)
}

func (tx *fakeTx) Delete(ctx context.Context, namespace, key string) error {
	return tx.store.Delete(ctx, namespace, key)
}

func (tx *fakeTx) CompareAndSwap(ctx context.Context, namespace, key string, old, newValue []byte) error {
	tx.store.mu.Lock()
	cur, ok := tx.store.data[fkey(namespace, key)]
	tx.store.mu.Unlock()
	if old == nil {
		if ok {
			return cascade.New(cascade.KindConflict, "fakeStore: key already exists")
		}
	} else if !ok || string(cur) != string(old) {
		return cascade.New(cascade.KindConflict, "fakeStore: value mismatch")
	}
	return tx.store.Put(ctx, namespace, key, newValue)
}

var (
	_ provider.Store    = (*fakeStore)(nil)
	_ provider.Tx       = (*fakeTx)(nil)
	_ provider.Iterator = (*fakeIterator)(nil)
)
