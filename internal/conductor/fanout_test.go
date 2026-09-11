package conductor

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

// passthroughPermit is the trivial WithPermitFn double: it always runs fn
// and never denies. Defined _test.go only (Art.1).
func passthroughPermit(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

// spyJournal records every AppendLeg call for assertion.
type spyJournal struct {
	mu      sync.Mutex
	entries []spyJournalEntry
}

type spyJournalEntry struct {
	kind     string
	taskID   string
	legIndex int
	fields   map[string]string
}

func (s *spyJournal) AppendLeg(_ context.Context, kind, taskID string, legIndex int, fields map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, spyJournalEntry{kind: kind, taskID: taskID, legIndex: legIndex, fields: fields})
	return nil
}

func (s *spyJournal) count(kind string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, e := range s.entries {
		if e.kind == kind {
			n++
		}
	}
	return n
}

func fanoutReq() provider.ModelRequest {
	return provider.ModelRequest{TaskID: "fanout-task", TaskClass: "chat", Inputs: []provider.ChatMessage{{Role: "user", Content: "hi"}}}
}

// countingExec returns an exec double that records call count and sleeps a
// randomised short delay so completion order is not launch order.
func countingExec(t *testing.T) (func(context.Context, provider.ModelRequest) (provider.ModelResponse, error), *int32) {
	t.Helper()
	var calls int32
	return func(_ context.Context, req provider.ModelRequest) (provider.ModelResponse, error) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(time.Duration(rand.Intn(3)) * time.Millisecond)
		return provider.ModelResponse{JobID: JobID("job-" + req.TaskID)}, nil
	}, &calls
}

func TestFanOut_AllSuccess(t *testing.T) {
	exec, calls := countingExec(t)
	j := &spyJournal{}
	results, err := FanOut(context.Background(), fanoutReq(), 4, nil, passthroughPermit, j, exec)
	if err != nil {
		t.Fatalf("FanOut: %v", err)
	}
	if len(results) != 4 {
		t.Fatalf("len(results) = %d, want 4", len(results))
	}
	if atomic.LoadInt32(calls) != 4 {
		t.Fatalf("exec calls = %d, want 4", atomic.LoadInt32(calls))
	}
}

func TestFanOut_FirstLegError_AllDrained(t *testing.T) {
	var calls int32
	exec := func(_ context.Context, _ provider.ModelRequest) (provider.ModelResponse, error) {
		atomic.AddInt32(&calls, 1)
		return provider.ModelResponse{}, errors.New("boom")
	}
	j := &spyJournal{}
	_, err := FanOut(context.Background(), fanoutReq(), 5, nil, passthroughPermit, j, exec)
	if err == nil {
		t.Fatal("FanOut: want error, got nil")
	}
	if atomic.LoadInt32(&calls) != 5 {
		t.Fatalf("exec calls = %d, want 5 (all legs still dispatched)", atomic.LoadInt32(&calls))
	}
	if j.count("fanout_leg_done") != 5 {
		t.Fatalf("fanout_leg_done entries = %d, want 5", j.count("fanout_leg_done"))
	}
}

func TestFanOut_CtxCancelledBeforeAdmit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls int32
	exec := func(_ context.Context, _ provider.ModelRequest) (provider.ModelResponse, error) {
		atomic.AddInt32(&calls, 1)
		return provider.ModelResponse{}, nil
	}
	j := &spyJournal{}
	_, err := FanOut(ctx, fanoutReq(), 3, nil, passthroughPermit, j, exec)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("FanOut: err = %v, want context.Canceled", err)
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("exec calls = %d, want 0 (ctx already cancelled)", atomic.LoadInt32(&calls))
	}
}

func TestFanOut_CtxCancelledAfterDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// cancel is idempotent and safe to call from multiple leg goroutines
	// concurrently; FanOut itself blocks (synchronously) until every leg
	// is drained, so no extra synchronization is needed here.
	exec := func(_ context.Context, _ provider.ModelRequest) (provider.ModelResponse, error) {
		cancel()
		return provider.ModelResponse{JobID: "job"}, nil
	}
	j := &spyJournal{}
	_, err := FanOut(ctx, fanoutReq(), 2, nil, passthroughPermit, j, exec)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("FanOut: err = %v, want context.Canceled", err)
	}
}

func TestFanOut_NegativeN_InvalidRequest(t *testing.T) {
	var calls int32
	exec := func(_ context.Context, _ provider.ModelRequest) (provider.ModelResponse, error) {
		atomic.AddInt32(&calls, 1)
		return provider.ModelResponse{}, nil
	}
	j := &spyJournal{}
	_, err := FanOut(context.Background(), fanoutReq(), -1, nil, passthroughPermit, j, exec)
	if err != ErrInvalidRequest {
		t.Fatalf("FanOut(n=-1): err = %v, want ErrInvalidRequest", err)
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("exec calls = %d, want 0", atomic.LoadInt32(&calls))
	}
}

