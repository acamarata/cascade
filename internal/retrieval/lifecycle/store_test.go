package lifecycle_test

// Purpose: TestRecallIndex-prefixed tests exercising this package's
// store-layer error paths (Get/Put/Scan failures other than "not found")
// via a selective-failure provider.Store wrapper, raising branch coverage
// toward the Art.4 core-engine floor.
//
// SPORT: internal.retrieval.lifecycle.Manager/ADDED (P1-E06-W2-S11-T4).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/lifecycle"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// flakyStore wraps a real provider.Store, failing Get/Put/Delete/Scan
// calls whose key contains failOn (empty means never fail that verb).
type flakyStore struct {
	provider.Store
	failGetOn, failPutOn, failDeleteOn, failScanOn string
}

func (f flakyStore) Get(ctx context.Context, ns, key string) ([]byte, error) {
	if f.failGetOn != "" && strings.Contains(key, f.failGetOn) {
		return nil, cascade.New(cascade.KindUnavailable, "flaky get")
	}
	return f.Store.Get(ctx, ns, key)
}

func (f flakyStore) Put(ctx context.Context, ns, key string, value []byte) error {
	if f.failPutOn != "" && strings.Contains(key, f.failPutOn) {
		return cascade.New(cascade.KindUnavailable, "flaky put")
	}
	return f.Store.Put(ctx, ns, key, value)
}

func (f flakyStore) Delete(ctx context.Context, ns, key string) error {
	if f.failDeleteOn != "" && strings.Contains(key, f.failDeleteOn) {
		return cascade.New(cascade.KindUnavailable, "flaky delete")
	}
	return f.Store.Delete(ctx, ns, key)
}

func (f flakyStore) Scan(ctx context.Context, ns, prefix string) (provider.Iterator, error) {
	if f.failScanOn != "" && strings.Contains(prefix, f.failScanOn) {
		return nil, cascade.New(cascade.KindUnavailable, "flaky scan")
	}
	return f.Store.Scan(ctx, ns, prefix)
}

// buildFlaky builds a Manager identical to h.manager but backed by flaky.
func buildFlaky(t *testing.T, h *harness, flaky provider.Store, sources []lifecycle.Source) *lifecycle.Manager {
	t.Helper()
	m, err := lifecycle.NewManager(lifecycle.ManagerOptions{
		CatalogPath: h.catalogPath, Store: flaky, Index: h.index, Vectors: h.vectors,
		Pipeline: h.pipeline, Sources: fixedSource{sources: sources}, TreeHash: h.hash.Get, Clock: h.clock,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

// TestRecallIndexRebuildManifestWriteErrorSurfaces proves a failing
// manifest Put surfaces from Rebuild.
func TestRecallIndexRebuildManifestWriteErrorSurfaces(t *testing.T) {
	h := newHarness(t)
	flaky := flakyStore{Store: h.store, failPutOn: "lifecycle:manifest:"}
	m := buildFlaky(t, h, flaky, oneSource("a.md", "# T\n\nbody"))
	if _, err := m.Rebuild(context.Background()); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("want the manifest write error surfaced, got %v", err)
	}
}

// TestRecallIndexRebuildCorporaIndexWriteErrorSurfaces proves a failing
// "lifecycle:corpora" write surfaces from Rebuild.
func TestRecallIndexRebuildCorporaIndexWriteErrorSurfaces(t *testing.T) {
	h := newHarness(t)
	flaky := flakyStore{Store: h.store, failPutOn: "lifecycle:corpora"}
	m := buildFlaky(t, h, flaky, oneSource("a.md", "# T\n\nbody"))
	if _, err := m.Rebuild(context.Background()); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("want the corpus-index write error surfaced, got %v", err)
	}
}

// TestRecallIndexRebuildMarkerWriteErrorSurfaces proves a failing
// generation-marker write surfaces from Rebuild.
func TestRecallIndexRebuildMarkerWriteErrorSurfaces(t *testing.T) {
	h := newHarness(t)
	flaky := flakyStore{Store: h.store, failPutOn: "lifecycle:generation"}
	m := buildFlaky(t, h, flaky, oneSource("a.md", "# T\n\nbody"))
	if _, err := m.Rebuild(context.Background()); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("want the marker write error surfaced, got %v", err)
	}
}

// TestRecallIndexRebuildKnownCorporaReadErrorSurfaces proves a failing
// "lifecycle:corpora" read surfaces from Rebuild (allManifestIDs' first
// call).
func TestRecallIndexRebuildKnownCorporaReadErrorSurfaces(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	m := h.manager(oneSource("a.md", "# T\n\nbody"))
	if _, err := m.Rebuild(ctx); err != nil {
		t.Fatalf("seed rebuild: %v", err)
	}
	flaky := flakyStore{Store: h.store, failGetOn: "lifecycle:corpora"}
	m2 := buildFlaky(t, h, flaky, oneSource("a.md", "# T\n\nbody"))
	if _, err := m2.Rebuild(ctx); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("want the corpus-index read error surfaced, got %v", err)
	}
}

// TestRecallIndexRebuildManifestReadErrorSurfaces proves a failing
// per-corpus manifest read surfaces from Rebuild.
func TestRecallIndexRebuildManifestReadErrorSurfaces(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	m := h.manager(oneSource("a.md", "# T\n\nbody"))
	if _, err := m.Rebuild(ctx); err != nil {
		t.Fatalf("seed rebuild: %v", err)
	}
	flaky := flakyStore{Store: h.store, failGetOn: "lifecycle:manifest:"}
	m2 := buildFlaky(t, h, flaky, oneSource("a.md", "# T\n\nnew body"))
	if _, err := m2.Rebuild(ctx); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("want the manifest read error surfaced, got %v", err)
	}
}

// TestRecallIndexVerifyScanErrorSurfaces proves a failing FTS5 leg scan
// surfaces from Verify rather than reporting a false-clean report.
func TestRecallIndexVerifyScanErrorSurfaces(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	m := h.manager(oneSource("a.md", "# T\n\nbody"))
	if _, err := m.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	flaky := flakyStore{Store: h.store, failScanOn: "fts:doc:"}
	m2 := buildFlaky(t, h, flaky, nil)
	if _, err := m2.Verify(ctx); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("want the scan error surfaced, got %v", err)
	}
}

// TestRecallIndexRebuildCatalogWriteErrorSurfaces proves an unwritable
// catalog path surfaces from Rebuild.
func TestRecallIndexRebuildCatalogWriteErrorSurfaces(t *testing.T) {
	h := newHarness(t)
	m, err := lifecycle.NewManager(lifecycle.ManagerOptions{
		// A catalog path under a file (not a directory) can never be
		// created: MkdirAll on its parent fails.
		CatalogPath: h.catalogPath + "/nested/catalog.json",
		Store:       h.store, Index: h.index, Vectors: h.vectors, Pipeline: h.pipeline,
		Sources: fixedSource{sources: oneSource("a.md", "# T\n\nbody")}, TreeHash: h.hash.Get, Clock: h.clock,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(h.catalogPath), 0o700); err != nil {
		t.Fatalf("seed parent dir: %v", err)
	}
	if err := os.WriteFile(h.catalogPath, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("seed conflicting file: %v", err)
	}
	if _, err := m.Rebuild(context.Background()); err == nil {
		t.Fatal("want an error writing the catalog under a file path component")
	}
}
