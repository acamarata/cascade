package citations

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
)

// mapResolver authorizes exactly the chunk ids it holds. It stands in for
// the query path's scope filter, whose Resolve method it mirrors; the real
// filter is exercised directly in TestAssembleWithTheRealScopeFilter.
type mapResolver map[string]corpus.Record

func (m mapResolver) Resolve(chunkID string) (corpus.Record, bool) {
	r, ok := m[chunkID]
	return r, ok
}

// mapLocator supplies line spans for the chunks it holds and nothing else.
type mapLocator map[string]LineRange

func (m mapLocator) Lines(chunkID string) (LineRange, bool) {
	r, ok := m[chunkID]
	return r, ok
}

func record(id, corpusID string, trust corpus.TrustLevel) corpus.Record {
	return corpus.Record{
		ID: id, CorpusID: corpusID, ScopeRef: "proj/a",
		Privacy: corpus.PrivacyProject, Visibility: corpus.VisibilityPrivate,
		Trust: trust,
	}
}

func mustAssemble(t *testing.T, results []rrf.FusedResult, opts Options) CitationSet {
	t.Helper()
	set, err := Assemble(results, opts)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	return set
}

func TestAssembleCitesTheResultItIsAttachedTo(t *testing.T) {
	results := []rrf.FusedResult{
		{ChunkID: "c1", Path: "docs/a.md", CorpusID: "docs", Trust: corpus.TrustTrusted,
			Score: 1.0, RawScore: 0.032, Strategies: []rrf.StrategyName{rrf.StrategyFTS}},
		{ChunkID: "c2", Path: "docs/b.md", CorpusID: "docs", Trust: corpus.TrustTrusted,
			Score: 0.5, RawScore: 0.016, Strategies: []rrf.StrategyName{rrf.StrategyVector}},
	}
	set := mustAssemble(t, results, Options{
		Resolver: mapResolver{"c1": record("c1", "docs", corpus.TrustTrusted), "c2": record("c2", "docs", corpus.TrustTrusted)},
		Locator:  mapLocator{"c1": {Start: 3, End: 9}},
	})
	if set.Len() != 2 || set.Withheld != 0 {
		t.Fatalf("set has %d citations and %d withheld, want 2/0", set.Len(), set.Withheld)
	}
	for i, want := range results {
		got := set.Citations[i]
		if got.ChunkID != want.ChunkID || got.Path != want.Path || got.CorpusID != want.CorpusID {
			t.Fatalf("citation %d is %s@%s (corpus %s), want %s@%s (corpus %s)",
				i, got.ChunkID, got.Path, got.CorpusID, want.ChunkID, want.Path, want.CorpusID)
		}
		if got.Rank != i+1 || got.Score != want.Score || got.RawScore != want.RawScore {
			t.Fatalf("citation %d carries rank %d score %v raw %v, want %d/%v/%v",
				i, got.Rank, got.Score, got.RawScore, i+1, want.Score, want.RawScore)
		}
	}
	if set.Citations[0].Lines != (LineRange{Start: 3, End: 9}) {
		t.Fatalf("citation 0 span %+v, want 3-9", set.Citations[0].Lines)
	}
	if set.Citations[1].Lines.Known() {
		t.Fatalf("citation 1 invented the span %+v for a chunk the locator does not know",
			set.Citations[1].Lines)
	}
}

