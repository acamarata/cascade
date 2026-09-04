// Purpose: the Epic F acceptance query path — recall(), which runs a
// query through the whole pipeline under a loaded config, and the
// end-to-end acceptance stories over it. The harness, the two doubles and
// the ingest path live in e2e_fixtures_test.go.
//
// Inputs: n/a (test-only).
// Outputs: n/a (test-only).
// Constraints: Art.7 — files under t.TempDir(), injected clock, no
// network, no wall clock.
// SPORT: internal.retrieval.e2e/ADDED (P1-E06-W2-S12-T4).

package retrieval_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/citations"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/internal/runtime"
)

// answer is one end-to-end recall result.
type answer struct {
	results   []rrf.FusedResult
	citations citations.CitationSet
	rendered  citations.Rendered
}

// recall runs the whole query path under cfg: scope filter, both legs,
// configured fusion, the optional reranker stage, then citations.
func (h *e2eHarness) recall(
	t *testing.T, cfg *runtime.Config, q corpus.Query, query string,
) answer {
	t.Helper()
	ctx := context.Background()
	filter, err := fusion.NewScopeFilter(h.store, q)
	if err != nil {
		t.Fatalf("NewScopeFilter: %v", err)
	}
	lists := []rrf.RankedList{h.lexicalLeg(filter, query, 10)}
	vec, ran, err := h.leg.Query(ctx, filter, query, 10)
	if err != nil {
		t.Fatalf("VectorLeg.Query: %v", err)
	}
	if ran {
		lists = append(lists, vec)
	}
	fused, err := rrf.FuseWith(lists, paramsFrom(cfg))
	if err != nil {
		t.Fatalf("FuseWith: %v", err)
	}
	outcome, err := rrf.Rerank(ctx, query, fused, rrf.RerankOptions{
		Enabled:     cfg.RerankerEnabled(),
		PassageText: func(r rrf.FusedResult) string { return string(h.chunks[r.ChunkID].chunk.Content) },
	})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	set, err := citations.Assemble(outcome.Results, citations.Options{Resolver: filter})
	if err != nil {
		t.Fatalf("citations.Assemble: %v", err)
	}
	return answer{results: outcome.Results, citations: set, rendered: citations.Render(set)}
}

// paramsFrom translates the loaded [retrieval.fusion] config into the
// fusion's own parameters. This is the whole config-to-pipeline seam this
// ticket exists to surface, kept in one function so the E2E exercises the
// same translation a shipping caller would.
func paramsFrom(cfg *runtime.Config) rrf.Params {
	p := rrf.Params{K: cfg.FusionK()}
	if weights := cfg.FusionWeights(); weights != nil {
		p.Weights = make(map[rrf.StrategyName]float64, len(weights))
		for leg, w := range weights {
			p.Weights[rrf.StrategyName(leg)] = w
		}
	}
	return p
}

// handbookCorpus is the corpus the querying session is a member of.
var handbookCorpus = corpus.Corpus{
	ID: "handbook", ScopeRef: "project/cascade",
	Privacy: corpus.PrivacyProject, Visibility: corpus.VisibilityScopeLocal,
	Trust: corpus.TrustTrusted,
}

// journalCorpus belongs to a scope the querying session is not in and is
// personal-tier besides. Nothing from it may reach the answer.
var journalCorpus = corpus.Corpus{
	ID: "journal", ScopeRef: "user/journal",
	Privacy: corpus.PrivacyPersonal, Visibility: corpus.VisibilityScopeLocal,
	Trust: corpus.TrustTrusted,
}

