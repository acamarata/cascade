// Package topics (eval_test.go): Purpose: unit tests for every scorer code
// path (LoadCorpusRecord, LoadCorpus, Evaluate, AssertFloors) against
// hand-crafted in-memory fixtures. No network. All file writes go under
// t.TempDir().
package topics

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func rec(id string, nTurns int, boundaries []int, labels map[int]string) CorpusRecord {
	turns := make([]Turn, nTurns)
	for i := range turns {
		turns[i] = Turn{Speaker: "a", Text: "turn"}
	}
	return CorpusRecord{ID: id, Turns: turns, Boundaries: boundaries, TopicLabels: labels}
}

func TestEvalBoundaryF1Perfect(t *testing.T) {
	corpus := Corpus{rec("r1", 4, []int{1, 3}, nil)}
	preds := EvalPredictions{Boundaries: map[string][]int{"r1": {1, 3}}}
	got := Evaluate(corpus, preds)
	if got.BoundaryF1 != 1.0 {
		t.Fatalf("BoundaryF1 = %v, want 1.0", got.BoundaryF1)
	}
	if got.BoundaryPrecision != 1.0 || got.BoundaryRecall != 1.0 {
		t.Fatalf("precision/recall = %v/%v, want 1.0/1.0", got.BoundaryPrecision, got.BoundaryRecall)
	}
}

func TestEvalBoundaryF1Zero(t *testing.T) {
	corpus := Corpus{rec("r1", 4, []int{1, 3}, nil)}
	preds := EvalPredictions{Boundaries: map[string][]int{"r1": {0, 2}}}
	got := Evaluate(corpus, preds)
	if got.BoundaryF1 != 0.0 {
		t.Fatalf("BoundaryF1 = %v, want 0.0", got.BoundaryF1)
	}
}

func TestEvalBoundaryF1Midpoint(t *testing.T) {
	// Want {1,2,3}, predicted {1,2,4}: tp=2, fp=1, fn=1 -> P=2/3, R=2/3, F1=2/3.
	corpus := Corpus{rec("r1", 5, []int{1, 2, 3}, nil)}
	preds := EvalPredictions{Boundaries: map[string][]int{"r1": {1, 2, 4}}}
	got := Evaluate(corpus, preds)
	want := 2.0 / 3.0
	if diff := got.BoundaryF1 - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("BoundaryF1 = %v, want %v", got.BoundaryF1, want)
	}
}

func TestEvalAssignmentAccuracyPerfect(t *testing.T) {
	corpus := Corpus{rec("r1", 2, nil, map[int]string{0: "greeting", 1: "billing"})}
	preds := EvalPredictions{TopicLabels: map[string]map[int]string{
		"r1": {0: "greeting", 1: "billing"},
	}}
	got := Evaluate(corpus, preds)
	if got.AssignmentAccuracy != 1.0 {
		t.Fatalf("AssignmentAccuracy = %v, want 1.0", got.AssignmentAccuracy)
	}
}

func TestEvalAssignmentAccuracyZero(t *testing.T) {
	corpus := Corpus{rec("r1", 2, nil, map[int]string{0: "greeting", 1: "billing"})}
	preds := EvalPredictions{TopicLabels: map[string]map[int]string{
		"r1": {0: "billing", 1: "greeting"},
	}}
	got := Evaluate(corpus, preds)
	if got.AssignmentAccuracy != 0.0 {
		t.Fatalf("AssignmentAccuracy = %v, want 0.0", got.AssignmentAccuracy)
	}
}

func TestEvalEmptyCorpus(t *testing.T) {
	got := Evaluate(nil, EvalPredictions{})
	want := EvalResult{}
	if got != want {
		t.Fatalf("Evaluate(nil, {}) = %+v, want zero value", got)
	}
}

