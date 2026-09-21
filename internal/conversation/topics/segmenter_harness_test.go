// Package topics (segmenter_harness_test.go): Purpose: the UNTAGGED half
// of the corpus-accuracy harness - a small, hand-written synthetic corpus
// (documented in testdata/README.md) driven through the REAL
// Segment -> Evaluate -> AssertFloors path, so the scoring path this
// package will run against the owner corpus is executed on every ordinary
// `go test` run rather than only behind the topics_corpus tag, and is
// proven able to FAIL: the same corpus scored from a segmenter that never
// emits a boundary must miss the floors.
//
// Inputs: in-memory records built by syntheticCorpus(); no files, no
// network.
// Outputs: none (t.Fatal only).
// Constraints: the doubles here classify and embed FROM THE TURN TEXT, so
// neither reads the records' ground-truth labels. That is the whole point:
// a double that echoed rec.TopicLabels would make assignment accuracy 1.0
// by construction and measure nothing.
// SPORT: internal/conversation/topics segmenter (ADD) (P1-E21-W5-S45-T2).
package topics

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// syntheticTopics is the closed vocabulary the synthetic turns are written
// from. It is also the embedding space: a turn's vector is the one-hot
// vector of its topic's index here, which is why the space is exactly
// len(syntheticTopics) wide.
var syntheticTopics = []string{"alpha", "beta", "gamma", "delta"}

var syntheticEmbedModel = provider.EmbedModel{ID: "topics-synthetic-onehot-v1", Dimensions: len(syntheticTopics)}

// topicOf returns the first vocabulary topic mentioned in text, or "" when
// none is. Both doubles below derive their answer from this, so both are
// functions of the turn text alone.
func topicOf(text string) string {
	for _, topic := range syntheticTopics {
		if strings.Contains(text, topic) {
			return topic
		}
	}
	return ""
}

// keywordClassifier is a real (if deliberately simple) cheap-lane
// classifier over the request it is handed: it reads the rendered prompt -
// which carries the turn text - and answers with the topic it finds there.
// It never sees a corpus record.
type keywordClassifier struct{}

func (keywordClassifier) Execute(_ context.Context, req provider.ModelRequest) (provider.ModelResponse, error) {
	if len(req.Inputs) == 0 {
		return provider.ModelResponse{}, cascade.New(cascade.KindInvalidInput, "keywordClassifier: empty request")
	}
	label := topicOf(req.Inputs[0].Content)
	if label == "" {
		return provider.ModelResponse{}, cascade.New(cascade.KindIntegrity, "keywordClassifier: no known topic in turn")
	}
	return provider.ModelResponse{Output: label}, nil
}

// onehotEmbedder embeds a turn as the one-hot vector of its topic, so turns
// inside a topic are at distance 0 and turns across topics at distance 1 -
// vectors designed to encode the topic structure the segmenter is supposed
// to find, and again derived from the text only.
type onehotEmbedder struct{}

func (onehotEmbedder) Model() provider.EmbedModel { return syntheticEmbedModel }

func (onehotEmbedder) Embed(_ context.Context, inputs []provider.EmbedInput) ([]provider.EmbedOutput, error) {
	out := make([]provider.EmbedOutput, len(inputs))
	for i, in := range inputs {
		vec := make([]float32, len(syntheticTopics))
		found := false
		for j, topic := range syntheticTopics {
			if strings.Contains(in.Text, topic) {
				vec[j] = 1
				found = true
				break
			}
		}
		if !found {
			return nil, cascade.Newf(cascade.KindInvalidInput, "onehotEmbedder: turn %d names no known topic", i)
		}
		out[i] = provider.EmbedOutput{Vector: vec, Model: syntheticEmbedModel}
	}
	return out, nil
}

// syntheticSegment is one run of consecutive turns on one topic.
type syntheticSegment struct {
	topic string
	turns int
}

// buildSyntheticRecord expands segments into a CorpusRecord: one turn per
// step, ground-truth boundaries at every segment start after the first, and
// a ground-truth label on every turn.
func buildSyntheticRecord(id string, segments []syntheticSegment) CorpusRecord {
	rec := CorpusRecord{ID: id, TopicLabels: map[int]string{}}
	for _, seg := range segments {
		if len(rec.Turns) > 0 {
			rec.Boundaries = append(rec.Boundaries, len(rec.Turns))
		}
		for i := 0; i < seg.turns; i++ {
			rec.TopicLabels[len(rec.Turns)] = seg.topic
			rec.Turns = append(rec.Turns, Turn{
				Speaker: "a",
				Text:    "let us discuss " + seg.topic + " in more detail",
			})
		}
	}
	return rec
}

