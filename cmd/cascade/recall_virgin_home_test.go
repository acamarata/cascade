package main

// Purpose: DEFECT-recall-no-embedded-path.md's regression test: drive the
//
//	REAL production command tree (newRootCmd with productionRecallDeps, not
//	a fake-Paths stub) against a CASCADE_HOME that has never been created —
//	the exact virgin-HOME state the Wave-2 hardening gate hit on the
//	shipped binary ("warning: daemon not running; running in embedded
//	(daemonless) mode" immediately followed by a socket dial anyway).
//	Reuses execRootProductionVirginHome (context_slice_virgin_home_test.go)
//	verbatim: same helper, same CASCADE_HOME/CASCADE_SOCKET/HOME shape, so
//	the daemonless probe always takes the embedded path deterministically
//	here too.
//
// SPORT: cmd.cascade.cmd.recall (FIX, embedded-path routing test).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/recall"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/sqlite"
)

// TestRecallVirginHomeNoIndexNeverDials is the primary regression proof:
// on a virgin HOME with no daemon and no retrieval index ever built,
// `cascade recall` must answer with the catalog's own honest refusal
// (KindNotFound: "no retrieval index has been built yet") — the SAME
// refusal a live daemon with no index gives (recall_integration_test.go's
// TestRecallIsReachableOnTheDaemonTheCompositionRootBuilds) — never the
// dial failure the shipped binary produced.
func TestRecallVirginHomeNoIndexNeverDials(t *testing.T) {
	virginHome := filepath.Join(t.TempDir(), "never-created", ".cascade")
	if _, err := os.Stat(virginHome); !os.IsNotExist(err) {
		t.Fatalf("test setup bug: virginHome must not exist yet, stat err=%v", err)
	}

	got, err := execRootProductionVirginHome(t, virginHome, "recall", "gofmt")
	if err == nil {
		t.Fatalf("cascade recall on a virgin HOME with no index: got nil error, "+
			"want the catalog's not-found refusal\noutput:\n%s", got)
	}
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("err = %v, want KindNotFound (\"no retrieval index has been built yet\")", err)
	}
	if strings.Contains(err.Error(), "daemon not running") || strings.Contains(err.Error(), "dial unix") {
		t.Fatalf("err = %v, still the dial-error shape DEFECT-recall-no-embedded-path.md reported", err)
	}

	indexDir := filepath.Join(virginHome, "data", "retrieval")
	if info, statErr := os.Stat(indexDir); statErr != nil || !info.IsDir() {
		t.Errorf("cascade recall did not bootstrap %s: stat err=%v", indexDir, statErr)
	}
}

// TestRecallVirginHomeEmptyIndexMatchesNothing proves the embedded path
// runs the REAL recall.Service.Query fusion logic end to end, rather than
// short-circuiting on "no index" alone: with a genuinely built (if empty)
// catalog present, the answer is a real, honest empty match ("no
// results", no error) rather than a leg-availability refusal.
//
// This assertion changed from KindUnavailable to "no results" as part of
// FIX-retrieval-leg-wiring.md: this file's sibling, openEmbeddedFTSLeg
// (recall_embedded.go), now opens cascade.db and wires
// internal/retrieval.NewLeg alongside the vector leg on every embedded
// recall, so a build with an available (if empty) full-text index no
// longer has "no retrieval leg is available" as a reachable answer here —
// that refusal now requires the catalog itself to be missing or broken
// (TestRecallVirginHomeNoIndexNeverDials above), not merely empty. Still
// never a dial error either way.
func TestRecallVirginHomeEmptyIndexMatchesNothing(t *testing.T) {
	virginHome := filepath.Join(t.TempDir(), "never-created", ".cascade")
	indexDir := filepath.Join(virginHome, "data", "retrieval")
	if err := os.MkdirAll(indexDir, 0o700); err != nil {
		t.Fatalf("test setup: mkdir %s: %v", indexDir, err)
	}
	doc := map[string]any{"version": 1, "corpora": []any{}, "records": []any{}}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("test setup: marshal catalog fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(indexDir, "catalog.json"), raw, 0o600); err != nil {
		t.Fatalf("test setup: write catalog fixture: %v", err)
	}

	got, err := execRootProductionVirginHome(t, virginHome, "recall", "gofmt")
	if err != nil {
		t.Fatalf("cascade recall against a real, empty index: %v\noutput:\n%s", err, got)
	}
	if strings.Contains(got, "daemon not running") || strings.Contains(got, "dial unix") {
		t.Fatalf("output = %q, still a dial-error shape, not a real empty match", got)
	}
	if !strings.Contains(got, "no results") {
		t.Fatalf("output = %q, want the real \"no results\" answer an available, empty index gives", got)
	}
}

