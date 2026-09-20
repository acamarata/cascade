//go:build topics_corpus

// Package topics (segmenter_corpus_fixture_test.go): Purpose: the fixture
// half of the corpus harness - the optional per-record "embeddings" and
// "classifier_predictions" arrays (format: testdata/README.md), the loader
// that reads them, the Embedder and cheap-lane doubles that replay them,
// the refusal a record without embeddings gets, and the tests that execute
// those refusals while testdata/corpus/ is still empty. Split out of
// segmenter_corpus_test.go under the 300-line file cap (Art.10.3).
//
// Inputs: *.json records under a corpus directory (a t.TempDir one, in the
// tests here).
// Outputs: none (t.Fatal only).
// Constraints: no network; the doubles replay recorded data and refuse a
// batch or a label they have no fixture for rather than inventing one.
// SPORT: internal/conversation/topics segmenter (ADD) (P1-E21-W5-S45-T2).
package topics

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// corpusFixture is the optional, segmenter-specific half of a corpus
// record: precomputed per-turn embeddings and recorded per-turn cheap-lane
// predictions. eval.go's CorpusRecord deliberately does not carry these
// (T1 owns that type and its ground-truth fields); they are read here.
type corpusFixture struct {
	ID          string      `json:"id"`
	Embeddings  [][]float32 `json:"embeddings"`
	Predictions []string    `json:"classifier_predictions"`
}

// loadCorpusFixtures reads the same *.json files LoadCorpus reads and
// returns their fixture halves keyed by record id.
func loadCorpusFixtures(dir string) (map[string]corpusFixture, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	out := make(map[string]corpusFixture, len(names))
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		var fx corpusFixture
		if err := json.Unmarshal(data, &fx); err != nil {
			return nil, err
		}
		out[fx.ID] = fx
	}
	return out, nil
}

// requireFixtureEmbeddings reports why rec cannot be scored when its
// fixture half carries no per-turn embeddings. It is a returned error
// rather than an inline t.Fatalf so the refusal itself is exercised by
// TestSegmenterCorpusFixtureEmbeddingsAreRequired below even while testdata/corpus/
// is empty - the alternative is a refusal path that has never run.
func requireFixtureEmbeddings(rec CorpusRecord, fx corpusFixture) error {
	if len(fx.Embeddings) != 0 {
		return nil
	}
	return cascade.Newf(cascade.KindInvalidInput,
		"record %q carries no \"embeddings\" and this test configures no live embedder (Art.7: no network in "+
			"tests). Scoring a real segmenter against vectors this test invented would measure the fixture, "+
			"not the segmenter, so this is a FAILURE rather than a skip. Add per-turn embeddings to the "+
			"record (format: testdata/README.md).", rec.ID)
}

// fixtureEmbedder replays a record's precomputed vectors. It is a real
// Embedder implementation, contract included: it refuses a batch size it
// has no vectors for rather than padding one.
type fixtureEmbedder struct {
	vectors [][]float32
	model   provider.EmbedModel
}

func (f fixtureEmbedder) Model() provider.EmbedModel { return f.model }

func (f fixtureEmbedder) Embed(_ context.Context, inputs []provider.EmbedInput) ([]provider.EmbedOutput, error) {
	if len(inputs) != len(f.vectors) {
		return nil, cascade.Newf(cascade.KindInvalidInput,
			"fixtureEmbedder: asked for %d vectors, fixture holds %d", len(inputs), len(f.vectors))
	}
	out := make([]provider.EmbedOutput, len(inputs))
	for i := range inputs {
		out[i] = provider.EmbedOutput{Vector: f.vectors[i], Model: f.model}
	}
	return out, nil
}

// newFixtureEmbedder validates a record's fixture vectors (one per turn,
// all the same width) and returns an Embedder over them.
func newFixtureEmbedder(rec CorpusRecord, vectors [][]float32) (fixtureEmbedder, error) {
	if len(vectors) != len(rec.Turns) {
		return fixtureEmbedder{}, cascade.Newf(cascade.KindInvalidInput,
			"record %q: %d embeddings for %d turns", rec.ID, len(vectors), len(rec.Turns))
	}
	width := len(vectors[0])
	for i, v := range vectors {
		if len(v) != width {
			return fixtureEmbedder{}, cascade.Newf(cascade.KindInvalidInput,
				"record %q: embedding %d is %d wide, embedding 0 is %d wide", rec.ID, i, len(v), width)
		}
	}
	return fixtureEmbedder{
		vectors: vectors,
		model:   provider.EmbedModel{ID: "topics-corpus-fixture-v1", Dimensions: width},
	}, nil
}