func TestEvalEmptyPredictions(t *testing.T) {
	corpus := Corpus{rec("r1", 3, []int{1}, map[int]string{0: "x"})}
	got := Evaluate(corpus, EvalPredictions{})
	if got.BoundaryF1 != 0 || got.AssignmentAccuracy != 0 {
		t.Fatalf("Evaluate with empty predictions = %+v, want all-zero scores", got)
	}
}

func TestLoadCorpusRecordValid(t *testing.T) {
	data := []byte(`{"id":"r1","turns":[{"speaker":"a","text":"hi"},{"speaker":"b","text":"bye"}],
		"boundaries":[1],"topic_labels":{"0":"greeting"}}`)
	got, err := LoadCorpusRecord(data)
	if err != nil {
		t.Fatalf("LoadCorpusRecord: %v", err)
	}
	if got.ID != "r1" || len(got.Turns) != 2 || got.TopicLabels[0] != "greeting" {
		t.Fatalf("LoadCorpusRecord = %+v, unexpected", got)
	}
}

func TestLoadCorpusRecordErrors(t *testing.T) {
	cases := []struct {
		name string
		data string
	}{
		{"invalid JSON", `{not json`},
		{"empty id", `{"id":"","turns":[{"speaker":"a","text":"hi"}]}`},
		{"no turns", `{"id":"r1","turns":[]}`},
		{"boundary out of range", `{"id":"r1","turns":[{"speaker":"a","text":"hi"}],"boundaries":[5]}`},
		{"label key not integer", `{"id":"r1","turns":[{"speaker":"a","text":"hi"}],"topic_labels":{"x":"y"}}`},
		{"label index out of range", `{"id":"r1","turns":[{"speaker":"a","text":"hi"}],"topic_labels":{"9":"y"}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := LoadCorpusRecord([]byte(c.data))
			if err == nil {
				t.Fatal("LoadCorpusRecord: want error, got nil")
			}
			if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
				t.Fatalf("KindOf(err) = %v, %v; want KindInvalidInput, true", kind, ok)
			}
		})
	}
}

func TestLoadCorpus(t *testing.T) {
	dir := t.TempDir()
	writeCorpusFile(t, dir, "a.json", `{"id":"a","turns":[{"speaker":"x","text":"hi"}]}`)
	writeCorpusFile(t, dir, "b.json", `{"id":"b","turns":[{"speaker":"x","text":"hi"}]}`)
	writeCorpusFile(t, dir, "README.md", "not a record")

	corpus, err := LoadCorpus(dir)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if len(corpus) != 2 || corpus[0].ID != "a" || corpus[1].ID != "b" {
		t.Fatalf("LoadCorpus = %+v, want sorted [a b]", corpus)
	}
}

func TestLoadCorpusMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	_, err := LoadCorpus(dir)
	if err == nil {
		t.Fatal("LoadCorpus: want error for missing dir, got nil")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindNotFound {
		t.Fatalf("KindOf(err) = %v, %v; want KindNotFound, true", kind, ok)
	}
}

func TestLoadCorpusMalformedRecord(t *testing.T) {
	dir := t.TempDir()
	writeCorpusFile(t, dir, "bad.json", `{"id":"","turns":[]}`)
	_, err := LoadCorpus(dir)
	if err == nil {
		t.Fatal("LoadCorpus: want error for malformed record, got nil")
	}
}

func TestAssertFloors(t *testing.T) {
	if err := AssertFloors(EvalResult{BoundaryF1: 0.80, AssignmentAccuracy: 0.85}); err != nil {
		t.Fatalf("AssertFloors at exact floor: %v", err)
	}
	err := AssertFloors(EvalResult{BoundaryF1: 0.5, AssignmentAccuracy: 0.5})
	if err == nil {
		t.Fatal("AssertFloors below both floors: want error, got nil")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindIntegrity {
		t.Fatalf("KindOf(err) = %v, %v; want KindIntegrity, true", kind, ok)
	}
}

func writeCorpusFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("writeCorpusFile %s: %v", name, err)
	}
}
