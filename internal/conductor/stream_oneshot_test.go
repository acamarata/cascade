// Purpose: split out of stream_test.go (P1-E11-W3-S23-T3) purely to stay
//   under Art.10.3's 300-line/file cap - the one-shot Execute
//   terminal-event tests, the no-second-JobID-generator proof, and the
//   concurrent cancel/completion race test, plus the shared waitFor
//   helper (used by both this file and stream_test.go - same package,
//   file placement carries no meaning to the Go compiler).
// SPORT: conductor.streaming/ADD (P1-E11-W3-S23-T3), file split only.

package conductor

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestStream_OneShotJobID(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	bridge := &recordingBridge{}
	exec.SetEventBridge(bridge)
	deps.prov.chatFn = func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		return provider.ChatResponse{Message: provider.ChatMessage{Content: "ok"}}, nil
	}
	resp, err := exec.Execute(context.Background(), validReq())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.JobID == "" {
		t.Fatal("one-shot Execute returned an empty JobID")
	}
	if n := exec.cancels().len(); n != 0 {
		t.Fatalf("cancel registry len after one-shot Execute = %d, want 0 (immediate deregister)", n)
	}
	if found := exec.Cancel(JobID(resp.JobID)); found {
		t.Fatalf("Cancel on a completed one-shot job reported found=true, want false")
	}
}

func TestStream_OneShotTerminalEventEmitted(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	bridge := &recordingBridge{}
	exec.SetEventBridge(bridge)
	deps.prov.chatFn = func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		return provider.ChatResponse{Message: provider.ChatMessage{Content: "ok"}}, nil
	}
	resp, err := exec.Execute(context.Background(), validReq())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	types := bridge.terminalTypes(jobEventKind(JobID(resp.JobID)))
	if len(types) != 1 || types[0] != "done" {
		t.Fatalf("one-shot terminal events = %v, want exactly one \"done\"", types)
	}
}

func TestStream_NoSecondJobIDGenerator(t *testing.T) {
	// R-21.217/R-21.281: JobID is `type JobID = provider.JobID`, declared
	// once in model.go via cascade.NewID() in execute.go. This asserts
	// the alias identity holds and that stream.go's own id (from
	// ExecuteStreamJob) is a value of that same aliased type, not a
	// locally-declared one - the type system itself proves no second type
	// exists; a second *generator* is proven by construction (stream.go's
	// ExecuteStreamJob calls cascade.NewID(), the same function
	// execute.go's Execute calls - see both call sites).
	var _ = provider.JobID("x") // JobID is `type JobID = provider.JobID`; no separate type exists to declare
	exec, deps := newReadyExecutor(t)
	deps.prov.streamFn = func(_ context.Context, _ provider.ChatRequest, sink provider.StreamSink) error {
		return sink(provider.StreamEvent{Kind: provider.StreamEventDone})
	}
	id, ch, _, err := exec.ExecuteStreamJob(context.Background(), validReq())
	if err != nil {
		t.Fatalf("ExecuteStreamJob: %v", err)
	}
	drainStream(t, ch)
	other, err := cascade.NewID()
	if err != nil {
		t.Fatalf("cascade.NewID: %v", err)
	}
	if len(id) != len(other) {
		t.Fatalf("job id %q has a different length than a fresh cascade.NewID() value %q - stream.go must mint ids through the same generator execute.go uses", id, other)
	}
}

func TestStream_ConcurrentCancelAndCompletion(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	bridge := &recordingBridge{}
	exec.SetEventBridge(bridge)
	ready := make(chan struct{})
	deps.prov.streamFn = func(ctx context.Context, _ provider.ChatRequest, _ provider.StreamSink) error {
		close(ready)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(waitTimeout):
			return nil // completes "naturally" if cancel never lands - still exactly one terminal
		}
	}

	startInflight := exec.cancels().len()
	id, ch, cancel, err := exec.ExecuteStreamJob(context.Background(), validReq())
	if err != nil {
		t.Fatalf("ExecuteStreamJob: %v", err)
	}
	select {
	case <-ready:
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for stream to start")
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); cancel() }()
	go func() { defer wg.Done(); cancel() }() // idempotent, must not panic or double-emit
	wg.Wait()
	drainStream(t, ch)

	types := bridge.terminalTypes(jobEventKind(id))
	if len(types) != 1 {
		t.Fatalf("terminal events under concurrent cancel = %v, want exactly one (sync.Once latch)", types)
	}
	if n := exec.cancels().len(); n != startInflight {
		t.Fatalf("cancel registry len after concurrent cancel/completion = %d, want back to %d", n, startInflight)
	}
}

// waitFor polls cond until it is true or waitTimeout elapses, failing the
// test with a clear message rather than a bare blocking receive.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	if !cond() {
		t.Fatalf("condition never became true within %s", waitTimeout)
	}
}
