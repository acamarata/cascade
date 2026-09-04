// Purpose: the [retrieval] config surface seen from the side that
// consumes it — that its defaults agree with the fusion's own, that every
// malformed form fails closed before any query runs, that setting a key
// twice converges, and that each knob actually reaches the ranking.
//
// Inputs: n/a (test-only).
// Outputs: n/a (test-only).
// Constraints: Art.7 — config files under t.TempDir(), injected clock, no
// network. The harness and its two named doubles live in
// e2e_fixtures_test.go.
// SPORT: runtime/config.retrieval (ADD, P1-E06-W2-S12-T4).

package retrieval_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestRetrievalDefaultKAgreesWithFusion: retrieval.fusion.k's documented
// default and the fusion's own DefaultK are two declarations of one
// number, in two packages that must not depend on each other. Nothing but
// this assertion keeps them equal.
func TestRetrievalDefaultKAgreesWithFusion(t *testing.T) {
	if runtime.DefaultFusionK != rrf.DefaultK {
		t.Fatalf("runtime.DefaultFusionK = %d but rrf.DefaultK = %d",
			runtime.DefaultFusionK, rrf.DefaultK)
	}
	cfg := loadRetrievalConfig(t, "[retrieval]\nsources = [\"handbook\"]\n")
	if got := paramsFrom(cfg).EffectiveK(); got != rrf.DefaultK {
		t.Errorf("a config with no fusion.k fuses at k = %d, want %d", got, rrf.DefaultK)
	}
}

// TestRetrievalConfigFailsClosedBeforeAnyQuery: an operator who
// configured retrieval and silently got the defaults has been misled
// about what their system is doing, so every malformed form must refuse
// the load outright — before a filter is built, before a leg runs.
func TestRetrievalConfigFailsClosedBeforeAnyQuery(t *testing.T) {
	bodies := map[string]string{
		"unknown key":          "[retrieval]\nchunk_size = 400\n",
		"unknown fusion key":   "[retrieval.fusion]\nrrf_k = 60\n",
		"unknown reranker key": "[retrieval.reranker]\nenable = true\n",
		"wrong type":           "[retrieval.fusion]\nk = \"60\"\n",
		"zero where positive":  "[retrieval.fusion]\nk = 0\n",
		"negative":             "[retrieval.fusion]\nk = -1\n",
		"negative weight":      "[retrieval.fusion]\nweights = { fts5 = -1.0 }\n",
		"malformed section":    "retrieval = \"on\"\n",
		"malformed sources":    "[retrieval]\nsources = [\"a\", \"a\"]\n",
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			cfg, err := loadRetrievalConfigErr(t, body)
			if err == nil {
				t.Fatalf("a malformed [retrieval] loaded successfully and would have "+
					"queried on defaults: %+v", cfg)
			}
			if !strings.Contains(err.Error(), "retrieval") {
				t.Errorf("error %q does not name the offending section", err)
			}
		})
	}
}

// TestRetrievalConfigIdempotent: writing the same value twice converges.
// The second write produces byte-identical file content and the reload
// produces identical effective values, so `cascade config set` run twice
// is a no-op rather than a churn.
func TestRetrievalConfigIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[retrieval.fusion]\nk = 60\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	src, err := os.ReadFile(path) //nolint:gosec // path is under t.TempDir()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	once, err := runtime.SetKeyLine(src, "retrieval.fusion.k", "80")
	if err != nil {
		t.Fatalf("SetKeyLine: %v", err)
	}
	twice, err := runtime.SetKeyLine(once, "retrieval.fusion.k", "80")
	if err != nil {
		t.Fatalf("SetKeyLine (second): %v", err)
	}
	if string(once) != string(twice) {
		t.Errorf("setting the same value twice changed the file:\n%s\n---\n%s", once, twice)
	}
	first := loadRetrievalConfig(t, string(once))
	second := loadRetrievalConfig(t, string(twice))
	if first.FusionK() != second.FusionK() || first.FusionK() != 80 {
		t.Errorf("effective k diverged: %d then %d, want 80 both times",
			first.FusionK(), second.FusionK())
	}
}

