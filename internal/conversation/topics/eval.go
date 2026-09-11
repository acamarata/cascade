// Package topics (eval.go): Purpose: the corpus record format,
// load-and-validate function, and evaluation scorers (boundary F1,
// topic-assignment accuracy) that this ticket and T2's segmenter share.
//
// Inputs: JSON corpus records on disk (see CorpusRecord) and an
// EvalPredictions value a caller derives from its own segmenter/classifier
// output.
//
// Outputs: a validated Corpus, or an EvalResult scoring predictions against
// a corpus's labels.
//
// Constraints: no network, no bare time/rand; every error returned to a
// caller wraps one of pkg/cascade's frozen Kinds. LoadCorpus and Evaluate
// must not panic on an empty corpus or empty predictions.
//
// SPORT: internal/conversation/topics corpus+eval-harness (ADD), N/S-28.T1
// taxonomy pending.
package topics

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Turn is one speaker turn in a corpus record.
type Turn struct {
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
}

// CorpusRecord is one labeled transcript: its turns, the turn indices where
// a human labeler marked a topic boundary, and the topic label assigned to
// each turn. TopicLabels is keyed by turn index.
type CorpusRecord struct {
	ID          string         `json:"id"`
	Turns       []Turn         `json:"turns"`
	Boundaries  []int          `json:"boundaries"`
	TopicLabels map[int]string `json:"topic_labels"`
}

// Corpus is an ordered set of validated corpus records.
type Corpus []CorpusRecord

// jsonRecord mirrors CorpusRecord's wire shape. JSON object keys are always
// strings, so topic_labels arrives as map[string]string and is converted to
// TopicLabels' map[int]string by LoadCorpusRecord.
type jsonRecord struct {
	ID          string            `json:"id"`
	Turns       []Turn            `json:"turns"`
	Boundaries  []int             `json:"boundaries"`
	TopicLabels map[string]string `json:"topic_labels"`
}

// LoadCorpusRecord parses and validates one corpus record from data. It
// returns a *cascade.Error with KindInvalidInput for any malformed input:
// unparseable JSON, an empty id, no turns, an out-of-range boundary index,
// or a topic_labels key that is not a valid in-range turn index.
//
// This is the function FuzzCorpusRecord (eval_fuzz_test.go) exercises.
func LoadCorpusRecord(data []byte) (CorpusRecord, error) {
	var raw jsonRecord
	if err := json.Unmarshal(data, &raw); err != nil {
		return CorpusRecord{}, cascade.Wrap(cascade.KindInvalidInput, err, "corpus record: invalid JSON")
	}
	if strings.TrimSpace(raw.ID) == "" {
		return CorpusRecord{}, cascade.New(cascade.KindInvalidInput, "corpus record: id is empty")
	}
	if len(raw.Turns) == 0 {
		return CorpusRecord{}, cascade.New(cascade.KindInvalidInput, "corpus record: no turns")
	}
	n := len(raw.Turns)
	for _, b := range raw.Boundaries {
		if b < 0 || b >= n {
			return CorpusRecord{}, cascade.Newf(cascade.KindInvalidInput,
				"corpus record %q: boundary index %d out of range [0,%d)", raw.ID, b, n)
		}
	}
	labels := make(map[int]string, len(raw.TopicLabels))
	for k, v := range raw.TopicLabels {
		idx, err := strconv.Atoi(k)
		if err != nil {
			return CorpusRecord{}, cascade.Wrapf(cascade.KindInvalidInput, err,
				"corpus record %q: topic_labels key %q is not an integer turn index", raw.ID, k)
		}
		if idx < 0 || idx >= n {
			return CorpusRecord{}, cascade.Newf(cascade.KindInvalidInput,
				"corpus record %q: topic_labels index %d out of range [0,%d)", raw.ID, idx, n)
		}
		labels[idx] = v
	}
	return CorpusRecord{ID: raw.ID, Turns: raw.Turns, Boundaries: raw.Boundaries, TopicLabels: labels}, nil
}

