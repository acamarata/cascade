//go:build topics_corpus

// Package topics (segmenter_corpus_test.go): Purpose: TestSegmenterCorpus-
// Accuracy scores a REAL Segmenter against the owner-supplied corpus
// (testdata/corpus/, P1-E21-W5-S45-T1) and requires the boundary-F1 >=0.80
// / assignment-accuracy >=0.85 floors from 06-FORGE-SPEC §5 rule 12.
//
// Inputs: JSON records under testdata/corpus/. Beyond the fields eval.go's
// CorpusRecord reads, a record may carry two OPTIONAL fixture fields this
// test reads directly (format documented in testdata/README.md):
//
//	"embeddings":             [[...], ...]  one vector per turn
//	"classifier_predictions": ["...", ...]  one recorded cheap-lane label per turn
//
// Outputs: none (t.Fatal/t.Skip only).
//
// Constraints: no network, no live model or embedding call. THIS TEST HAS
// NEVER BEEN EXECUTED against real data: testdata/corpus/ holds no records
// (06-FORGE-SPEC §7 owner prerequisite), so it skips, and no CI job runs
// the topics_corpus tag today - see .github/wiki/Topic-Engine.md. When a
// record IS present it must carry "embeddings": scoring a real segmenter
// against vectors this test invented would measure the fixture rather than
// the segmenter, so a record without them FAILS here instead of quietly
// degrading. "classifier_predictions" is optional; without it the classify
// step is replayed from the record's own labels, which makes the classify
// half an oracle and leaves the boundary and label-propagation halves as
// the real measurement. That is stated in the skip/log output rather than
// left for a reader of this file to discover.
// SPORT: internal/conversation/topics segmenter (ADD) (P1-E21-W5-S45-T2).
package topics

import (
	"context"
	"testing"
)

const corpusDir = "testdata/corpus"

// recordLabels returns rec's TopicLabels in turn order, defaulting an
// unlabeled turn to the label of the nearest preceding labeled turn, so the
// replay classifier always has exactly one label per turn.
func recordLabels(rec CorpusRecord) []string {
	labels := make([]string, len(rec.Turns))
	last := ""
	for i := range rec.Turns {
		if l, ok := rec.TopicLabels[i]; ok {
			last = l
		}
		labels[i] = last
	}
	return labels
}

// classifyFixture picks the per-turn classify labels for rec: the recorded
// predictions when the fixture carries them, otherwise the record's own
// labels, which is announced because it makes the classify half an oracle.
func classifyFixture(t *testing.T, rec CorpusRecord, fx corpusFixture) []string {
	t.Helper()
	if len(fx.Predictions) == len(rec.Turns) {
		return fx.Predictions
	}
	if len(fx.Predictions) != 0 {
		t.Fatalf("record %q: %d classifier_predictions for %d turns", rec.ID, len(fx.Predictions), len(rec.Turns))
	}
	t.Logf("record %q carries no classifier_predictions: replaying its own labels, so the classify step is an "+
		"oracle here and only the boundary and label-propagation halves are measured", rec.ID)
	return recordLabels(rec)
}

func TestSegmenterCorpusAccuracy(t *testing.T) {
	corpus, err := LoadCorpus(corpusDir)
	if err != nil {
		t.Skipf("UNEXECUTED OWNER PREREQUISITE (06-FORGE-SPEC §7, P1-E21-W5-S45-T1's labeled corpus): %s is "+
			"unreadable, so this test has never run against real data: %v", corpusDir, err)
	}
	if len(corpus) == 0 {
		t.Skipf("UNEXECUTED OWNER PREREQUISITE (06-FORGE-SPEC §7): %s holds no *.json records, so this test "+
			"has never run against real data. The untagged synthetic harness "+
			"(TestSegmenterClearsAccuracyFloorsOnSyntheticCorpus) is what exercises the scoring path today.", corpusDir)
	}
	fixtures, err := loadCorpusFixtures(corpusDir)
	if err != nil {
		t.Fatalf("reading the corpus fixture halves from %s: %v", corpusDir, err)
	}
	result := scoreRealCorpus(t, corpus, fixtures)
	if err := AssertFloors(result); err != nil {
		t.Fatalf("real Segmenter predictions missed the accuracy floors: %v (result=%+v)", err, result)
	}
}

// scoreRealCorpus runs the real engine over every record and scores its own
// output: boundaries as Segment returned them, per-turn labels derived from
// those boundaries by predictedLabels (segmenter_harness_test.go) - never
// from the record's ground truth.
func scoreRealCorpus(t *testing.T, corpus Corpus, fixtures map[string]corpusFixture) EvalResult {
	t.Helper()
	preds := EvalPredictions{
		Boundaries:  make(map[string][]int, len(corpus)),
		TopicLabels: make(map[string]map[int]string, len(corpus)),
	}
	for _, rec := range corpus {
		fx := fixtures[rec.ID]
		if err := requireFixtureEmbeddings(rec, fx); err != nil {
			t.Fatal(err)
		}
		emb, err := newFixtureEmbedder(rec, fx.Embeddings)
		if err != nil {
			t.Fatalf("record %q: %v", rec.ID, err)
		}
		seg, err := newTestSegmenter(&replayClassifier{labels: classifyFixture(t, rec, fx)}, emb,
			HysteresisConfig{Threshold: 0.3, Window: 2})
		if err != nil {
			t.Fatalf("record %q: NewSegmenter: %v", rec.ID, err)
		}
		bounds, err := seg.Segment(context.Background(), rec.Turns)
		if err != nil {
			t.Fatalf("record %q: Segment: %v", rec.ID, err)
		}
		idxs := make([]int, len(bounds))
		for i, b := range bounds {
			idxs[i] = b.TurnIndex
		}
		preds.Boundaries[rec.ID] = idxs
		preds.TopicLabels[rec.ID] = predictedLabels(len(rec.Turns), bounds)
	}
	return Evaluate(corpus, preds)
}