// TestAssembleCitesTheDedupedRowsOwnSource is the case a citation goes
// wrong in most easily. Two legs return the SAME chunk id at two different
// paths, so RRF collapses them into one row and picks one path for it. The
// citation must report the path that row actually carries, not the other
// leg's — and the neighbouring result must not pick up either of them.
func TestAssembleCitesTheDedupedRowsOwnSource(t *testing.T) {
	fused, err := rrf.Fuse([]rrf.RankedList{
		{Strategy: rrf.StrategyFTS, Weight: rrf.NeutralWeight, Hits: []rrf.Candidate{
			{ChunkID: "shared", Path: "vendor/copy.md", CorpusID: "docs", Trust: corpus.TrustTrusted},
			{ChunkID: "other", Path: "docs/other.md", CorpusID: "docs", Trust: corpus.TrustTrusted},
		}},
		{Strategy: rrf.StrategyVector, Weight: rrf.NeutralWeight, Hits: []rrf.Candidate{
			{ChunkID: "shared", Path: "docs/original.md", CorpusID: "docs", Trust: corpus.TrustTrusted},
		}},
	}, rrf.DefaultK)
	if err != nil {
		t.Fatalf("Fuse: %v", err)
	}
	set := mustAssemble(t, fused, Options{Resolver: mapResolver{
		"shared": record("shared", "docs", corpus.TrustTrusted),
		"other":  record("other", "docs", corpus.TrustTrusted),
	}})
	if set.Len() != len(fused) {
		t.Fatalf("assembled %d citations for %d results", set.Len(), len(fused))
	}
	byChunk := map[string]Citation{}
	for _, c := range set.Citations {
		byChunk[c.ChunkID] = c
	}
	for _, r := range fused {
		c, ok := byChunk[r.ChunkID]
		if !ok {
			t.Fatalf("result %s has no citation", r.ChunkID)
		}
		if c.Path != r.Path {
			t.Fatalf("citation for %s points at %q, but the fused row it is attached to is %q",
				r.ChunkID, c.Path, r.Path)
		}
	}
	if got := byChunk["shared"].Strategies; len(got) != 2 {
		t.Fatalf("the merged row's citation reports legs %v, want both legs that found it", got)
	}
	if byChunk["other"].Path != "docs/other.md" {
		t.Fatalf("the neighbouring result's citation picked up %q from the merged row",
			byChunk["other"].Path)
	}
}

// TestAssembleDoesNotLaunderTrust covers the laundering bug directly: two
// legs reach the same chunk through differently classified paths, fusion
// resolves the row to untrusted, and the authorized record for that chunk
// still says trusted. The citation must report the untrusted resolution.
func TestAssembleDoesNotLaunderTrust(t *testing.T) {
	fused, err := rrf.Fuse([]rrf.RankedList{
		{Strategy: rrf.StrategyFTS, Weight: rrf.NeutralWeight, Hits: []rrf.Candidate{
			{ChunkID: "shared", Path: "docs/a.md", CorpusID: "docs", Trust: corpus.TrustTrusted},
		}},
		{Strategy: rrf.StrategyVector, Weight: rrf.NeutralWeight, Hits: []rrf.Candidate{
			{ChunkID: "shared", Path: "docs/a.md", CorpusID: "docs", Trust: corpus.TrustUntrustedSource},
		}},
	}, rrf.DefaultK)
	if err != nil {
		t.Fatalf("Fuse: %v", err)
	}
	if fused[0].Trust != corpus.TrustUntrustedSource {
		t.Fatalf("precondition: fusion resolved the merged row to %q, want untrusted-source", fused[0].Trust)
	}
	set := mustAssemble(t, fused, Options{
		Resolver: mapResolver{"shared": record("shared", "docs", corpus.TrustTrusted)},
	})
	if set.Citations[0].Trust != corpus.TrustUntrustedSource {
		t.Fatalf("citation reports trust %q for a row fusion resolved to untrusted-source",
			set.Citations[0].Trust)
	}
	if !strings.Contains(Render(set).Definitions, "[untrusted]") {
		t.Fatalf("the rendered citation does not say the source is untrusted:\n%s",
			Render(set).Definitions)
	}
}

// TestAssembleWithholdsWhatTheSessionMayNotSee proves a result the
// resolver refuses contributes no citation and leaks nothing — not its
// path, not its corpus — through the assembled set or its rendered form.
func TestAssembleWithholdsWhatTheSessionMayNotSee(t *testing.T) {
	const secretPath = "other-project/secrets.md"
	const secretCorpus = "other-project-notes"
	results := []rrf.FusedResult{
		{ChunkID: "visible", Path: "docs/a.md", CorpusID: "docs", Trust: corpus.TrustTrusted, Score: 1},
		{ChunkID: "hidden", Path: secretPath, CorpusID: secretCorpus, Trust: corpus.TrustTrusted, Score: 0.4},
	}
	set := mustAssemble(t, results, Options{
		Resolver: mapResolver{"visible": record("visible", "docs", corpus.TrustTrusted)},
		Locator:  mapLocator{"hidden": {Start: 1, End: 99}},
	})
	if set.Len() != 1 || set.Citations[0].ChunkID != "visible" {
		t.Fatalf("set holds %+v, want exactly the visible citation", set.Citations)
	}
	if set.Withheld != 1 {
		t.Fatalf("Withheld = %d, want 1", set.Withheld)
	}
	rendered := Render(set)
	haystack := rendered.Definitions + strings.Join(rendered.Refs, "")
	for _, leak := range []string{secretPath, secretCorpus, "hidden", "99"} {
		if strings.Contains(haystack, leak) {
			t.Fatalf("the rendered citations disclose %q from a withheld record:\n%s", leak, haystack)
		}
	}
}