// handbookFiles are real markdown files. Content differs per file so no
// two chunks collide on the content-addressed id, and fusion.md and
// recall.md are written so the two legs DISAGREE about which is best:
// fusion.md holds the query's terms most often (the lexical leg's
// measure) while recall.md holds them in the query's own proportions (the
// embedding leg's measure). A corpus both legs rank identically would
// make the fusion untestable through them.
var handbookFiles = map[string]string{
	"fusion.md": "# Fusion\n\nReciprocal rank fusion merges the ranked lists " +
		"each leg returns into one rank order.\n",
	"recall.md": "# Recall\n\nA reciprocal rank fusion query returns cited " +
		"chunks.\n",
	"citations.md": "# Citations\n\nA citation carries provenance: the path, " +
		"the corpus and the trust tag behind a fused rank.\n",
	"storage.md": "# Storage\n\nThe storage driver keeps chunks and their " +
		"vectors; it neither ranks nor merges.\n",
}

// journalFiles is deliberately the STRONGEST match for the query. If any
// part of the pipeline leaks across scope, this file wins the ranking, so
// the scope assertion cannot pass by accident.
var journalFiles = map[string]string{
	"secrets.md": "# Private\n\nquokka reciprocal rank fusion reciprocal rank " +
		"fusion reciprocal rank fusion quokka.\n",
}

const e2eQuery = "reciprocal rank fusion"

// inScope is the querying session: a member of the handbook's scope only,
// entitled to project-tier content only.
var inScope = corpus.Query{
	Membership:  corpus.Membership{Scope: "project/cascade"},
	Entitlement: corpus.PrivacyProject,
}

// newStory ingests both corpora into one harness.
func newStory(t *testing.T) *e2eHarness {
	t.Helper()
	h := newE2EHarness(t)
	h.ingest(t, handbookCorpus, handbookFiles)
	h.ingest(t, journalCorpus, journalFiles)
	return h
}

// loadRetrievalConfig writes body to a real config.toml under t.TempDir()
// and loads it through the real loader.
func loadRetrievalConfig(t *testing.T, body string) *runtime.Config {
	t.Helper()
	cfg, err := loadRetrievalConfigErr(t, body)
	if err != nil {
		t.Fatalf("runtime.Load: %v", err)
	}
	return cfg
}

func loadRetrievalConfigErr(t *testing.T, body string) (*runtime.Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	return runtime.Load(context.Background(), runtime.LoadOptions{
		Path:    path,
		Getenv:  func(string) string { return "" },
		Environ: func() []string { return nil },
	})
}

// TestEpicFAcceptanceEndToEnd is the acceptance story: real files in, a
// fused, cited answer out, with the document that actually answers the
// query at the top.
func TestEpicFAcceptanceEndToEnd(t *testing.T) {
	h := newStory(t)
	got := h.recall(t, loadRetrievalConfig(t, "[retrieval]\nsources = [\"handbook\"]\n"), inScope, e2eQuery)

	if len(got.results) == 0 {
		t.Fatal("the query returned nothing")
	}
	if !strings.HasSuffix(got.results[0].Path, "fusion.md") {
		t.Errorf("top result is %q, want the document about fusion", got.results[0].Path)
	}
	if got.citations.Len() != len(got.results) {
		t.Errorf("%d results produced %d citations; every authorized result must be citable",
			len(got.results), got.citations.Len())
	}
	top := got.citations.Citations[0]
	if top.ChunkID != got.results[0].ChunkID || top.Path != got.results[0].Path {
		t.Errorf("citation %+v does not describe its own result %+v", top, got.results[0])
	}
	if top.CorpusID != handbookCorpus.ID || top.Trust != corpus.TrustTrusted {
		t.Errorf("citation carries corpus %q trust %q, want handbook/trusted", top.CorpusID, top.Trust)
	}
	if !strings.Contains(got.rendered.Definitions, "fusion.md") {
		t.Errorf("rendered citations do not name the source:\n%s", got.rendered.Definitions)
	}
}

