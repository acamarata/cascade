package context

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose: pipeline.go's stage-level tests (P1-E21-W5-S46-T3): construction
//   refusals, the empty-input guards, the request each stage actually
//   builds, and its error paths. The three TRANSFORMS are asserted on the
//   output they write back in pipeline_transform_test.go, the composer
//   wiring in pipeline_compose_test.go, and lane policy in
//   pipeline_lane_test.go (four files because one would exceed Art.10.3's
//   300-line cap).
// Constraints: Art.1 -- every double here lives under _test.go. stageExecutor
//   is this file's own recording provider.ModelExecutor (the ticket's "mock
//   Conductor" task (d)): summarizer_core_test.go's fakeExecutor returns one
//   fixed output for every call and cannot script the per-protocol responses
//   these transforms parse.
// SPORT: context-engine/pipeline-tests (ADD, P1-E21-W5-S46-T3).

// stageExecutor records every request and replies from a scripted queue.
type stageExecutor struct {
	mu       sync.Mutex
	requests []provider.ModelRequest
	replies  []string
	err      error
}

func (e *stageExecutor) Execute(_ context.Context, req provider.ModelRequest) (provider.ModelResponse, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.requests = append(e.requests, req)
	if e.err != nil {
		return provider.ModelResponse{}, e.err
	}
	out := ""
	if n := len(e.requests) - 1; n < len(e.replies) {
		out = e.replies[n]
	}
	return provider.ModelResponse{Output: out}, nil
}

func (e *stageExecutor) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.requests)
}

func (e *stageExecutor) requestAt(i int) provider.ModelRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.requests[i]
}

// runeCounter measures a rune per token, so "shorter" is a real comparison
// (fixedTokenCounter reports the same count for every string and could not
// tell a condensation from an expansion).
type runeCounter struct{}

func (runeCounter) Count(_ context.Context, text string) (int, error) { return len([]rune(text)), nil }

// mustPreStage and mustSummarizeStage build a stage or fail the test.
func mustPreStage(t *testing.T, kind PreStageKind, exec provider.ModelExecutor) PipelineStage {
	t.Helper()
	stage, err := NewPreStage(kind, exec)
	if err != nil {
		t.Fatalf("NewPreStage(%v): %v", kind, err)
	}
	return stage
}

func mustSummarizeStage(t *testing.T, exec provider.ModelExecutor) PipelineStage {
	t.Helper()
	stage, err := NewSummarizeStage(exec, runeCounter{})
	if err != nil {
		t.Fatalf("NewSummarizeStage: %v", err)
	}
	return stage
}

func TestPipelineStageConstructorRefusals(t *testing.T) {
	if _, err := NewPreStage(PreStageClassify, nil); err == nil {
		t.Error("NewPreStage with a nil executor must be refused")
	} else {
		requireKind(t, err, cascade.KindInvalidInput)
	}
	if _, err := NewPreStage(PreStageKind(0), &stageExecutor{}); err == nil {
		t.Error("NewPreStage with an unset kind must be refused, not defaulted")
	} else {
		requireKind(t, err, cascade.KindInvalidInput)
		if !strings.Contains(err.Error(), PreStageKind(0).String()) {
			t.Errorf("the refusal does not name the kind it refused: %v", err)
		}
	}
	if got := PreStageKind(0).String(); got == pipelineTaskClassClassify || got == pipelineTaskClassSegment {
		t.Errorf("PreStageKind(0).String() = %q, want a name no valid kind uses", got)
	}
	if PreStageClassify.String() != pipelineTaskClassClassify || PreStageSegment.String() != pipelineTaskClassSegment {
		t.Error("a pre-stage kind's name must be its own task class")
	}
	if _, err := NewSummarizeStage(nil, runeCounter{}); err == nil {
		t.Error("NewSummarizeStage with a nil executor must be refused")
	}
	if _, err := NewSummarizeStage(&stageExecutor{}, nil); err == nil {
		t.Error("NewSummarizeStage with a nil counter must be refused: it cannot prove 'shorter'")
	} else {
		requireKind(t, err, cascade.KindInvalidInput)
	}
}

