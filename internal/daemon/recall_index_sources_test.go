package daemon

// Purpose: mutation-provable coverage for recall_index_sources.go — the
//   retrieval.sources[] ingest wiring DEFECT-retrieval-sources-not-wired-
//   to-ingest.md reported missing. Proves a real, registered project
//   directory is actually indexed (a non-zero CorporaIndexed/
//   ChunksWritten from the real RPC dispatch, never an emitted event) and
//   that the same content is then findable through the real recall.query
//   composition over the same store.
// Constraints: Art.2 -- a real modernc SQLite store and a real
//   config.toml under t.TempDir(); no doubles. Art.7.1 -- nothing written
//   outside t.TempDir(), no network listener.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/internal/retrieval/lifecycle"
	"github.com/acamarata/cascade/internal/retrieval/recall"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/sqlite"
)

// recallIndexFixtureWord is the distinctive token seeded into the fixture
// project's one real file, and later searched for.
const recallIndexFixtureWord = "cascadeindexfixtureneedle"

// writeRetrievalSourcesConfig writes a config.toml at paths.ConfigPath()
// declaring sourceDir as the sole retrieval.sources[] entry — the exact
// key configSourceProvider.Sources reads.
func writeRetrievalSourcesConfig(t *testing.T, paths runtime.PathProvider, sourceDir string) {
	t.Helper()
	body := "[retrieval]\nsources = [" + strconv.Quote(sourceDir) + "]\n"
	if err := os.WriteFile(paths.ConfigPath(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRegisterRecallIndexHandler_RebuildIndexesConfiguredSource is the
// mutation-provable proof: with retrieval.sources[] naming a real
// directory holding one real markdown file, `recall.index.rebuild`
// dispatched through the REAL registry reports a non-zero CorporaIndexed
// and ChunksWritten, and the SAME store then answers a real recall.query
// for a word from that file with a real hit. Before this ticket's wiring,
// RegisterRecallIndexHandler built its Manager with no Sources at all, so
// this rebuild reported CorporaIndexed: 0 regardless of what
// retrieval.sources named — that is this test's real red message: removing
// the `Sources: configSourceProvider{...}` line from recall_index.go's
// RegisterRecallIndexHandler reproduces exactly that failure.
func TestRegisterRecallIndexHandler_RebuildIndexesConfiguredSource(t *testing.T) {
	root, paths, sourceDir, store, dbPath := seedRecallIndexSourceFixture(t)

	reg := rpc.NewRegistry()
	if err := RegisterRecallIndexHandler(reg, paths, runtime.NewSystemClock(), store, dbPath); err != nil {
		t.Fatalf("RegisterRecallIndexHandler: %v", err)
	}

	res, errObj := reg.Dispatch(context.Background(), &rpc.Request{
		JSONRPC: "2.0", Method: RecallIndexRebuildMethod, ID: []byte("1"),
	})
	if errObj != nil {
		t.Fatalf("recall.index.rebuild dispatch: %+v", errObj)
	}
	result, ok := res.(lifecycle.RebuildResult)
	if !ok {
		t.Fatalf("recall.index.rebuild returned %T, want lifecycle.RebuildResult", res)
	}
	// The real assertion: a STORE-STATE-backed count from the rebuild's own
	// returned result, not an event and not a mere "no error".
	if result.CorporaIndexed == 0 {
		t.Fatalf("CorporaIndexed = 0 against a real registered project directory; retrieval.sources "+
			"is configured (%s) but nothing was ingested. Result: %+v", sourceDir, result)
	}
	if result.ChunksWritten == 0 {
		t.Fatalf("ChunksWritten = 0; the configured source's one real file produced no chunks. Result: %+v", result)
	}

	assertRecallFindsFixtureWord(t, paths, store, root)
}

// seedRecallIndexSourceFixture builds a real project directory holding one
// real markdown file naming recallIndexFixtureWord, a config.toml
// declaring it as the sole retrieval.sources[] entry, and a real modernc
// SQLite store ready for RegisterRecallIndexHandler.
func seedRecallIndexSourceFixture(t *testing.T) (
	root string, paths runtime.PathProvider, sourceDir string, store provider.Store, dbPath string,
) {
	t.Helper()
	root = t.TempDir()
	fp := fakePaths{root: root}

	sourceDir = filepath.Join(root, "project")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "# Fixture\n\nThis project mentions " + recallIndexFixtureWord + " right here.\n"
	if err := os.WriteFile(filepath.Join(sourceDir, "NOTES.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRetrievalSourcesConfig(t, fp, sourceDir)

	if err := os.MkdirAll(recallIndexDataDir(fp), 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath = filepath.Join(root, "cascade.db")
	s, err := sqlite.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("opening the real SQLite database: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return root, fp, sourceDir, s, dbPath
}

// assertRecallFindsFixtureWord proves the SAME store answers a real query
// for the seeded word, through the real recall.Service/recall.Handler
// composition registerRecallHandler builds for the daemon
// (internal/daemon cannot import cmd/cascade's registerRecallHandler, so
// this composes the identical pieces directly rather than duplicating a
// second handler).
func assertRecallFindsFixtureWord(t *testing.T, paths runtime.PathProvider, store provider.Store, root string) {
	t.Helper()
	idx, err := retrieval.NewIndex(store)
	if err != nil {
		t.Fatalf("retrieval.NewIndex: %v", err)
	}
	catalog := recall.NewFileCatalog(filepath.Join(recallIndexDataDir(paths), recallIndexCatalogName))
	legs := []recall.Leg{fusion.NewVectorLeg(nil, nil, nil), retrieval.NewLeg(idx)}
	svc, err := recall.NewService(catalog, rrf.Params{}, legs...)
	if err != nil {
		t.Fatalf("recall.NewService: %v", err)
	}
	raw, err := json.Marshal(recall.QueryParams{Query: recallIndexFixtureWord, Scope: "local", Cite: true})
	if err != nil {
		t.Fatalf("encode recall.query params: %v", err)
	}
	out, err := recall.NewHandler(svc).Query(context.Background(), raw)
	if err != nil {
		t.Fatalf("recall.query for %q: %v", recallIndexFixtureWord, err)
	}
	queryResult, ok := out.(recall.QueryResult)
	if !ok {
		t.Fatalf("recall.query returned %T, want recall.QueryResult", out)
	}
	if len(queryResult.Results) == 0 {
		t.Fatalf("recall.query for %q returned zero results against a freshly rebuilt index in %s; "+
			"citations=%+v", recallIndexFixtureWord, root, queryResult.Citations)
	}
	if len(queryResult.Citations) == 0 {
		t.Fatalf("recall.query for %q returned %d result(s) but no citation", recallIndexFixtureWord, len(queryResult.Results))
	}
}