// replayClassifier hands back one recorded label per Execute call, in call
// order (Segment classifies turns strictly in sequence).
type replayClassifier struct {
	labels []string
	i      int
}

func (r *replayClassifier) Execute(context.Context, provider.ModelRequest) (provider.ModelResponse, error) {
	if r.i >= len(r.labels) {
		return provider.ModelResponse{}, cascade.Newf(cascade.KindIntegrity,
			"replayClassifier: asked for label %d, fixture holds %d", r.i, len(r.labels))
	}
	out := provider.ModelResponse{Output: r.labels[r.i]}
	r.i++
	return out, nil
}

// TestSegmenterCorpusFixtureEmbeddingsAreRequired executes the two refusals the
// empty corpus leaves unreached: a record with no fixture embeddings is
// refused outright, and a fixture whose vectors do not match the record
// (count or width) never reaches the segmenter.
func TestSegmenterCorpusFixtureEmbeddingsAreRequired(t *testing.T) {
	rec := CorpusRecord{ID: "fixture-refusals", Turns: turnsN(2)}
	err := requireFixtureEmbeddings(rec, corpusFixture{})
	if err == nil || !strings.Contains(err.Error(), "FAILURE rather than a skip") {
		t.Fatalf("missing embeddings: error = %v, want an explicit refusal", err)
	}
	if err := requireFixtureEmbeddings(rec, corpusFixture{Embeddings: [][]float32{{1, 0}, {0, 1}}}); err != nil {
		t.Fatalf("present embeddings: error = %v, want nil", err)
	}
	for name, vectors := range map[string][][]float32{
		"one vector for two turns": {{1, 0}},
		"ragged widths":            {{1, 0}, {1}},
	} {
		if _, err := newFixtureEmbedder(rec, vectors); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("%s: error = %v, want KindInvalidInput", name, err)
		}
	}
	emb, err := newFixtureEmbedder(rec, [][]float32{{1, 0}, {0, 1}})
	if err != nil {
		t.Fatalf("newFixtureEmbedder: %v", err)
	}
	if _, err := emb.Embed(context.Background(), make([]provider.EmbedInput, 1)); err == nil {
		t.Fatal("fixtureEmbedder answered a batch it has no vectors for, instead of refusing it")
	}
}

// TestSegmenterCorpusFixtureFormatIsParsed pins the fixture format
// documented in testdata/README.md against a written file, so a record that
// carries embeddings and recorded predictions is actually parsed rather
// than silently ignored.
func TestSegmenterCorpusFixtureFormatIsParsed(t *testing.T) {
	dir := t.TempDir()
	const rec = `{"id":"r1","turns":[{"speaker":"a","text":"x"},{"speaker":"b","text":"y"}],` +
		`"boundaries":[1],"topic_labels":{"0":"a","1":"b"},` +
		`"embeddings":[[1,0],[0,1]],"classifier_predictions":["a","b"]}`
	if err := os.WriteFile(filepath.Join(dir, "r1.json"), []byte(rec), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	fixtures, err := loadCorpusFixtures(dir)
	if err != nil {
		t.Fatalf("loadCorpusFixtures: %v", err)
	}
	fx, ok := fixtures["r1"]
	if !ok {
		t.Fatalf("loadCorpusFixtures returned %v, want an entry keyed r1", fixtures)
	}
	if len(fx.Embeddings) != 2 || len(fx.Embeddings[1]) != 2 || fx.Embeddings[1][1] != 1 {
		t.Fatalf("embeddings = %v, want [[1,0],[0,1]]", fx.Embeddings)
	}
	if len(fx.Predictions) != 2 || fx.Predictions[1] != "b" {
		t.Fatalf("classifier_predictions = %v, want [a b]", fx.Predictions)
	}
}
