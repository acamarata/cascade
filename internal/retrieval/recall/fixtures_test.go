// Purpose: the recall service's test fixtures — the two corpora, the
// indexed chunks, the leg doubles and the real catalog documents every
// test in this package runs against. Split from service_test.go under the
// 300-line file cap.
//
// WHAT IS REAL AND WHAT IS NOT. Real: FileCatalog reading a real file
// under t.TempDir(), internal/retrieval/corpus's model and its
// authorization decision, internal/retrieval/fusion's ScopeFilter,
// internal/retrieval/rrf's fusion and internal/retrieval/citations'
// assembler and renderer. ONE double: lexicalLeg below, which stands in
// for the FTS5 leg because F/S-10.T2 has not landed, named at its
// declaration exactly as the Epic F acceptance harness names its own.
//
// Constraints: Art.7 — files under t.TempDir(), no network, no wall clock.
// SPORT: internal.retrieval.recall.Service/ADDED (P1-E06-W2-S11-T3).

package recall

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/pkg/cascade"
)

// handbookCorpus is the corpus the querying session is a member of.
var handbookCorpus = corpus.Corpus{
	ID: "handbook", ScopeRef: "project/cascade",
	Privacy: corpus.PrivacyProject, Visibility: corpus.VisibilityScopeLocal,
	Trust: corpus.TrustTrusted,
}

// journalCorpus belongs to a scope the querying session is not in and is
// personal-tier besides. Nothing from it may reach an answer.
var journalCorpus = corpus.Corpus{
	ID: "journal", ScopeRef: "user/journal",
	Privacy: corpus.PrivacyPersonal, Visibility: corpus.VisibilityScopeLocal,
	Trust: corpus.TrustTrusted,
}

// testQuery is the query every story below asks.
const testQuery = "reciprocal rank fusion"

// fixtureChunks is the indexed content. journal/secrets.md is deliberately
// the STRONGEST match for testQuery: if any part of the composition leaks
// across scope, that chunk wins the ranking, so the scope assertions
// cannot pass by accident.
var fixtureChunks = map[string]struct {
	corpusID string
	path     string
	body     string
}{
	"c-fusion": {"handbook", "handbook/fusion.md",
		"Reciprocal rank fusion merges the ranked lists each leg returns."},
	"c-recall": {"handbook", "handbook/recall.md",
		"A reciprocal rank fusion query returns cited chunks."},
	"c-storage": {"handbook", "handbook/storage.md",
		"The storage driver keeps chunks; it neither ranks nor merges."},
	"c-secret": {"journal", "journal/secrets.md",
		"quokka reciprocal rank fusion reciprocal rank fusion reciprocal rank fusion quokka"},
}

// lexicalLeg is the ONE double here, and the reason it exists is stated
// plainly: F/S-10.T2's FTS5 leg has not landed, and the service needs a
// leg to fuse. It ranks by how many query terms a chunk's content holds
// and reads ONLY chunks the scope filter resolves, which is how a real
// full-text leg narrows its scan.
type lexicalLeg struct{}

func (lexicalLeg) Query(
	_ context.Context, f *fusion.ScopeFilter, text string, topK int,
) (rrf.RankedList, bool, error) {
	terms := strings.Fields(strings.ToLower(text))
	type scored struct {
		cand  rrf.Candidate
		score int
	}
	var hits []scored
	ids := make([]string, 0, len(fixtureChunks))
	for id := range fixtureChunks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		record, ok := f.Resolve(id)
		if !ok {
			continue
		}
		body := strings.ToLower(fixtureChunks[id].body)
		total := 0
		for _, term := range terms {
			total += strings.Count(body, term)
		}
		if total == 0 {
			continue
		}
		hits = append(hits, scored{score: total, cand: rrf.Candidate{
			ChunkID: id, Path: fixtureChunks[id].path,
			CorpusID: record.CorpusID, Trust: record.Trust,
		}})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].cand.ChunkID < hits[j].cand.ChunkID
	})
	if len(hits) > topK {
		hits = hits[:topK]
	}
	out := rrf.RankedList{Strategy: rrf.StrategyFTS, Weight: rrf.NeutralWeight}
	for _, h := range hits {
		out.Hits = append(out.Hits, h.cand)
	}
	return out, true, nil
}

// leakyLeg returns a chunk the scope filter never gave it. It models the
// one failure the service's own re-resolution exists to catch: a leg that
// hands back an id it was never authorized to see.
type leakyLeg struct{ chunkID string }

func (l leakyLeg) Query(
	context.Context, *fusion.ScopeFilter, string, int,
) (rrf.RankedList, bool, error) {
	return rrf.RankedList{
		Strategy: rrf.StrategyVector, Weight: rrf.NeutralWeight,
		Hits: []rrf.Candidate{{
			ChunkID:  l.chunkID,
			Path:     fixtureChunks[l.chunkID].path,
			CorpusID: fixtureChunks[l.chunkID].corpusID,
			Trust:    corpus.TrustTrusted,
		}},
	}, true, nil
}

// skippedLeg is a leg that is not configured in this build.
type skippedLeg struct{}

func (skippedLeg) Query(
	context.Context, *fusion.ScopeFilter, string, int,
) (rrf.RankedList, bool, error) {
	return rrf.RankedList{}, false, nil
}

// failingLeg fails the query.
type failingLeg struct{}

func (failingLeg) Query(
	context.Context, *fusion.ScopeFilter, string, int,
) (rrf.RankedList, bool, error) {
	return rrf.RankedList{}, false, cascade.New(cascade.KindUnavailable, "leg: index file is locked")
}

// writeCatalog writes a real catalog document holding both corpora and
// every fixture chunk, and returns its path.
func writeCatalog(t *testing.T) string {
	t.Helper()
	doc := CatalogDoc{Version: CatalogVersion, Corpora: []corpus.Corpus{handbookCorpus, journalCorpus}}
	ids := make([]string, 0, len(fixtureChunks))
	for id := range fixtureChunks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		c := handbookCorpus
		if fixtureChunks[id].corpusID == journalCorpus.ID {
			c = journalCorpus
		}
		doc.Records = append(doc.Records, corpus.Record{
			ID: id, CorpusID: c.ID, ScopeRef: c.ScopeRef,
			Privacy: c.Privacy, Visibility: c.Visibility, Trust: c.Trust,
		})
	}
	return writeCatalogDoc(t, doc)
}

// writeCatalogDoc writes any document to a real file under t.TempDir().
func writeCatalogDoc(t *testing.T, doc CatalogDoc) string {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal catalog: %v", err)
	}
	return writeCatalogBytes(t, raw)
}

// writeCatalogBytes writes raw catalog bytes, valid or not.
func writeCatalogBytes(t *testing.T, raw []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), CatalogFileName)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write catalog: %v", err)
	}
	return path
}

// newTestService builds a service over the full fixture catalog.
func newTestService(t *testing.T, legs ...Leg) *Service {
	t.Helper()
	if len(legs) == 0 {
		legs = []Leg{lexicalLeg{}}
	}
	svc, err := NewService(NewFileCatalog(writeCatalog(t)), rrf.Params{}, legs...)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

// inScopeRequest is the querying session: a member of the handbook's scope
// only, entitled to project-tier content only.
func inScopeRequest() Request {
	return Request{Query: testQuery, Scope: "project/cascade"}
}
