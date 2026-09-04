// Purpose: the store-fault and race paths of the namespace/model guard,
// plus the misbehaving-store doubles the dedupe suite shares.
//
// The doubles here fail or corrupt the pipeline's own bookkeeping rather
// than its vectors, because that bookkeeping is what the mixing refusal
// and the dedupe skip both rest on: if a broken store could make either
// silently answer "no record", the guard would be advisory.

package embed

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/localvector"
)

// brokenStore fails or corrupts the pipeline's bookkeeping reads and
// writes, so the guard's own error paths are exercised.
type brokenStore struct {
	provider.Store
	failGetIn string
	corruptIn string
	failTx    bool
	failPutIn string
}

func (b *brokenStore) Get(ctx context.Context, ns, key string) ([]byte, error) {
	if ns == b.failGetIn {
		return nil, cascade.New(cascade.KindUnavailable, "store is down")
	}
	if ns == b.corruptIn {
		return []byte("not json"), nil
	}
	return b.Store.Get(ctx, ns, key)
}

func (b *brokenStore) Put(ctx context.Context, ns, key string, value []byte) error {
	if ns == b.failPutIn {
		return cascade.New(cascade.KindUnavailable, "store is down")
	}
	return b.Store.Put(ctx, ns, key, value)
}

func (b *brokenStore) Tx(ctx context.Context, fn func(context.Context, provider.Tx) error) error {
	if b.failTx {
		return cascade.New(cascade.KindUnavailable, "store is down")
	}
	return b.Store.Tx(ctx, fn)
}

func newBrokenHarness(t *testing.T, meta provider.Store) *Pipeline {
	t.Helper()
	p, err := New(&fakeEmbedder{model: testModel}, localvector.New(storetest.NewMemStore()),
		meta, runtime.NewFixedClock(fixedInstant), 4)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func TestGuardPropagatesStoreFailures(t *testing.T) {
	cases := map[string]*brokenStore{
		"binding read fails":  {Store: storetest.NewMemStore(), failGetIn: modelNamespace},
		"binding is corrupt":  {Store: storetest.NewMemStore(), corruptIn: modelNamespace},
		"binding write fails": {Store: storetest.NewMemStore(), failTx: true},
	}
	wantKind := map[string]cascade.Kind{
		"binding read fails":  cascade.KindUnavailable,
		"binding is corrupt":  cascade.KindIntegrity,
		"binding write fails": cascade.KindUnavailable,
	}
	for name, store := range cases {
		t.Run(name, func(t *testing.T) {
			p := newBrokenHarness(t, store)
			_, err := p.Run(context.Background(), Request{
				Corpus: testCorpus("notes"), Chunks: chunks(1),
			})
			if !cascade.HasKind(err, wantKind[name]) {
				t.Fatalf("err = %v, want kind %v", err, wantKind[name])
			}
		})
	}
}

// racingStore runs a hook after the first Get in modelNamespace, so a
// test can let another writer win the conditional bind in the window
// between the guard's read and its write.
type racingStore struct {
	*storetest.MemStore
	onFirstGet func()
	fired      bool
}

func (r *racingStore) Get(ctx context.Context, ns, key string) ([]byte, error) {
	data, err := r.MemStore.Get(ctx, ns, key)
	if !r.fired && ns == modelNamespace {
		r.fired = true
		r.onFirstGet()
	}
	return data, err
}

func TestGuardRereadsAfterALostBindRace(t *testing.T) {
	store := storetest.NewMemStore()
	ns := fusion.NamespaceFor("notes")
	winner := provider.EmbedModel{ID: "winner-v1", Dimensions: 4}
	racer := &racingStore{MemStore: store}
	racer.onFirstGet = func() {
		if err := (namespaceGuard{store: store}).bind(context.Background(), ns, winner); err != nil {
			t.Errorf("the racing writer could not bind: %v", err)
		}
	}
	err := namespaceGuard{store: racer}.claim(context.Background(), ns, testModel)
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("err = %v, want KindConflict after losing the bind race", err)
	}
}

func TestGuardAcceptsARaceThatBoundTheSameModel(t *testing.T) {
	store := storetest.NewMemStore()
	ns := fusion.NamespaceFor("notes")
	racer := &racingStore{MemStore: store}
	racer.onFirstGet = func() {
		if err := (namespaceGuard{store: store}).bind(context.Background(), ns, testModel); err != nil {
			t.Errorf("the racing writer could not bind: %v", err)
		}
	}
	if err := (namespaceGuard{store: racer}).claim(context.Background(), ns, testModel); err != nil {
		t.Fatalf("claim = %v, want nil when the race bound the same model", err)
	}
}
