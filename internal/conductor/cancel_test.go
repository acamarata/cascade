package conductor

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/provider"
)

// dispatchCancel drives registry.Dispatch exactly the way a real POST
// /rpc call would - the same entry point RegisterHandlers wires
// job.cancel onto - proving the wiring by using the registry's own public
// surface rather than calling Executor.Cancel directly.
func dispatchCancel(t *testing.T, registry *rpc.Registry, jobID string) (jobCancelResult, *rpc.ErrorObject) {
	t.Helper()
	params, err := json.Marshal(jobCancelParams{JobID: jobID})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	req := &rpc.Request{Method: JobCancelMethod, Params: params}
	result, rpcErr := registry.Dispatch(context.Background(), req)
	if rpcErr != nil {
		return jobCancelResult{}, rpcErr
	}
	out, ok := result.(jobCancelResult)
	if !ok {
		t.Fatalf("job.cancel result type = %T, want jobCancelResult", result)
	}
	return out, nil
}

func TestCancel_RegisterHandlers_KnownJob(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	block := make(chan struct{})
	deps.prov.streamFn = func(ctx context.Context, _ provider.ChatRequest, _ provider.StreamSink) error {
		close(block)
		<-ctx.Done()
		return ctx.Err()
	}

	registry := rpc.NewRegistry()
	RegisterHandlers(registry, exec)

	id, ch, _, err := exec.ExecuteStreamJob(context.Background(), validReq())
	if err != nil {
		t.Fatalf("ExecuteStreamJob: %v", err)
	}
	select {
	case <-block:
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the stream to start")
	}

	got, rpcErr := dispatchCancel(t, registry, string(id))
	if rpcErr != nil {
		t.Fatalf("job.cancel dispatch error: %v", rpcErr)
	}
	if !got.Cancelled {
		t.Fatalf("job.cancel on a known in-flight job returned Cancelled=false, want true")
	}
	drainStream(t, ch)
}

func TestCancel_RegisterHandlers_UnknownJob(t *testing.T) {
	exec, _ := newReadyExecutor(t)
	registry := rpc.NewRegistry()
	RegisterHandlers(registry, exec)

	got, rpcErr := dispatchCancel(t, registry, "no-such-job")
	if rpcErr != nil {
		t.Fatalf("job.cancel on an unknown job id returned a JSON-RPC error %v, want success (idempotent no-op)", rpcErr)
	}
	if got.Cancelled {
		t.Fatalf("job.cancel on an unknown job id returned Cancelled=true, want false")
	}
}

func TestCancel_RegisterHandlers_TwiceIsIdempotent(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	block := make(chan struct{})
	deps.prov.streamFn = func(ctx context.Context, _ provider.ChatRequest, _ provider.StreamSink) error {
		close(block)
		<-ctx.Done()
		return ctx.Err()
	}
	registry := rpc.NewRegistry()
	RegisterHandlers(registry, exec)

	id, ch, _, err := exec.ExecuteStreamJob(context.Background(), validReq())
	if err != nil {
		t.Fatalf("ExecuteStreamJob: %v", err)
	}
	select {
	case <-block:
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the stream to start")
	}

	first, rpcErr := dispatchCancel(t, registry, string(id))
	if rpcErr != nil {
		t.Fatalf("first cancel: %v", rpcErr)
	}
	if !first.Cancelled {
		t.Fatal("first cancel on a known in-flight job should report Cancelled=true")
	}
	drainStream(t, ch)

	second, rpcErr := dispatchCancel(t, registry, string(id))
	if rpcErr != nil {
		t.Fatalf("second cancel on an already-cancelled job returned an error, want idempotent success: %v", rpcErr)
	}
	if second.Cancelled {
		t.Fatalf("second cancel on an already-completed job reported Cancelled=true, want false (it is no longer in flight)")
	}
}

func TestCancel_MalformedParamsRefused(t *testing.T) {
	exec, _ := newReadyExecutor(t)
	registry := rpc.NewRegistry()
	RegisterHandlers(registry, exec)

	req := &rpc.Request{Method: JobCancelMethod, Params: json.RawMessage(`{"job_id":`)}
	_, rpcErr := registry.Dispatch(context.Background(), req)
	if rpcErr == nil {
		t.Fatal("job.cancel with malformed params succeeded, want a refusal")
	}
}

func TestCancel_EmptyJobIDRefused(t *testing.T) {
	exec, _ := newReadyExecutor(t)
	registry := rpc.NewRegistry()
	RegisterHandlers(registry, exec)

	_, rpcErr := dispatchCancel(t, registry, "")
	if rpcErr == nil {
		t.Fatal("job.cancel with an empty job_id succeeded, want a refusal")
	}
}

// TestCancel_WiringIsLoadBearing proves RegisterHandlers is the real
// production caller, not decoration: with it never called, the exact same
// Dispatch entry point a live client would use fails method-not-found -
// this is the "prove the test can fail by removing the wiring" evidence.
func TestCancel_WiringIsLoadBearing(t *testing.T) {
	registry := rpc.NewRegistry() // RegisterHandlers deliberately NOT called
	_, rpcErr := dispatchCancel(t, registry, "any-job")
	if rpcErr == nil {
		t.Fatal("job.cancel dispatched successfully with no handler registered - the wiring proof is broken")
	}
	if registry.Registered(JobCancelMethod) {
		t.Fatal("registry reports job.cancel registered without RegisterHandlers ever being called")
	}
}
