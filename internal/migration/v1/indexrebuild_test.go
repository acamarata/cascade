// Purpose: TestRebuildIndex-prefixed tests for RebuildIndex — a real
// sqlite-backed FTS5 index driven end to end over real files on disk, with
// no embedder configured (the same degradation
// internal/daemon/recall_index.go's production wiring documents).
//
// SPORT: migration/v1/indexrebuild/ADD (P1-E26-W10-S53-T2).
package v1

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/providers/sqlite"
)

// rebuildTestCorpus is the classification every RebuildIndex test file
// gets registered under.
var rebuildTestCorpus = corpus.Corpus{
	ID: "migrated-memory", ScopeRef: "project/migration-golden",
	Privacy: corpus.PrivacyProject, Visibility: corpus.VisibilityScopeLocal, Trust: corpus.TrustTrusted,
}

// rebuildFixedNow is the injected instant every RebuildIndex test sees.
var rebuildFixedNow = time.Unix(1700000000, 0)

// fixedRebuildHash is a test-only GitTreeHashFunc returning a fixed value
// (Art.1.1: this package has no real git tree to hash under t.TempDir()).
func fixedRebuildHash(hash string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return hash, nil }
}

// newRebuildDeps opens a real sqlite-backed store and FTS5 index under
// dir, with no vector store or embed pipeline configured.
func newRebuildDeps(t *testing.T, dir string) RebuildDeps {
	t.Helper()
	driver, err := sqlite.Open(context.Background(), filepath.Join(dir, "cascade.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	idx, err := retrieval.NewIndex(driver)
	if err != nil {
		t.Fatalf("retrieval.NewIndex: %v", err)
	}
	return RebuildDeps{
		CatalogPath: filepath.Join(dir, "retrieval", "catalog.json"),
		Store:       driver,
		Index:       idx,
		TreeHash:    fixedRebuildHash("commit-1"),
		Clock:       runtime.NewFixedClock(rebuildFixedNow),
	}
}

// writeSourceFile writes body to dir/name, creating dir if needed.
func writeSourceFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s/%s: %v", dir, name, err)
	}
}

// TestRebuildIndex drives RebuildIndex over one real markdown file and
// checks the real F/S-10 chunker and F/S-11 lifecycle actually ran: a
// chunk was written, and the rebuilt index verifies clean (the "doctor
// --storage reports index healthy after rebuild" acceptance criterion).
// It also proves an unrecognized extension under the same root is
// skipped, not refused, and never reaches the index.
func TestRebuildIndex(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "memory")
	writeSourceFile(t, root, "decisions.md", "Use BLAKE3 for content hashing everywhere.\n")
	writeSourceFile(t, root, "notes.bin", "\x00not chunkable and must be skipped, not refused\n")

	deps := newRebuildDeps(t, dir)
	result, err := RebuildIndex(context.Background(), deps, RebuildOptions{
		Corpus: rebuildTestCorpus, Roots: []string{root},
	})
	if err != nil {
		t.Fatalf("RebuildIndex: %v", err)
	}
	if result.Rebuild.CorporaIndexed != 1 {
		t.Errorf("CorporaIndexed = %d, want 1", result.Rebuild.CorporaIndexed)
	}
	if result.Rebuild.ChunksWritten == 0 {
		t.Error("ChunksWritten = 0, want at least the one real markdown chunk")
	}
	if !result.Verify.Clean() {
		t.Errorf("Verify report not clean after rebuild: %+v", result.Verify)
	}
}

// TestRebuildIndex_EmptyRoot proves a migration stage that has not written
// anything yet (a root directory that does not exist) is a real,
// convergent empty source, not an error — RebuildIndex's own documented
// degradation, exercised end to end.
func TestRebuildIndex_EmptyRoot(t *testing.T) {
	dir := t.TempDir()
	deps := newRebuildDeps(t, dir)
	result, err := RebuildIndex(context.Background(), deps, RebuildOptions{
		Corpus: rebuildTestCorpus, Roots: []string{filepath.Join(dir, "does-not-exist-yet")},
	})
	if err != nil {
		t.Fatalf("RebuildIndex over a missing root: %v", err)
	}
	if result.Rebuild.CorporaIndexed != 0 {
		t.Errorf("CorporaIndexed = %d, want 0 for no registered sources", result.Rebuild.CorporaIndexed)
	}
	if !result.Rebuild.Converged {
		t.Error("Converged = false, want true for a real, empty migration source")
	}
}

// TestRebuildIndex_Idempotent proves the idempotency acceptance criterion:
// a second RebuildIndex call over an unchanged tree writes zero new
// entries and deletes zero, and the rebuilt index still verifies clean.
func TestRebuildIndex_Idempotent(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "memory")
	writeSourceFile(t, root, "decisions.md", "Use BLAKE3 for content hashing everywhere.\n")
	deps := newRebuildDeps(t, dir)
	opts := RebuildOptions{Corpus: rebuildTestCorpus, Roots: []string{root}}

	first, err := RebuildIndex(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("first RebuildIndex: %v", err)
	}
	if first.Rebuild.ChunksWritten == 0 {
		t.Fatal("first run wrote no chunks; the idempotency check below would be vacuous")
	}

	second, err := RebuildIndex(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("second RebuildIndex: %v", err)
	}
	if second.Rebuild.ChunksWritten != 0 {
		t.Errorf("second run's ChunksWritten = %d, want 0 (idempotent re-run over an unchanged tree)",
			second.Rebuild.ChunksWritten)
	}
	if second.Rebuild.ChunksDeleted != 0 {
		t.Errorf("second run's ChunksDeleted = %d, want 0", second.Rebuild.ChunksDeleted)
	}
	if !second.Rebuild.Converged {
		t.Error("second run not reported Converged, want true")
	}
	if !second.Verify.Clean() {
		t.Errorf("second run's verify report not clean: %+v", second.Verify)
	}
}