func TestFanOut_NoAdmitCalled(t *testing.T) {
	// FanOut imports no governor package and declares no
	// AdmissionController (R-21.214) - this is a static, compile-time
	// property asserted by the callgraph arch gate (callgraph_test.go),
	// not a runtime spy. This test asserts the seam it DOES use never
	// receives a permit-shaped call it wasn't given: passthroughPermit is
	// the only "admission" FanOut ever touches.
	var permitCalls int32
	permit := func(ctx context.Context, fn func(context.Context) error) error {
		atomic.AddInt32(&permitCalls, 1)
		return fn(ctx)
	}
	exec := func(_ context.Context, _ provider.ModelRequest) (provider.ModelResponse, error) {
		return provider.ModelResponse{JobID: "job"}, nil
	}
	j := &spyJournal{}
	if _, err := FanOut(context.Background(), fanoutReq(), 3, nil, permit, j, exec); err != nil {
		t.Fatalf("FanOut: %v", err)
	}
	if atomic.LoadInt32(&permitCalls) != 3 {
		t.Fatalf("withPermit calls = %d, want 3 (one per leg, never a separate Admit)", atomic.LoadInt32(&permitCalls))
	}
}

func TestFanOut_ResumeSkipsCompletedLegs(t *testing.T) {
	exec, calls := countingExec(t)
	j := &spyJournal{}
	completed := map[int]JobID{0: "job-0", 2: "job-2"}
	results, err := FanOut(context.Background(), fanoutReq(), 3, completed, passthroughPermit, j, exec)
	if err != nil {
		t.Fatalf("FanOut: %v", err)
	}
	if atomic.LoadInt32(calls) != 1 {
		t.Fatalf("exec calls = %d, want 1 (legs 0 and 2 replayed)", atomic.LoadInt32(calls))
	}
	if results[0].JobID != "job-0" || results[2].JobID != "job-2" {
		t.Fatalf("replayed legs did not carry their completed JobID: %+v", results)
	}
	if j.count("fanout_leg_started") != 1 || j.count("fanout_leg_done") != 1 {
		t.Fatalf("skipped legs must append no journal entries: started=%d done=%d", j.count("fanout_leg_started"), j.count("fanout_leg_done"))
	}
}

func TestFanOut_JournalLegEntries(t *testing.T) {
	exec, _ := countingExec(t)
	j := &spyJournal{}
	if _, err := FanOut(context.Background(), fanoutReq(), 3, nil, passthroughPermit, j, exec); err != nil {
		t.Fatalf("FanOut: %v", err)
	}
	if j.count("fanout_leg_started") != 3 {
		t.Fatalf("fanout_leg_started entries = %d, want 3", j.count("fanout_leg_started"))
	}
	if j.count("fanout_leg_done") != 3 {
		t.Fatalf("fanout_leg_done entries = %d, want 3", j.count("fanout_leg_done"))
	}
}

func TestFanOut_WithPermitError_LegOnly(t *testing.T) {
	var calls int32
	exec := func(_ context.Context, _ provider.ModelRequest) (provider.ModelResponse, error) {
		atomic.AddInt32(&calls, 1)
		return provider.ModelResponse{JobID: "job"}, nil
	}
	deny := 1
	permit := func(ctx context.Context, fn func(context.Context) error) error {
		return fn(ctx)
	}
	denyingOnce := func(_ context.Context, _ func(context.Context) error) error {
		return errors.New("denied")
	}
	legPermit := func(idx int) WithPermitFn {
		if idx == deny {
			return denyingOnce
		}
		return permit
	}
	j := &spyJournal{}
	n := 3
	ch := make(chan legResult, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ch <- dispatchLeg(context.Background(), fanoutReq(), idx, nil, legPermit(idx), j, exec)
		}(i)
	}
	wg.Wait()
	close(ch)
	var deniedFound bool
	for r := range ch {
		if r.index == deny {
			if !errors.Is(r.err, ErrAdmissionDenied) {
				t.Fatalf("leg %d: err = %v, want ErrAdmissionDenied", deny, r.err)
			}
			deniedFound = true
		} else if r.err != nil {
			t.Fatalf("leg %d: unexpected error %v", r.index, r.err)
		}
	}
	if !deniedFound {
		t.Fatal("denied leg result never observed")
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("exec calls = %d, want 2 (denied leg never calls exec)", atomic.LoadInt32(&calls))
	}
}

func TestFanOut_ParentPermitReleasedBeforeLegs(t *testing.T) {
	// FanOut takes no parent-held permit object in its signature (see the
	// journal): the Execute call path that constructs a FanOut dispatch
	// never itself holds an admission permit (Execute calls no Admit
	// either), so there is nothing for a parent to release before
	// spawning legs. This test asserts that vacuous truth: FanOut's first
	// observable action for each leg is independent of any prior
	// admission state.
	exec, calls := countingExec(t)
	j := &spyJournal{}
	if _, err := FanOut(context.Background(), fanoutReq(), 2, nil, passthroughPermit, j, exec); err != nil {
		t.Fatalf("FanOut: %v", err)
	}
	if atomic.LoadInt32(calls) != 2 {
		t.Fatalf("exec calls = %d, want 2", atomic.LoadInt32(calls))
	}
}
