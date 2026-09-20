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

// fakeExecutor is a real, in-memory provider.ModelExecutor test double
// (Art.1 -- lives under _test.go, never a shipped mock): it records every
// request it received and returns a configurable output or error. entered
// and release, when set, let a test rendezvous with an in-flight Execute
// call without sleeping (Art.7.3).
type fakeExecutor struct {
	mu       sync.Mutex
	calls    int
	requests []provider.ModelRequest
	output   string
	err      error
	entered  chan struct{}
	release  chan struct{}
}

func (f *fakeExecutor) Execute(_ context.Context, req provider.ModelRequest) (provider.ModelResponse, error) {
	f.mu.Lock()
	f.calls++
	f.requests = append(f.requests, req)
	out, err := f.output, f.err
	f.mu.Unlock()
	if f.entered != nil {
		f.entered <- struct{}{}
	}
	if f.release != nil {
		<-f.release
	}
	if err != nil {
		return provider.ModelResponse{}, err
	}
	return provider.ModelResponse{Output: out}, nil
}

func (f *fakeExecutor) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeExecutor) requestAt(i int) provider.ModelRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[i]
}

// flakyStore wraps a real storetest.MemStore and injects a configurable
// Get/Put failure, for exercising loadRecord/saveRecord's own error paths
// without a shipped mock (Art.1 -- MemStore is the real implementation;
// only the injected failure is test-only).
type flakyStore struct {
	*storetest.MemStore
	getErr error
	putErr error
}

func (s *flakyStore) Get(ctx context.Context, namespace, key string) ([]byte, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.MemStore.Get(ctx, namespace, key)
}

func (s *flakyStore) Put(ctx context.Context, namespace, key string, value []byte) error {
	if s.putErr != nil {
		return s.putErr
	}
	return s.MemStore.Put(ctx, namespace, key, value)
}

// recordingPublisher is a SummaryEventPublisher double that keeps every
// event, so a test can assert what an operator would actually see.
type recordingPublisher struct {
	mu     sync.Mutex
	events []SummaryEvent
}

func (p *recordingPublisher) Publish(_ context.Context, event SummaryEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, event)
}

func (p *recordingPublisher) all() []SummaryEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]SummaryEvent(nil), p.events...)
}

func newTestSummarizer(t *testing.T, exec provider.ModelExecutor, store provider.Store) *Summarizer {
	t.Helper()
	return newTestSummarizerWithEvents(t, exec, store, nil)
}

func newTestSummarizerWithEvents(t *testing.T, exec provider.ModelExecutor, store provider.Store, pub SummaryEventPublisher) *Summarizer {
	t.Helper()
	s, err := NewSummarizer(exec, store, provider.NaiveTokenCounter{}, testkit.NewFrozenClock(time.Unix(1000, 0)), pub)
	if err != nil {
		t.Fatalf("NewSummarizer: unexpected error: %v", err)
	}
	return s
}

// TestSummarizerNewValidation asserts all four dependencies are required
// and that a nil event publisher is a supported configuration.
func TestSummarizerNewValidation(t *testing.T) {
	exec := &fakeExecutor{}
	store := storetest.NewMemStore()
	counter := provider.NaiveTokenCounter{}
	clock := testkit.NewFrozenClock(time.Unix(0, 0))

	cases := []struct {
		name             string
		e                provider.ModelExecutor
		st               provider.Store
		co               provider.TokenCounter
		c                Clock
		wantMsgSubstring string
	}{
		{"nil executor", nil, store, counter, clock, "ModelExecutor"},
		{"nil store", exec, nil, counter, clock, "Store"},
		{"nil counter", exec, store, nil, clock, "TokenCounter"},
		{"nil clock", exec, store, counter, nil, "Clock"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := NewSummarizer(tc.e, tc.st, tc.co, tc.c, nil)
			if err == nil {
				t.Fatalf("NewSummarizer(%s): want error, got nil", tc.name)
			}
			if s != nil {
				t.Errorf("NewSummarizer(%s): want nil Summarizer on error", tc.name)
			}
			if k, ok := cascade.KindOf(err); !ok || k != cascade.KindInvalidInput {
				t.Errorf("NewSummarizer(%s) error kind = %v (ok=%v), want invalid-input", tc.name, k, ok)
			}
			if !strings.Contains(err.Error(), tc.wantMsgSubstring) {
				t.Errorf("NewSummarizer(%s) error = %q, want substring %q", tc.name, err.Error(), tc.wantMsgSubstring)
			}
		})
	}

	if s, err := NewSummarizer(exec, store, counter, clock, nil); err != nil || s == nil {
		t.Fatalf("NewSummarizer(valid deps, nil publisher) = (%v, %v), want a non-nil Summarizer and nil error", s, err)
	}
}

