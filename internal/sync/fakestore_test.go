package sync

// Purpose: a minimal in-memory provider.Store double, mirroring
//   internal/fleet/journal's fakeStore_test.go precedent exactly (same
//   package-private-double pattern), so cursor.go/filter.go/staging.go's
//   unit tests exercise their real logic without a real SQLite driver.
// Constraints: no isolation semantics — Tx runs fn directly against the
//   same shared map.
// SPORT: internal.sync (tests only).

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

type fakeStore struct {
	mu       sync.Mutex
	data     map[string][]byte
	failPut  bool
	failScan bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{data: make(map[string][]byte)}
}

func fkey(namespace, key string) string { return namespace + "\x00" + key }

func (f *fakeStore) Get(_ context.Context, namespace, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.data[fkey(namespace, key)]
	if !ok {
		return nil, cascade.New(cascade.KindNotFound, "fakeStore: not found")
	}
	return v, nil
}

func (f *fakeStore) Put(_ context.Context, namespace, key string, value []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failPut {
		return cascade.New(cascade.KindUnavailable, "fakeStore: put failed (injected)")
	}
	f.data[fkey(namespace, key)] = append([]byte(nil), value...)
	return nil
}

func (f *fakeStore) Delete(_ context.Context, namespace, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.data, fkey(namespace, key))
	return nil
}

func (f *fakeStore) Scan(_ context.Context, namespace, prefix string) (provider.Iterator, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failScan {
		return nil, cascade.New(cascade.KindUnavailable, "fakeStore: scan failed (injected)")
	}
	var keys []string
	for k := range f.data {
		parts := strings.SplitN(k, "\x00", 2)
		if len(parts) == 2 && parts[0] == namespace && strings.HasPrefix(parts[1], prefix) {
			keys = append(keys, parts[1])
		}
	}
	sort.Strings(keys)
	return &fakeIterator{store: f, namespace: namespace, keys: keys}, nil
}

func (f *fakeStore) Tx(ctx context.Context, fn func(ctx context.Context, tx provider.Tx) error) error {
	return fn(ctx, &fakeTx{store: f})
}

type fakeIterator struct {
	store     *fakeStore
	namespace string
	keys      []string
	idx       int
	cur       string
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
func (it *fakeIterator) Err() error   { return nil }
func (it *fakeIterator) Close() error { return nil }

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

// fakeClock is a fixed injected Clock (Art.7.3).
type fakeClock struct{ t time.Time }

func (c fakeClock) Now() time.Time { return c.t }

var (
	_ provider.Store    = (*fakeStore)(nil)
	_ provider.Tx       = (*fakeTx)(nil)
	_ provider.Iterator = (*fakeIterator)(nil)
)
