package conductor

// Purpose (this file): a stream is retried on a transient failure only
//   while nothing reached the caller, and always ends in one terminal event.
// SPORT: conductor.transient-retry (TEST) - P1-GWY-03.

import (
	"context"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func streamRetryRig(t *testing.T) (*Executor, *readyDeps, map[string]*fakeProvider, *sleepRecorder) {
	t.Helper()
	exec, deps, _, provs := failoverRig(t)
	rec := &sleepRecorder{clock: &retryClock{now: time.Now()}}
	exec.SetRetry(DefaultRetryPolicy(), rec.clock, rec.sleep, rand.New(rand.NewPCG(1, 2)), nil)
	return exec, deps, provs, rec
}

func streamTransient() error {
	return provider.MarkTransient(cascade.New(cascade.KindUnavailable, "upstream: stream error api_error"),
		provider.TransientInfo{Cause: provider.TransientStreamAPIError})
}

// terminals counts done and error events.
func terminals(evs []provider.StreamEvent) (done, errs int) {
	for _, ev := range evs {
		if ev.Kind == provider.StreamEventDone {
			done++
		}
		if ev.Kind == provider.StreamEventError {
			errs++
		}
	}
	return done, errs
}

func TestStreamRetriesOnlyBeforeFirstDelta(t *testing.T) {
	exec, deps, provs, rec := streamRetryRig(t)
	calls := 0
	provs["lane-1"].streamFn = func(_ context.Context, _ provider.ChatRequest, sink provider.StreamSink) error {
		calls++
		if calls == 1 {
			err := streamTransient()
			_ = sink(provider.StreamEvent{Kind: provider.StreamEventError, Err: err})
			return err
		}
		_ = sink(provider.StreamEvent{Kind: provider.StreamEventDelta, Delta: "hi"})
		return sink(provider.StreamEvent{Kind: provider.StreamEventDone, Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}})
	}
	ch, _, err := exec.ExecuteStream(context.Background(), validReq())
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	evs := collect(ch)
	done, errs := terminals(evs)
	if calls != 2 || len(rec.delays) != 1 || deps.router.calls != 1 || done != 1 || errs != 0 || evs[0].Delta != "hi" {
		t.Fatalf("calls %d, pauses %v, Select %d, events %+v; want a retry on lane-1 and one done", calls, rec.delays, deps.router.calls, evs)
	}
	if tr := lastTrace(t, deps); tr.Attempts != 2 || !tr.Transient || tr.Outcome != "success" {
		t.Fatalf("audit = %+v, want success with attempts 2 transient true", tr)
	}

	exec, deps, provs, rec = streamRetryRig(t)
	calls = 0
	var sent error
	provs["lane-1"].streamFn = func(_ context.Context, _ provider.ChatRequest, sink provider.StreamSink) error {
		calls++
		_ = sink(provider.StreamEvent{Kind: provider.StreamEventDelta, Delta: "partial"})
		sent = streamTransient()
		_ = sink(provider.StreamEvent{Kind: provider.StreamEventError, Err: sent})
		return sent
	}
	ch, _, err = exec.ExecuteStream(context.Background(), validReq())
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	evs = collect(ch)
	done, errs = terminals(evs)
	if calls != 1 || len(rec.delays) != 0 || deps.router.calls != 1 || done != 0 || errs != 1 {
		t.Fatalf("calls %d, pauses %v, Select %d, events %+v; want no retry after a delta and one error event", calls, rec.delays, deps.router.calls, evs)
	}
	if last := evs[len(evs)-1]; last.Err != sent || evs[0].Delta != "partial" {
		t.Fatalf("events = %+v, want the delta then the driver's error forwarded once", evs)
	}
}
