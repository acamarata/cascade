// Purpose: TestParityChecker-prefixed tests — the real golden-parity path
// end to end (real sqlite FTS5 index, real RebuildIndex, real
// recall.Service, real ParityChecker over the v1 golden fixtures) plus the
// checker's own error paths, driven through a test-only Querier double
// (Art.1.1) rather than a corrupted real vector store.
//
// SPORT: migration/v1/paritychecker/ADD (P1-E26-W10-S53-T2).
package v1

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/recall"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/pkg/cascade"
)

const goldenRecallDir = "testdata/v1-goldens/recall"

func silentLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newGoldenRecallService rebuilds a real index over the real v1 golden
// corpus files and wires a real recall.Service over the real FTS5 leg —
// no embedder, matching internal/daemon/recall_index.go's own production
// wiring (F/S-10 chunking, F/S-11 write and query, verbatim).
func newGoldenRecallService(t *testing.T) *recall.Service {
	t.Helper()
	dir := t.TempDir()
	deps := newRebuildDeps(t, dir)
	corpusRoot, err := filepath.Abs(filepath.Join(goldenRecallDir, "corpus"))
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	if _, err := RebuildIndex(context.Background(), deps, RebuildOptions{
		Corpus: rebuildTestCorpus, Roots: []string{corpusRoot},
	}); err != nil {
		t.Fatalf("RebuildIndex over the real golden corpus: %v", err)
	}
	catalog := recall.NewFileCatalog(deps.CatalogPath)
	svc, err := recall.NewService(catalog, rrf.Params{}, retrieval.NewLeg(deps.Index.(*retrieval.Index)))
	if err != nil {
		t.Fatalf("recall.NewService: %v", err)
	}
	return svc
}

// TestParityChecker_GoldenCoverage is the ticket's central proof: the
// rebuilt v2 index answers every real v1 golden query with the exact
// real v1 content-hash it should, per R-14.82.
func TestParityChecker_GoldenCoverage(t *testing.T) {
	svc := newGoldenRecallService(t)
	queries, err := LoadGoldenQueries(goldenRecallDir)
	if err != nil {
		t.Fatalf("LoadGoldenQueries: %v", err)
	}
	if len(queries) == 0 {
		t.Fatal("no golden queries loaded; the fixture is missing or empty")
	}
	ledger, err := LoadDivergenceLedger(goldenRecallDir)
	if err != nil {
		t.Fatalf("LoadDivergenceLedger: %v", err)
	}
	checker, err := NewParityChecker(svc, silentLog())
	if err != nil {
		t.Fatalf("NewParityChecker: %v", err)
	}
	report := checker.Check(context.Background(), queries, ledger)
	if !report.Pass {
		t.Errorf("golden parity failed:\n%s", report.Summary())
	}
	for _, q := range report.Queries {
		if q.Err != nil {
			t.Errorf("%s: unexpected error: %v", q.QueryID, q.Err)
			continue
		}
		if q.Coverage != 1.0 {
			t.Errorf("%s: coverage = %.2f, want 1.0 (missing=%v surplus=%v)",
				q.QueryID, q.Coverage, q.Missing, q.Surplus)
		}
	}
}

// TestParityChecker_ZeroV1Results proves a golden query with no v1 result
// set at all (ExpectedHashes empty) is vacuously fully covered rather than
// dividing by zero or failing.
func TestParityChecker_ZeroV1Results(t *testing.T) {
	svc := newGoldenRecallService(t)
	checker, err := NewParityChecker(svc, silentLog())
	if err != nil {
		t.Fatalf("NewParityChecker: %v", err)
	}
	report := checker.Check(context.Background(), []GoldenQuery{
		{QueryID: "zero-results", Query: "quokka marmalade nonexistent", Scope: "project/migration-golden", K: 3},
	}, nil)
	if !report.Pass {
		t.Errorf("a query with zero v1 results must not fail parity: %s", report.Summary())
	}
	if len(report.Queries) != 1 || report.Queries[0].Coverage != 1.0 {
		t.Errorf("Queries = %+v, want one entry with Coverage 1.0", report.Queries)
	}
}

