package context

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose: the measurement legs. Every window, size bound and substitute in
//   this engine is measured in TOKENS by the injected provider.TokenCounter,
//   so a counter that fails is a real operating condition: the engine must
//   refuse rather than truncate to nothing, dispatch an unmeasured window, or
//   store an unchecked response.
// SPORT: context-engine/summarizer-measurement-test (ADD, P1-E21-W5-S46-T2).

// failingCounter is a provider.TokenCounter that always fails, for the
// fail-closed legs: a summarizer that cannot MEASURE must refuse, never
// truncate to nothing or serve an unmeasured block.
type failingCounter struct{}

func (failingCounter) Count(_ context.Context, _ string) (int, error) {
	return 0, cascade.New(cascade.KindUnavailable, "tokenizer service unreachable")
}

// newFailingCounterSummarizer builds a Summarizer whose only broken
// dependency is its TokenCounter.
func newFailingCounterSummarizer(t *testing.T, exec provider.ModelExecutor, store provider.Store) *Summarizer {
	t.Helper()
	s, err := NewSummarizer(exec, store, failingCounter{}, testkit.NewFrozenClock(time.Unix(1000, 0)), nil)
	if err != nil {
		t.Fatalf("NewSummarizer: %v", err)
	}
	return s
}

// TestSummarizerCounterFailureRefusesRatherThanGuesses asserts a broken
// TokenCounter fails the regeneration (reported as a failure event, never a
// silently empty window) and fails Summarize outright.
func TestSummarizerCounterFailureRefusesRatherThanGuesses(t *testing.T) {
	exec := &fakeExecutor{output: "a summary"}
	s := newFailingCounterSummarizer(t, exec, storetest.NewMemStore())

	outcome, err := s.GetSummary(context.Background(), "e", GranularityThread, "content", "v1")
	if err != nil {
		t.Fatalf("GetSummary: want the degrade-gracefully path, got error %v", err)
	}
	if outcome.Failed == nil {
		t.Fatal("outcome.Failed = nil, want a failure event when the counter cannot measure the window")
	}
	if !strings.Contains(outcome.Failed.Reason, "measuring") {
		t.Errorf("Failed.Reason = %q, want it to name the measurement that failed", outcome.Failed.Reason)
	}
	if exec.callCount() != 0 {
		t.Errorf("Execute called %d times with an unmeasurable window, want 0", exec.callCount())
	}

	if _, err := s.Summarize(context.Background(), Slot{Label: "e", Content: "c"}, 1000); err == nil {
		t.Fatal("Summarize: want an error when the counter cannot measure")
	}
}

// TestSummarizerResponseMeasurementFailure covers the other counter leg: the
// window measured fine (the counter breaks only afterwards), so the response
// arrives and cannot be checked against the size bound. It must not be
// stored on trust.
func TestSummarizerResponseMeasurementFailure(t *testing.T) {
	exec := &fakeExecutor{output: "a summary"}
	store := storetest.NewMemStore()
	s := newTestSummarizer(t, exec, store)
	s.counter = &countThenFail{limit: 1}

	outcome, err := s.GetSummary(context.Background(), "e", GranularityThread, "content", "v1")
	if err != nil {
		t.Fatalf("GetSummary: want the degrade-gracefully path, got error %v", err)
	}
	if outcome.Failed == nil {
		t.Fatal("outcome.Failed = nil, want a failure event when the response cannot be measured")
	}
	if !strings.Contains(outcome.Failed.Reason, "model response") {
		t.Errorf("Failed.Reason = %q, want it to name the response measurement", outcome.Failed.Reason)
	}
	if _, found := storedRecord(t, store, "e", GranularityThread); found {
		t.Error("an unmeasured response was persisted; it must never reach the store")
	}
}

// countThenFail answers normally for its first limit calls and fails after,
// so a test can break exactly one measurement in a sequence.
type countThenFail struct {
	mu    sync.Mutex
	calls int
	limit int
}

func (c *countThenFail) Count(ctx context.Context, text string) (int, error) {
	c.mu.Lock()
	c.calls++
	over := c.calls > c.limit
	c.mu.Unlock()
	if over {
		return 0, cascade.New(cascade.KindUnavailable, "tokenizer service unreachable")
	}
	return provider.NaiveTokenCounter{}.Count(ctx, text)
}
