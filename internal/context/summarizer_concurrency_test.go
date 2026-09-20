package context

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose: the two concurrency properties of the rolling summarizer that a
//   sequential test cannot show: that the single-flight guard (and not the
//   stored-summary cache) is what collapses concurrent callers of the SAME
//   {entityID, Level, sourceVersion}, and that a caller carrying a NEWER
//   source version is never handed the older version's result.
// Constraints: no test here proves a property by sleeping. Every rendezvous
//   is a channel; the one bounded sleep present is a documented margin
//   against host scheduler starvation, and the assertion it protects fails
//   loudly rather than silently passing without it.
// SPORT: context-engine/summarizer-concurrency-test (ADD, P1-E21-W5-S46-T2).

// barrierStore holds every Get at a gate and announces each arrival, so a
// test can prove that N callers all passed the cache check BEFORE any
// regeneration could have written a record for them to hit.
type barrierStore struct {
	*storetest.MemStore
	arrived chan struct{}
	gate    chan struct{}
}

func (s *barrierStore) Get(ctx context.Context, namespace, key string) ([]byte, error) {
	s.arrived <- struct{}{}
	<-s.gate
	return s.MemStore.Get(ctx, namespace, key)
}

// versionedExecutor answers with an output derived from the source marker in
// the request it received, and holds every call until released, so a test
// can have two different source versions in flight at once.
type versionedExecutor struct {
	mu      sync.Mutex
	calls   int
	entered chan struct{}
	release chan struct{}
}

func (e *versionedExecutor) Execute(_ context.Context, req provider.ModelRequest) (provider.ModelResponse, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	e.entered <- struct{}{}
	<-e.release
	if strings.Contains(req.Inputs[0].Content, "MARK-V2") {
		return provider.ModelResponse{Output: "summary-of-v2"}, nil
	}
	return provider.ModelResponse{Output: "summary-of-v1"}, nil
}

func (e *versionedExecutor) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// awaitSignal receives one value from ch or fails the test.
func awaitSignal(t *testing.T, ch chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// TestSummarizerNoDuplicateConcurrentRegen drives N concurrent GetSummary
// calls for the SAME {entityID, Level, sourceVersion} and asserts the guard
// collapses them into exactly one model.execute call.
//
// WHY THE BARRIER STORE. callCount()==1 alone does not prove a guard: a
// caller that arrives after the winner has already stored its record takes
// the CACHE path and dispatches nothing, so a summarizer with no guard at
// all can produce the same 1. The barrier makes that reading impossible --
// all N callers are held inside store.Get and released together, and the
// winner's Put cannot happen until release is closed, which is strictly
// later. So all N provably missed the cache, and the single dispatch is the
// guard's work.
func TestSummarizerNoDuplicateConcurrentRegen(t *testing.T) {
	const n = 5
	exec := &fakeExecutor{output: "shared summary", entered: make(chan struct{}, n), release: make(chan struct{})}
	store := &barrierStore{
		MemStore: storetest.NewMemStore(),
		arrived:  make(chan struct{}, n),
		gate:     make(chan struct{}),
	}
	s := newTestSummarizer(t, exec, store)

	outcomes := make([]SummaryOutcome, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			o, err := s.GetSummary(context.Background(), "entity-race", GranularityEpoch, "content", "v1")
			if err != nil {
				t.Errorf("goroutine %d: GetSummary error: %v", i, err)
			}
			outcomes[i] = o
		}(i)
	}

	for i := 0; i < n; i++ {
		awaitSignal(t, store.arrived, "caller to reach the stored-summary read")
	}
	close(store.gate)
	awaitSignal(t, exec.entered, "the single in-flight regeneration to start")
	// Margin, not proof: the winner is provably inside Execute, but the
	// other callers are between a returned Get and their registration on the
	// guard. If one is starved past the winner's completion it becomes a
	// second winner and the assertion below FAILS -- this margin makes that
	// flake unlikely, it does not make the assertion true.
	time.Sleep(250 * time.Millisecond)
	close(exec.release)
	wg.Wait()

	if got := exec.callCount(); got != 1 {
		t.Fatalf("Execute called %d times for %d cache-missing concurrent callers of the SAME key, want exactly 1", got, n)
	}
	for i, o := range outcomes {
		if o.Record.Content != "shared summary" {
			t.Errorf("goroutine %d: Record.Content = %q, want the single shared result %q", i, o.Record.Content, "shared summary")
		}
	}
}

// TestSummarizerSingleFlightKeyedBySourceVersion drives two concurrent
// callers of the same {entityID, Level} carrying DIFFERENT source versions
// and asserts each gets the summary of its own version.
//
// The guard key includes sourceVersion, so both must dispatch: the second
// `entered` signal is what proves it. A guard keyed on {entityID, Level}
// alone would collapse them, the second signal would never arrive, and this
// test would fail on the timeout rather than on the content.
func TestSummarizerSingleFlightKeyedBySourceVersion(t *testing.T) {
	exec := &versionedExecutor{entered: make(chan struct{}, 2), release: make(chan struct{})}
	s := newTestSummarizer(t, exec, storetest.NewMemStore())

	type result struct {
		outcome SummaryOutcome
		err     error
	}
	results := make([]result, 2)
	sources := []struct{ marker, version string }{
		{"MARK-V1 the original content", "v1"},
		{"MARK-V2 an entirely different content", "v2"},
	}
	var wg sync.WaitGroup
	for i, src := range sources {
		wg.Add(1)
		go func(i int, marker, version string) {
			defer wg.Done()
			o, err := s.GetSummary(context.Background(), "entity-versions", GranularityThread, marker, version)
			results[i] = result{o, err}
		}(i, src.marker, src.version)
	}

	awaitSignal(t, exec.entered, "the first version's regeneration to start")
	awaitSignal(t, exec.entered, "the SECOND version's regeneration to start (the guard key must carry sourceVersion)")
	close(exec.release)
	wg.Wait()

	if got := exec.callCount(); got != 2 {
		t.Fatalf("Execute called %d times for two DIFFERENT source versions, want 2", got)
	}
	assertVersionedResult(t, "v1", results[0].outcome, results[0].err, "summary-of-v1")
	assertVersionedResult(t, "v2", results[1].outcome, results[1].err, "summary-of-v2")
}

// assertVersionedResult asserts one caller of the version-keyed race got its
// own version's record, not the other caller's.
func assertVersionedResult(t *testing.T, version string, outcome SummaryOutcome, err error, wantContent string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s caller: GetSummary error: %v", version, err)
	}
	if outcome.Record.SourceVersion != version {
		t.Errorf("%s caller: Record.SourceVersion = %q, want %q", version, outcome.Record.SourceVersion, version)
	}
	if outcome.Record.Content != wantContent {
		t.Errorf("%s caller: Record.Content = %q, want %q", version, outcome.Record.Content, wantContent)
	}
}
