// Package topics (segmenter_errors_test.go): Purpose: every refusal and
// propagation path of segmenter_errors.go, driven through the real Segment
// pipeline: a classifier or embedder failure reaching the caller with its
// own cause and Kind intact, each half of the EmbedModel.ValidBatch
// contract refusal, and the zero-norm vector ValidBatch cannot see. Split
// out of segmenter_core_test.go under the 300-line file cap (Art.10.3);
// the doubles and helpers it uses (fakeClassifyExecutor, fakeEmbedder,
// turnsN, validCfg, assertCause) are declared there.
package topics

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestSegmenter_ClassifierErrorPropagates(t *testing.T) {
	wantErr := cascade.New(cascade.KindUnavailable, "classify lane down")
	exec := &fakeClassifyExecutor{err: wantErr}
	s, _ := NewSegmenter(exec, &fakeEmbedder{vectors: [][]float32{{1, 0}}}, validCfg())
	_, err := s.Segment(context.Background(), turnsN(1))
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("classifier error: got %v, want KindUnavailable preserved", err)
	}
	assertCause(t, err, wantErr)
}

func TestSegmenter_EmbedderErrorPropagates(t *testing.T) {
	wantErr := cascade.New(cascade.KindTimeout, "embed lane timed out")
	emb := &fakeEmbedder{err: wantErr}
	s, _ := NewSegmenter(&fakeClassifyExecutor{labels: []string{"A"}}, emb, validCfg())
	_, err := s.Segment(context.Background(), turnsN(1))
	if !cascade.HasKind(err, cascade.KindTimeout) {
		t.Fatalf("embedder error: got %v, want KindTimeout preserved", err)
	}
	assertCause(t, err, wantErr)
}

// TestSegmenter_EmbedBatchContractViolations covers each half of
// EmbedModel.ValidBatch through the real production check. The
// mixed-dimension case is the important one: a batch whose vectors have
// different widths would otherwise be compared with a partial cosine over
// the shorter prefix and yield confident-looking boundaries from
// meaningless numbers.
func TestSegmenter_EmbedBatchContractViolations(t *testing.T) {
	otherModel := provider.EmbedModel{ID: "some-other-space", Dimensions: 2}
	cases := map[string]*fakeEmbedder{
		"fewer outputs than inputs": {vectors: [][]float32{{1, 0}}},
		"mixed dimensions":          {vectors: [][]float32{{1, 0}, {1, 0, 0}}},
		"wrong width":               {vectors: [][]float32{{1}, {0}}},
		"foreign embedding space":   {vectors: [][]float32{{1, 0}, {0, 1}}, outModel: &otherModel},
	}
	for name, emb := range cases {
		t.Run(name, func(t *testing.T) {
			s, _ := NewSegmenter(&fakeClassifyExecutor{labels: []string{"A", "B"}}, emb, validCfg())
			bounds, err := s.Segment(context.Background(), turnsN(2))
			if !cascade.HasKind(err, cascade.KindInvalidInput) {
				t.Fatalf("%s: got (%v, %v), want KindInvalidInput", name, bounds, err)
			}
			if bounds != nil {
				t.Fatalf("%s: got boundaries %v alongside the refusal, want none", name, bounds)
			}
		})
	}
}

// TestSegmenter_ZeroNormEmbeddingIsAnError pins the one malformed vector
// ValidBatch cannot see: correctly-shaped, correctly-tagged, and all
// zeros. Treating it as distance 0 would silently suppress every boundary
// at that turn while reporting success.
func TestSegmenter_ZeroNormEmbeddingIsAnError(t *testing.T) {
	emb := &fakeEmbedder{vectors: [][]float32{{0, 0}, {1, 0}}}
	s, _ := NewSegmenter(&fakeClassifyExecutor{labels: []string{"A", "B"}}, emb, validCfg())
	bounds, err := s.Segment(context.Background(), turnsN(2))
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("zero-norm embedding: got (%v, %v), want KindInvalidInput", bounds, err)
	}
	if !strings.Contains(err.Error(), "zero-norm") || !strings.Contains(err.Error(), "turns 0 and 1") {
		t.Fatalf("zero-norm embedding: message %q must name the failure and the turn pair", err.Error())
	}
}

func TestSegmenter_EmptyClassifyLabelPropagates(t *testing.T) {
	exec := &fakeClassifyExecutor{labels: []string{"   "}}
	s, _ := NewSegmenter(exec, &fakeEmbedder{vectors: [][]float32{{1, 0}}}, validCfg())
	_, err := s.Segment(context.Background(), turnsN(1))
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("empty classify label: got %v, want KindIntegrity", err)
	}
}