// seedRealRetrievalIndex writes one real chunk into dataDir/cascade.db
// through the production write path (retrieval.NewIndex.Write) and the
// matching catalog.json, then releases cascade.db's §D-3 exclusive flock
// before returning: the embedded path opens its own connection
// (recall_embedded.go's openEmbeddedFTSLeg), and the real production open
// call this proves out would itself refuse with a held-lock conflict if
// the seeding connection were still open.
func seedRealRetrievalIndex(t *testing.T, dataDir string) corpus.Corpus {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dataDir, "retrieval"), 0o700); err != nil {
		t.Fatalf("test setup: mkdir: %v", err)
	}

	c := corpus.Corpus{
		ID: "handbook", ScopeRef: "project/example",
		Privacy: corpus.PrivacyProject, Visibility: corpus.VisibilityScopeLocal, Trust: corpus.TrustTrusted,
	}
	body := "reciprocal rank fusion combines ranked lists from every retrieval leg"
	chunk := retrieval.Chunk{
		ID: retrieval.ChunkID([]byte(body)), Path: "handbook/fusion.md",
		Content: []byte(body), Lang: "markdown", EndByte: len(body),
	}

	driver, err := sqlite.Open(context.Background(), filepath.Join(dataDir, "cascade.db"))
	if err != nil {
		t.Fatalf("test setup: sqlite.Open: %v", err)
	}
	idx, err := retrieval.NewIndex(driver)
	if err != nil {
		t.Fatalf("test setup: retrieval.NewIndex: %v", err)
	}
	if err := idx.Write(context.Background(), c.ID, []retrieval.Chunk{chunk}); err != nil {
		t.Fatalf("test setup: Write: %v", err)
	}
	if err := driver.Close(); err != nil {
		t.Fatalf("test setup: close seeding driver: %v", err)
	}

	catalogDoc := recall.CatalogDoc{
		Version: recall.CatalogVersion,
		Corpora: []corpus.Corpus{c},
		Records: []corpus.Record{{
			ID: chunk.ID, CorpusID: c.ID, ScopeRef: c.ScopeRef,
			Privacy: c.Privacy, Visibility: c.Visibility, Trust: c.Trust,
		}},
	}
	raw, err := json.Marshal(catalogDoc)
	if err != nil {
		t.Fatalf("test setup: marshal catalog: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "retrieval", recall.CatalogFileName), raw, 0o600); err != nil {
		t.Fatalf("test setup: write catalog: %v", err)
	}
	return c
}

// TestRecallVirginHomeEmbeddedPathFusesRealHits is
// FIX-retrieval-leg-wiring.md's end-to-end proof that the embedded
// composition root (recall_embedded.go's openEmbeddedFTSLeg) answers from
// a REAL full-text index rather than only ever degrading to "no leg" or
// "no results": seedRealRetrievalIndex seeds one real chunk, then this
// drives the real CLI end to end and asserts the corpus that chunk was
// indexed under appears in the printed table.
func TestRecallVirginHomeEmbeddedPathFusesRealHits(t *testing.T) {
	virginHome := filepath.Join(t.TempDir(), "never-created", ".cascade")
	c := seedRealRetrievalIndex(t, filepath.Join(virginHome, "data"))

	got, err := execRootProductionVirginHome(t, virginHome,
		"recall", "reciprocal rank fusion", "--scope", string(c.ScopeRef))
	if err != nil {
		t.Fatalf("cascade recall against a real index: %v\noutput:\n%s", err, got)
	}
	if strings.Contains(got, "no results") {
		t.Fatalf("real content was indexed under %q but recall reported no results:\n%s", c.ID, got)
	}
	if !strings.Contains(got, c.ID) {
		t.Fatalf("output does not name the indexed corpus %q:\n%s", c.ID, got)
	}
}