// TestEpicFScopeHoldsEndToEnd is the property most likely to break when
// the parts are composed: each of them enforces scope separately, and the
// seam is where a leak would appear. The out-of-scope corpus holds the
// strongest match for the query, so nothing here passes by accident.
func TestEpicFScopeHoldsEndToEnd(t *testing.T) {
	h := newStory(t)
	cfg := loadRetrievalConfig(t, "[retrieval]\nsources = [\"handbook\", \"journal\"]\n")
	got := h.recall(t, cfg, inScope, e2eQuery)

	if len(got.results) == 0 {
		t.Fatal("the query returned nothing, so the scope assertion would be vacuous")
	}
	for _, r := range got.results {
		if r.CorpusID == journalCorpus.ID || strings.Contains(r.Path, "secrets.md") {
			t.Errorf("out-of-scope result leaked into the ranking: %+v", r)
		}
	}
	for _, c := range got.citations.Citations {
		if c.CorpusID == journalCorpus.ID || strings.Contains(c.Path, "secrets.md") {
			t.Errorf("out-of-scope citation leaked: %+v", c)
		}
	}
	if strings.Contains(got.rendered.Definitions, "quokka") ||
		strings.Contains(got.rendered.Definitions, "secrets.md") {
		t.Errorf("out-of-scope content reached the rendered citations:\n%s", got.rendered.Definitions)
	}
	if got.citations.Withheld != 0 {
		t.Errorf("Withheld = %d; the out-of-scope corpus must be excluded before ranking, "+
			"not filtered out of an answer it had already entered", got.citations.Withheld)
	}
}

// TestEpicFScopeAdmitsItsOwnCorpus is the control for the test above: the
// same pipeline, asked by a session that IS in the journal's scope and is
// personally entitled, does return the journal content. Without this, a
// pipeline that returned nothing at all would pass the leak test.
func TestEpicFScopeAdmitsItsOwnCorpus(t *testing.T) {
	h := newStory(t)
	cfg := loadRetrievalConfig(t, "[retrieval]\nsources = [\"journal\"]\n")
	got := h.recall(t, cfg, corpus.Query{
		Membership:  corpus.Membership{Scope: "user/journal"},
		Entitlement: corpus.PrivacyPersonal,
	}, e2eQuery)

	if len(got.results) != 1 || got.results[0].CorpusID != journalCorpus.ID {
		t.Fatalf("the journal's own session did not receive its own corpus: %+v", got.results)
	}
	if !strings.Contains(got.rendered.Definitions, "secrets.md") {
		t.Errorf("the journal's own session got no citation to its own file:\n%s",
			got.rendered.Definitions)
	}
}

// TestEpicFDeterministicEndToEnd: the same corpus and query produce
// identical results and identical citations across runs.
func TestEpicFDeterministicEndToEnd(t *testing.T) {
	h := newStory(t)
	cfg := loadRetrievalConfig(t, "[retrieval.fusion]\nk = 60\n")
	first := h.recall(t, cfg, inScope, e2eQuery)
	second := h.recall(t, cfg, inScope, e2eQuery)

	if !reflect.DeepEqual(first.results, second.results) {
		t.Errorf("two runs ranked differently:\n%+v\n%+v", first.results, second.results)
	}
	if !reflect.DeepEqual(first.citations, second.citations) {
		t.Errorf("two runs cited differently:\n%+v\n%+v", first.citations, second.citations)
	}
	if first.rendered.Definitions != second.rendered.Definitions {
		t.Error("two runs rendered different citation text")
	}

	// A second, independently ingested tree under a different temp
	// directory must rank the same content in the same order: the ids are
	// content-addressed, so nothing in the ranking may depend on where the
	// files happened to live.
	other := newStory(t)
	fresh := other.recall(t, cfg, inScope, e2eQuery)
	if len(fresh.results) != len(first.results) {
		t.Fatalf("a fresh tree returned %d results, want %d", len(fresh.results), len(first.results))
	}
	for i := range first.results {
		if fresh.results[i].ChunkID != first.results[i].ChunkID {
			t.Errorf("rank %d: fresh tree returned chunk %s, want %s",
				i+1, fresh.results[i].ChunkID, first.results[i].ChunkID)
		}
	}
}