// TestParityChecker_EmptyCorpus proves the empty-index acceptance
// criterion: a real, rebuilt-but-empty index answers every golden query
// with coverage 0 and no panic, never an error.
func TestParityChecker_EmptyCorpus(t *testing.T) {
	dir := t.TempDir()
	deps := newRebuildDeps(t, dir)
	if _, err := RebuildIndex(context.Background(), deps, RebuildOptions{
		Corpus: rebuildTestCorpus, Roots: []string{filepath.Join(dir, "does-not-exist")},
	}); err != nil {
		t.Fatalf("RebuildIndex over an empty corpus: %v", err)
	}
	catalog := recall.NewFileCatalog(deps.CatalogPath)
	svc, err := recall.NewService(catalog, rrf.Params{}, retrieval.NewLeg(deps.Index.(*retrieval.Index)))
	if err != nil {
		t.Fatalf("recall.NewService: %v", err)
	}
	queries, err := LoadGoldenQueries(goldenRecallDir)
	if err != nil {
		t.Fatalf("LoadGoldenQueries: %v", err)
	}
	checker, err := NewParityChecker(svc, silentLog())
	if err != nil {
		t.Fatalf("NewParityChecker: %v", err)
	}
	report := checker.Check(context.Background(), queries, nil)
	if report.Pass {
		t.Error("an empty corpus must not report Pass=true against non-empty golden expectations")
	}
	for _, q := range report.Queries {
		if q.Err != nil {
			t.Errorf("%s: empty corpus must answer empty, not error: %v", q.QueryID, q.Err)
		}
		if q.Coverage != 0 {
			t.Errorf("%s: coverage = %.2f, want 0 against an empty index", q.QueryID, q.Coverage)
		}
	}
}

// corruptedVectorQuerier is a test-only Querier double (Art.1.1) standing
// in for a leg backed by a corrupted vector entry: one query id fails,
// the rest answer normally. It exists to prove ParityChecker.Check logs
// and continues rather than aborting the report, without needing to
// actually corrupt a real vector store.
type corruptedVectorQuerier struct{ failMarker string }

func (c corruptedVectorQuerier) Query(_ context.Context, req recall.Request) (recall.Response, error) {
	if strings.Contains(req.Query, c.failMarker) {
		return recall.Response{}, cascade.New(cascade.KindIntegrity,
			"migration v1 recall test double: corrupted vector entry")
	}
	return recall.Response{Query: req.Query, Results: []recall.Result{{ChunkID: "c-ok", Rank: 1}}}, nil
}

// TestParityChecker_CorruptedVectorEntry proves the "corrupted vector
// entry logs warning and continues" acceptance criterion: one query's
// failure is recorded and logged, and the remaining query is still
// scored.
func TestParityChecker_CorruptedVectorEntry(t *testing.T) {
	var logBuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logBuf, nil))
	checker, err := NewParityChecker(corruptedVectorQuerier{failMarker: "corrupt-trigger"}, log)
	if err != nil {
		t.Fatalf("NewParityChecker: %v", err)
	}
	report := checker.Check(context.Background(), []GoldenQuery{
		{QueryID: "bad-vector", Query: "corrupt-trigger term", Scope: "project/migration-golden", K: 3,
			ExpectedHashes: []string{"c-ok"}},
		{QueryID: "good-vector", Query: "clean term", Scope: "project/migration-golden", K: 3,
			ExpectedHashes: []string{"c-ok"}},
	}, nil)
	if report.Pass {
		t.Error("Pass = true, want false: one query failed")
	}
	if len(report.Queries) != 2 {
		t.Fatalf("Queries has %d entries, want 2 (the failure must not abort the report)", len(report.Queries))
	}
	if report.Queries[0].Err == nil {
		t.Error("bad-vector: Err = nil, want the corrupted-entry error")
	}
	if report.Queries[1].Err != nil || report.Queries[1].Coverage != 1.0 {
		t.Errorf("good-vector: %+v, want a clean, fully-covered result", report.Queries[1])
	}
	if !strings.Contains(logBuf.String(), "golden query failed") || !strings.Contains(logBuf.String(), "bad-vector") {
		t.Errorf("log output does not record the failed query as a warning:\n%s", logBuf.String())
	}
}