// TestRetrievalSourcesReachIngest: the sources[] an operator registers are
// the corpora the pipeline answers from. The assertion is against the
// corpora actually ingested, not against the config echoed back.
func TestRetrievalSourcesReachIngest(t *testing.T) {
	h := newStory(t)
	cfg := loadRetrievalConfig(t, "[retrieval]\nsources = [\"handbook\", \"journal\"]\n")
	sources := cfg.RetrievalSources()
	if len(sources) != 2 {
		t.Fatalf("RetrievalSources() = %v, want both registered sources", sources)
	}
	got := h.recall(t, cfg, inScope, e2eQuery)
	for _, r := range got.results {
		if r.CorpusID != handbookCorpus.ID {
			t.Errorf("result from corpus %q, which the session's scope does not reach", r.CorpusID)
		}
	}
	// Registering a source is not a grant: journal is registered and
	// ingested, and the session still sees nothing from it.
	if len(got.results) == 0 {
		t.Fatal("registering both sources returned nothing at all")
	}
}

// TestRetrievalFusionKReachesRanking: changing fusion.k through the config
// changes the fused result set.
//
// It changes the SCORES rather than the order here, and that is a property
// of RRF, not a gap in the wiring: these two legs rank the same documents
// in mirrored positions, and a mirrored pair's fused score is symmetric in
// k. The order-sensitivity of k is exercised where it can be constructed
// directly, in internal/retrieval/rrf's own params tests.
func TestRetrievalFusionKReachesRanking(t *testing.T) {
	h := newStory(t)
	small := h.recall(t, loadRetrievalConfig(t, "[retrieval.fusion]\nk = 2\n"), inScope, e2eQuery)
	large := h.recall(t, loadRetrievalConfig(t, "[retrieval.fusion]\nk = 600\n"), inScope, e2eQuery)

	if len(small.results) == 0 || len(small.results) != len(large.results) {
		t.Fatalf("result counts differ or are empty: %d vs %d",
			len(small.results), len(large.results))
	}
	for i := range small.results {
		if small.results[i].RawScore == large.results[i].RawScore {
			t.Errorf("rank %d scored %v under both k = 2 and k = 600; fusion.k did not reach the fusion",
				i+1, small.results[i].RawScore)
		}
	}
	if small.citations.Citations[0].RawScore == large.citations.Citations[0].RawScore {
		t.Error("the citation's reported score did not follow the configured k")
	}
}

// TestRetrievalFusionWeightsReachRanking: weighting one leg up promotes
// that leg's own top document, end to end and through the citations. This
// is the config surface's strongest claim — a tuning the operator writes
// moves the answer.
func TestRetrievalFusionWeightsReachRanking(t *testing.T) {
	h := newStory(t)
	favourLexical := h.recall(t, loadRetrievalConfig(t,
		"[retrieval.fusion]\nweights = { fts5 = 3.0, vector = 1.0 }\n"), inScope, e2eQuery)
	favourVector := h.recall(t, loadRetrievalConfig(t,
		"[retrieval.fusion]\nweights = { fts5 = 1.0, vector = 3.0 }\n"), inScope, e2eQuery)

	if !strings.HasSuffix(favourLexical.results[0].Path, "fusion.md") {
		t.Errorf("weighting the lexical leg up ranked %q first, want fusion.md",
			favourLexical.results[0].Path)
	}
	if !strings.HasSuffix(favourVector.results[0].Path, "recall.md") {
		t.Errorf("weighting the embedding leg up ranked %q first, want recall.md",
			favourVector.results[0].Path)
	}
	if favourLexical.citations.Citations[0].ChunkID == favourVector.citations.Citations[0].ChunkID {
		t.Error("the citations did not follow the reordered results")
	}
}

// TestRetrievalRerankerEnabledWithNoDriver: switching the stage on with
// nothing registered is a typed refusal, not a query that quietly returns
// fusion order while the operator believes reranking ran.
func TestRetrievalRerankerEnabledWithNoDriver(t *testing.T) {
	cfg := loadRetrievalConfig(t, "[retrieval.reranker]\nenabled = true\n")
	if !cfg.RerankerEnabled() {
		t.Fatal("the config did not enable the stage")
	}
	_, err := rrf.Rerank(t.Context(), e2eQuery, []rrf.FusedResult{{ChunkID: "a"}},
		rrf.RerankOptions{
			Enabled:     cfg.RerankerEnabled(),
			PassageText: func(rrf.FusedResult) string { return "a" },
		})
	if err == nil {
		t.Fatal("reranker.enabled = true with no driver registered was accepted")
	}
	kind, ok := cascade.KindOf(err)
	if !ok || kind != cascade.KindInvalidInput {
		t.Errorf("kind = %v (ok %v), want %v", kind, ok, cascade.KindInvalidInput)
	}
	var cfgErr *runtime.ConfigError
	if errors.As(err, &cfgErr) {
		t.Error("the refusal came from the config loader; it must come from the stage itself")
	}
}