// TestAssembleWithholdsOnCorpusDisagreement covers the narrower leak: the
// chunk id resolves, but the leg claims it came from a corpus the
// authorized record does not agree with. Citing it would name a corpus the
// session was never cleared for.
func TestAssembleWithholdsOnCorpusDisagreement(t *testing.T) {
	results := []rrf.FusedResult{
		{ChunkID: "c1", Path: "docs/a.md", CorpusID: "private-notes", Trust: corpus.TrustTrusted, Score: 1},
	}
	set := mustAssemble(t, results, Options{
		Resolver: mapResolver{"c1": record("c1", "docs", corpus.TrustTrusted)},
	})
	if set.Len() != 0 || set.Withheld != 1 {
		t.Fatalf("set holds %+v (withheld %d), want nothing cited", set.Citations, set.Withheld)
	}
	if strings.Contains(Render(set).Definitions, "private-notes") {
		t.Fatal("the rendered citations name the corpus the leg claimed but the record denied")
	}
}

// TestAssembleWithTheRealScopeFilter binds this package's resolver
// interface to the query path's actual scope filter, so the two cannot
// drift on what "authorized" means: a record from another project's scope
// is withheld by the real filter and therefore never cited.
func TestAssembleWithTheRealScopeFilter(t *testing.T) {
	store := corpus.NewStore()
	for _, c := range []corpus.Corpus{
		{ID: "mine", ScopeRef: "proj/a", Privacy: corpus.PrivacyProject,
			Visibility: corpus.VisibilityPrivate, Trust: corpus.TrustTrusted},
		{ID: "theirs", ScopeRef: "proj/b", Privacy: corpus.PrivacyProject,
			Visibility: corpus.VisibilityPrivate, Trust: corpus.TrustTrusted},
	} {
		if err := store.AddCorpus(c); err != nil {
			t.Fatalf("AddCorpus: %v", err)
		}
	}
	for _, r := range []corpus.Record{
		{ID: "mine-1", CorpusID: "mine", ScopeRef: "proj/a", Privacy: corpus.PrivacyProject,
			Visibility: corpus.VisibilityPrivate, Trust: corpus.TrustTrusted},
		{ID: "theirs-1", CorpusID: "theirs", ScopeRef: "proj/b", Privacy: corpus.PrivacyProject,
			Visibility: corpus.VisibilityPrivate, Trust: corpus.TrustTrusted},
	} {
		if err := store.AddRecord(r); err != nil {
			t.Fatalf("AddRecord: %v", err)
		}
	}
	filter, err := fusion.NewScopeFilter(store, corpus.Query{
		Membership:  corpus.Membership{Scope: "proj/a"},
		Entitlement: corpus.PrivacyProject,
	})
	if err != nil {
		t.Fatalf("NewScopeFilter: %v", err)
	}
	results := []rrf.FusedResult{
		{ChunkID: "mine-1", Path: "a.md", CorpusID: "mine", Trust: corpus.TrustTrusted, Score: 1},
		{ChunkID: "theirs-1", Path: "their-secret.md", CorpusID: "theirs", Trust: corpus.TrustTrusted, Score: 0.5},
	}
	set := mustAssemble(t, results, Options{Resolver: filter})
	if set.Len() != 1 || set.Citations[0].ChunkID != "mine-1" {
		t.Fatalf("set holds %+v, want only the record this scope may read", set.Citations)
	}
	if strings.Contains(Render(set).Definitions, "their-secret.md") {
		t.Fatal("a record the real scope filter withheld was rendered anyway")
	}
}