// TestSummarizerGetSummaryFreshRegeneration: no stored record -> regenerate
// dispatches exactly one model.execute call and persists the result.
func TestSummarizerGetSummaryFreshRegeneration(t *testing.T) {
	exec := &fakeExecutor{output: "the summary"}
	store := storetest.NewMemStore()
	s := newTestSummarizer(t, exec, store)

	outcome, err := s.GetSummary(context.Background(), "entity-1", GranularityThread, "source content", "v1")
	if err != nil {
		t.Fatalf("GetSummary: unexpected error: %v", err)
	}
	if outcome.Record.Content != "the summary" {
		t.Errorf("Record.Content = %q, want %q", outcome.Record.Content, "the summary")
	}
	if outcome.Record.SourceVersion != "v1" {
		t.Errorf("Record.SourceVersion = %q, want %q", outcome.Record.SourceVersion, "v1")
	}
	if outcome.Stale || outcome.Failed != nil || outcome.Warning != nil {
		t.Errorf("outcome = %+v, want a clean fresh result", outcome)
	}
	if got := exec.callCount(); got != 1 {
		t.Fatalf("Execute called %d times, want 1", got)
	}
	if exec.requestAt(0).TaskClass != "summarize" {
		t.Errorf("request TaskClass = %q, want %q", exec.requestAt(0).TaskClass, "summarize")
	}
}

// TestSummarizerGetSummaryCacheHit: a matching SourceVersion never
// re-dispatches.
func TestSummarizerGetSummaryCacheHit(t *testing.T) {
	exec := &fakeExecutor{output: "the summary"}
	store := storetest.NewMemStore()
	s := newTestSummarizer(t, exec, store)
	ctx := context.Background()

	if _, err := s.GetSummary(ctx, "entity-1", GranularityEpoch, "content", "v1"); err != nil {
		t.Fatalf("first GetSummary: %v", err)
	}
	outcome, err := s.GetSummary(ctx, "entity-1", GranularityEpoch, "content", "v1")
	if err != nil {
		t.Fatalf("second GetSummary: %v", err)
	}
	if got := exec.callCount(); got != 1 {
		t.Fatalf("Execute called %d times across two matching-version calls, want 1 (cache hit)", got)
	}
	if outcome.Record.Content != "the summary" {
		t.Errorf("Record.Content = %q, want cached %q", outcome.Record.Content, "the summary")
	}
}

// TestSummarizerGetSummaryStaleTriggersRegeneration: a changed
// SourceVersion re-dispatches and overwrites the stored record.
func TestSummarizerGetSummaryStaleTriggersRegeneration(t *testing.T) {
	exec := &fakeExecutor{output: "summary one"}
	store := storetest.NewMemStore()
	s := newTestSummarizer(t, exec, store)
	ctx := context.Background()

	if _, err := s.GetSummary(ctx, "entity-1", GranularityTurnWindow, "content v1", "v1"); err != nil {
		t.Fatalf("first GetSummary: %v", err)
	}
	exec.output = "summary two"
	outcome, err := s.GetSummary(ctx, "entity-1", GranularityTurnWindow, "content v2", "v2")
	if err != nil {
		t.Fatalf("second GetSummary: %v", err)
	}
	if got := exec.callCount(); got != 2 {
		t.Fatalf("Execute called %d times across two DIFFERENT-version calls, want 2 (stale regenerates)", got)
	}
	if outcome.Record.Content != "summary two" || outcome.Record.SourceVersion != "v2" {
		t.Errorf("outcome.Record = %+v, want the freshly regenerated v2 record", outcome.Record)
	}
}

// TestSummarizerGetSummaryInputValidation covers the three caller-misuse
// error paths: nil ctx, empty entityID, out-of-range Granularity. Each is
// distinguished by message, never by errors.Is alone (*cascade.Error.Is
// compares Kind only, and all three share KindInvalidInput).
func TestSummarizerGetSummaryInputValidation(t *testing.T) {
	exec := &fakeExecutor{output: "x"}
	store := storetest.NewMemStore()
	s := newTestSummarizer(t, exec, store)

	//nolint:staticcheck // SA1012: a nil ctx is exactly the misuse this test asserts is refused.
	if _, err := s.GetSummary(nil, "e", GranularityThread, "c", "v"); err == nil || !strings.Contains(err.Error(), "ctx") {
		t.Errorf("nil ctx: err = %v, want an error mentioning ctx", err)
	}
	if _, err := s.GetSummary(context.Background(), "", GranularityThread, "c", "v"); err == nil || !strings.Contains(err.Error(), "entityID") {
		t.Errorf("empty entityID: err = %v, want an error mentioning entityID", err)
	}
	if _, err := s.GetSummary(context.Background(), "e", Granularity(9), "c", "v"); err == nil || !strings.Contains(err.Error(), "granularity") {
		t.Errorf("invalid granularity: err = %v, want an error mentioning granularity", err)
	}
	if got := exec.callCount(); got != 0 {
		t.Errorf("Execute called %d times on caller-misuse inputs, want 0", got)
	}
}