// syntheticCorpus is the three-record fixture, documented as synthetic in
// testdata/README.md. Each record opens with a short segment, because the
// predicted per-turn labels are derived from the segmenter's own boundaries
// and the implicit first segment therefore has no predicted label at all
// (see predictedLabels).
func syntheticCorpus() Corpus {
	return Corpus{
		buildSyntheticRecord("synthetic-1", []syntheticSegment{
			{"alpha", 1}, {"beta", 6}, {"gamma", 7},
		}),
		buildSyntheticRecord("synthetic-2", []syntheticSegment{
			{"beta", 1}, {"gamma", 5}, {"alpha", 6},
		}),
		buildSyntheticRecord("synthetic-3", []syntheticSegment{
			{"gamma", 2}, {"delta", 6}, {"alpha", 6},
		}),
	}
}

// predictedLabels turns a segmenter's OWN output into a per-turn label
// prediction: every turn carries the Label of the most recent Boundary at
// or before it. Turns before the first boundary get no label ("") and
// therefore score as misses, because Segment surfaces no label for the
// implicit opening segment - the per-turn assignment surface is S-45.T3's,
// and inventing a label here (for instance by reading the corpus's own
// TopicLabels) is exactly the substitution that would make this
// measurement meaningless.
func predictedLabels(turnCount int, boundaries []Boundary) map[int]string {
	labels := make(map[int]string, turnCount)
	current := ""
	next := 0
	for i := 0; i < turnCount; i++ {
		for next < len(boundaries) && boundaries[next].TurnIndex == i {
			current = boundaries[next].Label
			next++
		}
		labels[i] = current
	}
	return labels
}

// scoreCorpus runs seg over every record and scores its real output.
func scoreCorpus(t *testing.T, corpus Corpus, seg Segmenter) EvalResult {
	t.Helper()
	preds := EvalPredictions{
		Boundaries:  make(map[string][]int, len(corpus)),
		TopicLabels: make(map[string]map[int]string, len(corpus)),
	}
	for _, rec := range corpus {
		bounds, err := seg.Segment(context.Background(), rec.Turns)
		if err != nil {
			t.Fatalf("record %s: Segment: %v", rec.ID, err)
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

// newSyntheticSegmenter builds the real engine over the two text-derived
// doubles.
func newSyntheticSegmenter(t *testing.T) Segmenter {
	t.Helper()
	seg, err := newTestSegmenter(keywordClassifier{}, onehotEmbedder{}, HysteresisConfig{Threshold: 0.5, Window: 2})
	if err != nil {
		t.Fatalf("NewSegmenter: %v", err)
	}
	return seg
}

func TestSegmenterClearsAccuracyFloorsOnSyntheticCorpus(t *testing.T) {
	corpus := syntheticCorpus()
	result := scoreCorpus(t, corpus, newSyntheticSegmenter(t))
	// Logged so the margin over each floor is visible in the run output:
	// assignment accuracy is capped below 1.0 by the unlabeled opening
	// segment predictedLabels documents, and a reader should see how close
	// to the floor that leaves it.
	t.Logf("synthetic corpus (%d records): %+v", len(corpus), result)
	if err := AssertFloors(result); err != nil {
		t.Fatalf("real Segmenter on the synthetic corpus missed the floors: %v (result=%+v)", err, result)
	}
	if result.BoundaryF1 != 1.0 {
		t.Fatalf("boundary F1 = %v on a fixture whose spikes sit exactly on its labeled boundaries, want 1.0 "+
			"(result=%+v)", result.BoundaryF1, result)
	}
}

// noBoundarySegmenter is the deliberately wrong segmenter: a Segmenter that
// finds nothing, ever.
type noBoundarySegmenter struct{}

func (noBoundarySegmenter) Segment(context.Context, []Turn) ([]Boundary, error) { return nil, nil }

// TestSegmenterFloorsFailTheWrongSegmenter proves the harness above can fail. The
// same corpus and the same scoring path, fed a segmenter that emits no
// boundary, must miss BOTH floors - otherwise a green run of
// TestSegmenterClearsAccuracyFloorsOnSyntheticCorpus would say nothing
// about the segmenter.
func TestSegmenterFloorsFailTheWrongSegmenter(t *testing.T) {
	result := scoreCorpus(t, syntheticCorpus(), noBoundarySegmenter{})
	err := AssertFloors(result)
	if err == nil {
		t.Fatalf("a segmenter that never emits a boundary passed the floors (result=%+v): the harness cannot "+
			"fail, so its green runs prove nothing", result)
	}
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("AssertFloors on the wrong segmenter: error = %v, want KindIntegrity", err)
	}
	for _, want := range []string{"boundary F1", "assignment accuracy"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("AssertFloors message %q must name the %q floor it missed", err.Error(), want)
		}
	}
}
