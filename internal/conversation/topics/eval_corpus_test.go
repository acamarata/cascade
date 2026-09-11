//go:build topics_corpus

// Package topics (eval_corpus_test.go): Purpose: TestCorpusAccuracyFloors
// loads the committed owner corpus (testdata/corpus/) and asserts the
// corpus-loading and scoring machinery works end to end against the
// boundary-F1 >=0.80 / assignment-accuracy >=0.85 floors from
// 06-FORGE-SPEC §5 rule 12, using stub perfect-oracle predictions derived
// from the corpus's own labels.
//
// Inputs: JSON records under testdata/corpus/.
//
// Outputs: none (t.Fatal/t.Skip only).
//
// Constraints: no network. Skips cleanly via t.Skip when
// testdata/corpus/ is absent or contains no *.json records, per the
// 06-FORGE-SPEC §7 owner prerequisite (sanitized labeled transcript
// corpus) — see testdata/corpus/README.md. Only run with
// `-tags topics_corpus`; CI runs this tag with the corpus present.
package topics

import "testing"

// TestCorpusAccuracyFloors is the floor-assertion harness T2's
// TestSegmenterCorpusAccuracy consumes as a pattern: load the corpus, score
// predictions against it with Evaluate, then require AssertFloors to pass.
// Here the "predictions" are the corpus's own labels (a perfect oracle),
// which exercises the full load -> score -> assert pipeline without
// depending on a segmenter implementation that does not exist until T2.
func TestCorpusAccuracyFloors(t *testing.T) {
	corpus, err := LoadCorpus("testdata/corpus")
	if err != nil {
		t.Skipf("corpus unavailable, skipping (06-FORGE-SPEC §7 owner prerequisite): %v", err)
	}
	if len(corpus) == 0 {
		t.Skip("corpus dir present but contains no *.json records; awaiting owner-delivered corpus (06-FORGE-SPEC §7)")
	}

	preds := EvalPredictions{
		Boundaries:  make(map[string][]int, len(corpus)),
		TopicLabels: make(map[string]map[int]string, len(corpus)),
	}
	for _, rec := range corpus {
		preds.Boundaries[rec.ID] = rec.Boundaries
		preds.TopicLabels[rec.ID] = rec.TopicLabels
	}

	result := Evaluate(corpus, preds)
	if err := AssertFloors(result); err != nil {
		t.Fatalf("perfect-oracle predictions failed to clear accuracy floors (harness bug, not a corpus problem): %v", err)
	}
}