// LoadCorpus reads every "*.json" file directly under dir (non-recursive,
// sorted by filename for determinism), validates each with
// LoadCorpusRecord, and returns the resulting Corpus. A missing dir
// returns a *cascade.Error with KindNotFound so callers can distinguish
// "corpus not yet delivered" from a malformed record.
func LoadCorpus(dir string) (Corpus, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, cascade.Wrapf(cascade.KindNotFound, err, "corpus dir %s not found", dir)
		}
		return nil, cascade.Wrapf(cascade.KindInternal, err, "reading corpus dir %s", dir)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	corpus := make(Corpus, 0, len(names))
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, cascade.Wrapf(cascade.KindInternal, err, "reading corpus record %s", name)
		}
		rec, err := LoadCorpusRecord(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		corpus = append(corpus, rec)
	}
	return corpus, nil
}

// EvalPredictions holds a caller's predicted boundaries and topic labels,
// keyed by CorpusRecord.ID, for scoring against a Corpus.
type EvalPredictions struct {
	Boundaries  map[string][]int
	TopicLabels map[string]map[int]string
}

// EvalResult is the output of Evaluate: micro-averaged boundary-detection
// precision/recall/F1 across every record, and per-turn topic-assignment
// accuracy across every labeled turn.
type EvalResult struct {
	BoundaryPrecision  float64
	BoundaryRecall     float64
	BoundaryF1         float64
	AssignmentAccuracy float64
}

// Evaluate scores predictions against corpus. It micro-averages boundary
// precision/recall/F1 (true/false positives and false negatives summed
// across every record before dividing) and computes topic-assignment
// accuracy as the agreement rate over every corpus-labeled turn. An empty
// corpus, or a corpus with no boundaries and no labels, returns a
// zero-value EvalResult without panicking.
func Evaluate(corpus Corpus, predictions EvalPredictions) EvalResult {
	var tp, fp, fn, correct, total int
	for _, rec := range corpus {
		want := toSet(rec.Boundaries)
		got := toSet(predictions.Boundaries[rec.ID])
		for idx := range got {
			if want[idx] {
				tp++
			} else {
				fp++
			}
		}
		for idx := range want {
			if !got[idx] {
				fn++
			}
		}
		predLabels := predictions.TopicLabels[rec.ID]
		for idx, label := range rec.TopicLabels {
			total++
			if predLabels != nil && predLabels[idx] == label {
				correct++
			}
		}
	}
	return EvalResult{
		BoundaryPrecision:  ratio(tp, tp+fp),
		BoundaryRecall:     ratio(tp, tp+fn),
		BoundaryF1:         f1(tp, fp, fn),
		AssignmentAccuracy: ratio(correct, total),
	}
}

// toSet converts a slice of turn indices into a membership set.
func toSet(idxs []int) map[int]bool {
	set := make(map[int]bool, len(idxs))
	for _, i := range idxs {
		set[i] = true
	}
	return set
}

// ratio returns num/den, or 0 when den is 0 (rather than NaN), so an empty
// corpus or empty prediction set scores 0 instead of panicking or
// propagating NaN into downstream comparisons.
func ratio(num, den int) float64 {
	if den == 0 {
		return 0
	}
	return float64(num) / float64(den)
}

// f1 returns the harmonic mean of precision and recall derived from tp, fp,
// fn, or 0 when both precision and recall are 0.
func f1(tp, fp, fn int) float64 {
	p := ratio(tp, tp+fp)
	r := ratio(tp, tp+fn)
	if p+r == 0 {
		return 0
	}
	return 2 * p * r / (p + r)
}

// Floor targets from 06-FORGE-SPEC.md §5 rule 12: boundary F1 >=0.80,
// topic-assignment accuracy >=0.85 for internal/conversation/topics.
const (
	BoundaryF1Floor         = 0.80
	AssignmentAccuracyFloor = 0.85
)

// AssertFloors returns nil when result meets both accuracy floors, and
// otherwise a *cascade.Error with KindIntegrity naming which floor(s)
// failed and by how much. T2's TestSegmenterCorpusAccuracy calls this
// against real segmenter predictions.
func AssertFloors(result EvalResult) error {
	var failures []string
	if result.BoundaryF1 < BoundaryF1Floor {
		failures = append(failures, fmt.Sprintf("boundary F1 %.4f < floor %.2f", result.BoundaryF1, BoundaryF1Floor))
	}
	if result.AssignmentAccuracy < AssignmentAccuracyFloor {
		failures = append(failures, fmt.Sprintf("assignment accuracy %.4f < floor %.2f",
			result.AssignmentAccuracy, AssignmentAccuracyFloor))
	}
	if len(failures) == 0 {
		return nil
	}
	return cascade.New(cascade.KindIntegrity, strings.Join(failures, "; "))
}