// TestPipelineStageTaskClassesPinnedToTaxonomy is the anti-drift pin: each
// stage's literal class equals the taxonomy's own constant, and its
// Requirements row equals that row's Reasoning/CtxK/Structured. Change any
// literal (or a taxonomy row) and this goes red.
func TestPipelineStageTaskClassesPinnedToTaxonomy(t *testing.T) {
	rows := conductor.TaskClasses()
	cases := []struct {
		class      conductor.TaskClass
		literal    string
		reasoning  string
		ctxTokens  int
		structured bool
	}{
		{conductor.TaskClassClassify, pipelineTaskClassClassify, pipelineClassifyReasoning, pipelineClassifyContext, true},
		{conductor.TaskClassSegment, pipelineTaskClassSegment, pipelineSegmentReasoning, pipelineSegmentContext, true},
		{conductor.TaskClassSummarize, pipelineTaskClassSummarize, pipelineSummarizeReasoning, pipelineSummarizeContext, false},
	}
	for _, tc := range cases {
		if tc.literal != string(tc.class) {
			t.Errorf("literal %q != conductor.TaskClass %q", tc.literal, tc.class)
		}
		row, ok := taskClassRow(rows, tc.literal)
		if !ok {
			t.Fatalf("no §5.16 row for %q", tc.literal)
		}
		if row.Reasoning != tc.reasoning || row.CtxK*1000 != tc.ctxTokens || row.Structured != tc.structured {
			t.Errorf("%s: transcribed (%s, %d, %t), row says (%s, %d, %t)",
				tc.literal, tc.reasoning, tc.ctxTokens, tc.structured,
				row.Reasoning, row.CtxK*1000, row.Structured)
		}
	}
}

// taskClassRow finds one §5.16 row by class name.
func taskClassRow(rows []conductor.TaskClassRow, class string) (conductor.TaskClassRow, bool) {
	for _, row := range rows {
		if row.Class == class {
			return row, true
		}
	}
	return conductor.TaskClassRow{}, false
}

// TestPipelineStageEmptyInputDispatchesNothing covers the empty-input guard
// on both halves: no slot with content, and no assembled text.
func TestPipelineStageEmptyInputDispatchesNothing(t *testing.T) {
	cases := []struct {
		name  string
		build func(*stageExecutor) PipelineStage
		in    *StageInput
	}{
		{"classify", func(e *stageExecutor) PipelineStage { return mustPreStage(t, PreStageClassify, e) },
			&StageInput{Slots: []Slot{{Label: "a"}}}},
		{"segment", func(e *stageExecutor) PipelineStage { return mustPreStage(t, PreStageSegment, e) },
			&StageInput{Slots: nil}},
		{"summarize", func(e *stageExecutor) PipelineStage { return mustSummarizeStage(t, e) },
			&StageInput{Text: ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &stageExecutor{}
			err := tc.build(exec).Execute(context.Background(), tc.in)
			if err == nil {
				t.Fatal("empty input must be refused")
			}
			requireKind(t, err, cascade.KindInvalidInput)
			if got := exec.callCount(); got != 0 {
				t.Errorf("executor called %d times on empty input, want 0", got)
			}
		})
	}
	exec := &stageExecutor{}
	if err := mustPreStage(t, PreStageClassify, exec).Execute(context.Background(), nil); err == nil {
		t.Error("a nil StageInput must be refused")
	}
	if got := exec.callCount(); got != 0 {
		t.Errorf("executor called %d times on a nil input, want 0", got)
	}
}

