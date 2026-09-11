package conductor

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/pkg/provider"
)

// waitTimeout bounds every channel/select wait in this file. Per the
// phase's own warning: a bare <-ch in a test turns one wrong code path
// into a package-wide hang, so every wait below is a select against
// time.After(waitTimeout) with a Fatalf naming what was expected.
const waitTimeout = 2 * time.Second

// recordingBridge is a concurrency-safe EventBridge double recording
// every Publish call, for asserting terminal-event counts and payload
// shapes without a real events.Bus.
type recordingBridge struct {
	mu     sync.Mutex
	events []events.EventKind
	bodies []string
}

func (b *recordingBridge) Publish(_ context.Context, _ string, kind events.EventKind, _ string, payload []byte) (events.Event, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, kind)
	b.bodies = append(b.bodies, string(payload))
	return events.Event{Kind: kind, Payload: payload}, nil
}

func (b *recordingBridge) count(kind events.EventKind) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, k := range b.events {
		if k == kind {
			n++
		}
	}
	return n
}

func (b *recordingBridge) terminalTypes(kind events.EventKind) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for i, k := range b.events {
		if k != kind {
			continue
		}
		var p streamEventPayload
		_ = json.Unmarshal([]byte(b.bodies[i]), &p)
		out = append(out, p.Type)
	}
	return out
}

func drainStream(t *testing.T, ch <-chan provider.StreamEvent) []provider.StreamEvent {
	t.Helper()
	var got []provider.StreamEvent
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return got
			}
			got = append(got, ev)
		case <-time.After(waitTimeout):
			t.Fatalf("timed out waiting for stream to close (got %d events so far)", len(got))
		}
	}
}

func TestStream_HappyPath(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	bridge := &recordingBridge{}
	exec.SetEventBridge(bridge)
	deps.prov.streamFn = func(_ context.Context, _ provider.ChatRequest, sink provider.StreamSink) error {
		if err := sink(provider.StreamEvent{Kind: provider.StreamEventDelta, Delta: "hello"}); err != nil {
			return err
		}
		return sink(provider.StreamEvent{Kind: provider.StreamEventDone})
	}

	id, ch, _, err := exec.ExecuteStreamJob(context.Background(), validReq())
	if err != nil {
		t.Fatalf("ExecuteStreamJob: %v", err)
	}
	if id == "" {
		t.Fatal("job id is empty")
	}
	got := drainStream(t, ch)
	if len(got) != 2 {
		t.Fatalf("forwarded %d events, want 2 (delta, done)", len(got))
	}
	if got[0].Kind != provider.StreamEventDelta || got[0].Delta != "hello" {
		t.Fatalf("first event = %+v, want delta %q", got[0], "hello")
	}
	waitFor(t, func() bool { return bridge.count(jobEventKind(id)) >= 2 })
	types := bridge.terminalTypes(jobEventKind(id))
	if len(types) == 0 || types[len(types)-1] != "done" {
		t.Fatalf("last published event type = %v, want final entry \"done\"", types)
	}
	if n := exec.cancels().len(); n != 0 {
		t.Fatalf("cancel registry len after happy-path completion = %d, want 0", n)
	}
}

func TestStream_MidStreamCancel(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	bridge := &recordingBridge{}
	exec.SetEventBridge(bridge)
	block := make(chan struct{})
	deps.prov.streamFn = func(ctx context.Context, _ provider.ChatRequest, sink provider.StreamSink) error {
		if err := sink(provider.StreamEvent{Kind: provider.StreamEventDelta, Delta: "partial"}); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}

	startInflight := exec.cancels().len()
	id, ch, cancel, err := exec.ExecuteStreamJob(context.Background(), validReq())
	if err != nil {
		t.Fatalf("ExecuteStreamJob: %v", err)
	}
	waitFor(t, func() bool { return exec.cancels().len() > startInflight })
	cancel()
	close(block)
	drainStream(t, ch)

	types := bridge.terminalTypes(jobEventKind(id))
	if len(types) == 0 || types[len(types)-1] != "cancelled" {
		t.Fatalf("terminal types = %v, want last entry \"cancelled\"", types)
	}
	if n := exec.cancels().len(); n != startInflight {
		t.Fatalf("cancel registry len after cancel = %d, want back to starting value %d (permit leak)", n, startInflight)
	}
}

func TestStream_CancelAfterCompletion_Noop(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	deps.prov.streamFn = func(_ context.Context, _ provider.ChatRequest, sink provider.StreamSink) error {
		return sink(provider.StreamEvent{Kind: provider.StreamEventDone})
	}
	id, ch, _, err := exec.ExecuteStreamJob(context.Background(), validReq())
	if err != nil {
		t.Fatalf("ExecuteStreamJob: %v", err)
	}
	drainStream(t, ch)
	waitFor(t, func() bool { return exec.cancels().len() == 0 })

	if found := exec.Cancel(id); found {
		t.Fatalf("Cancel on a completed job reported found=true, want false (no-op)")
	}
}

func TestStream_CancelUnknownJobID(t *testing.T) {
	exec, _ := newReadyExecutor(t)
	if found := exec.Cancel(JobID("no-such-job")); found {
		t.Fatalf("Cancel on an unknown job id reported found=true, want false")
	}
}

func TestStream_ClientDisconnect(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	bridge := &recordingBridge{}
	exec.SetEventBridge(bridge)
	deps.prov.streamFn = func(ctx context.Context, _ provider.ChatRequest, sink provider.StreamSink) error {
		if err := sink(provider.StreamEvent{Kind: provider.StreamEventDelta, Delta: "x"}); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}

	callerCtx, disconnect := context.WithCancel(context.Background())
	id, ch, _, err := exec.ExecuteStreamJob(callerCtx, validReq())
	if err != nil {
		t.Fatalf("ExecuteStreamJob: %v", err)
	}
	waitFor(t, func() bool { return exec.cancels().len() > 0 })
	disconnect() // simulates the SSE subscription's ctx cancelling on client disconnect
	drainStream(t, ch)

	types := bridge.terminalTypes(jobEventKind(id))
	if len(types) == 0 || types[len(types)-1] != "cancelled" {
		t.Fatalf("client-disconnect terminal types = %v, want last entry \"cancelled\" (same path as job.cancel)", types)
	}
}

func TestStream_TerminalEventOrdering(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	bridge := &recordingBridge{}
	exec.SetEventBridge(bridge)
	deps.prov.streamFn = func(ctx context.Context, _ provider.ChatRequest, sink provider.StreamSink) error {
		if err := sink(provider.StreamEvent{Kind: provider.StreamEventDelta, Delta: "a"}); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}
	id, ch, cancel, err := exec.ExecuteStreamJob(context.Background(), validReq())
	if err != nil {
		t.Fatalf("ExecuteStreamJob: %v", err)
	}
	waitFor(t, func() bool { return exec.cancels().len() > 0 })
	cancel()
	drainStream(t, ch)

	types := bridge.terminalTypes(jobEventKind(id))
	if len(types) != 1 {
		t.Fatalf("published exactly-terminal events = %v, want exactly one entry", types)
	}
	if types[0] != "cancelled" {
		t.Fatalf("terminal event = %q, want \"cancelled\"", types[0])
	}
}