// TestPipelineStageRequestShape asserts every stage's dispatched request:
// its own declared class, no Policy, zero Sensitivity, zero
// RequiredCapabilities, and the slot content actually in the prompt.
func TestPipelineStageRequestShape(t *testing.T) {
	cases := []struct {
		name  string
		class string
		build func(*stageExecutor) PipelineStage
		in    *StageInput
	}{
		{"classify", pipelineTaskClassClassify,
			func(e *stageExecutor) PipelineStage { return mustPreStage(t, PreStageClassify, e) },
			&StageInput{Slots: []Slot{{Label: "a", Content: "MARKER-ALPHA"}}}},
		{"segment", pipelineTaskClassSegment,
			func(e *stageExecutor) PipelineStage { return mustPreStage(t, PreStageSegment, e) },
			&StageInput{Slots: []Slot{{Label: "a", Content: "MARKER-ALPHA"}}}},
		{"summarize", pipelineTaskClassSummarize,
			func(e *stageExecutor) PipelineStage { return mustSummarizeStage(t, e) },
			&StageInput{Text: "MARKER-ALPHA"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &stageExecutor{replies: []string{"x"}}
			stage := tc.build(exec)
			if err := stage.Execute(context.Background(), tc.in); err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if exec.callCount() != 1 {
				t.Fatalf("executor called %d times, want exactly 1", exec.callCount())
			}
			req := exec.requestAt(0)
			if req.TaskClass != tc.class || stage.TaskClass() != tc.class {
				t.Errorf("TaskClass = %q / %q, want %q", req.TaskClass, stage.TaskClass(), tc.class)
			}
			if req.Policy != (provider.Policy{}) {
				t.Errorf("Policy = %+v, want zero value", req.Policy)
			}
			if req.Sensitivity != provider.SensitivityTier(0) {
				t.Errorf("Sensitivity = %v, want zero value", req.Sensitivity)
			}
			if req.RequiredCapabilities != (provider.RequiredCapabilities{}) {
				t.Errorf("RequiredCapabilities = %+v, want zero value", req.RequiredCapabilities)
			}
			if !strings.Contains(req.Inputs[0].Content, "MARKER-ALPHA") {
				t.Error("the dispatched prompt does not carry the content it was asked about")
			}
		})
	}
}

// TestPipelineStageDispatchErrorPreservesKind covers the model.execute
// failure path. errors.Is is deliberately NOT used: (*cascade.Error).Is
// compares KIND only, so it would pass against any KindUnavailable error and
// prove nothing about this error wrapping THAT one.
func TestPipelineStageDispatchErrorPreservesKind(t *testing.T) {
	inner := cascade.New(cascade.KindUnavailable, "MARKER-INNER")
	exec := &stageExecutor{err: inner}
	err := mustPreStage(t, PreStageClassify, exec).Execute(context.Background(),
		&StageInput{Slots: []Slot{{Content: "alpha"}}})
	if err == nil {
		t.Fatal("Execute: want error, got nil")
	}
	requireKind(t, err, cascade.KindUnavailable)
	if !unwrapTo(err, inner) {
		t.Errorf("Execute error does not wrap the executor's own error value: %v", err)
	}
	if !strings.Contains(err.Error(), "MARKER-INNER") {
		t.Errorf("Execute error loses the underlying message: %v", err)
	}
}

// TestPipelineStageDispatchErrorOutsideTaxonomy covers the other branch of
// the dependency wrap: a plain error, which has no Kind to preserve, becomes
// KindInternal.
func TestPipelineStageDispatchErrorOutsideTaxonomy(t *testing.T) {
	inner := errors.New("MARKER-PLAIN")
	exec := &stageExecutor{err: inner}
	err := mustSummarizeStage(t, exec).Execute(context.Background(), &StageInput{Text: "alpha"})
	if err == nil {
		t.Fatal("Execute: want error, got nil")
	}
	requireKind(t, err, cascade.KindInternal)
	if !unwrapTo(err, inner) {
		t.Errorf("Execute error does not wrap the plain error value: %v", err)
	}
}

// unwrapTo reports whether err's unwrap chain contains the exact target
// VALUE. errors.Is is not usable for this assertion: (*cascade.Error).Is
// compares KIND only, so errors.Is(err, someKindUnavailableError) is true
// for every KindUnavailable error in the tree and proves nothing about which
// error this one wraps.
func unwrapTo(err, target error) bool {
	for e := err; e != nil; e = errors.Unwrap(e) {
		if e == target {
			return true
		}
	}
	return false
}
