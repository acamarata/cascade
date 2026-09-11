// Purpose: the streaming half of the model.execute door (P1-E11-W3-S23-T3,
//   §D-21): a cancel-map lifecycle keyed by the EXISTING JobID (S-22.T1's
//   model.go declares it once, R-21.217/R-21.281 - this file declares no
//   second JobID type and no second generator), an EventBridge seam that
//   *events.Bus (D/S-06.T4) satisfies structurally, and ExecuteStreamJob:
//   the production entry point that mints a job_id BEFORE streaming
//   begins, forwards provider delta events onto the bridge tagged with
//   that job_id, and publishes exactly one terminal event (done | error |
//   cancelled) through a sync.Once latch before deregistering.
// Inputs: a provider.ModelRequest (ExecuteStreamJob); a JobID (cancel.go's
//   job.cancel calls into the cancel registry this file owns).
// Outputs: a JobID, a <-chan provider.StreamEvent, a CancelFunc, or an
//   error - and, side-channel, typed SSE events on the injected
//   EventBridge.
// Constraints: the terminal event is ALWAYS emitted by the forwarding
//   goroutine here, never by cancel.go's RPC handler (R-21.217's ordering
//   invariant: the terminal event must be the last SSE event for a
//   job_id). Deregistration happens only inside that same sync.Once, after
//   the terminal event is published. No credential or model-output text
//   is ever placed in a terminal event's payload (only a {"type": kind}
//   tag); delta content is the wire's designed payload, not a log line.
// SPORT: conductor.streaming/ADD (P1-E11-W3-S23-T3).

package conductor

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// EventBridge is the injected SSE-publish seam (D/S-06.T4): *events.Bus
// satisfies this interface with its existing Publish method (structural
// typing, no adapter needed). Tests substitute a recording double so the
// default unit lane never opens a real socket; stream_integration_test.go
// (build tag integration) wires a real *events.Bus and a real
// internal/rpc.SSEHandler over a real unix socket.
type EventBridge interface {
	Publish(ctx context.Context, namespace string, kind events.EventKind, source string, payload []byte) (events.Event, error)
}

// conductorEventNamespace is the single events.Bus namespace every job's
// SSE events publish into. conductorEventSource identifies this package as
// the publisher in the persisted Event.Source field.
const (
	conductorEventNamespace = "conductor"
	conductorEventSource    = "conductor"
)

// jobEventKind is the per-job EventKind a GET /events?filter=job:<id>
// subscription matches exactly: internal/rpc/sse.go's filterSet compares
// kinds by exact string equality, so tagging every one of a job's events
// with this same kind - and no other job's events with it - is what
// prevents event bleed between concurrent subscriptions (R-40.X9,
// TestStreamRealSSE).
func jobEventKind(id JobID) events.EventKind {
	return events.EventKind("job:" + string(id))
}

// streamEventPayload is the SSE data payload for a delta or terminal
// event. Type is always set; Delta carries text for a delta event only.
// No error text or other free-form content is ever placed here - a
// terminal event carries only its Type tag, since a stream's own content
// is delivered exclusively through Delta events, never re-described in a
// terminal one (the redaction discipline: nothing derived from model
// output reaches anywhere but the wire payload it was always destined
// for).
type streamEventPayload struct {
	Type  string `json:"type"`
	Delta string `json:"delta,omitempty"`
}

// cancelRegistry is the map[JobID]context.CancelFunc lifecycle R-21.217
// describes: register on start, deregister on any terminal outcome.
// Len reports the number of currently in-flight registrations - the
// balance stream_test.go proves returns to its starting value after every
// cancel (register/deregister is this ticket's admission-permit
// analogue: fanout.go's WithPermitFn governs a different, unrelated
// resource this ticket's own contract never mentions using).
type cancelRegistry struct {
	mu    sync.Mutex
	funcs map[JobID]context.CancelFunc
}

func newCancelRegistry() *cancelRegistry {
	return &cancelRegistry{funcs: make(map[JobID]context.CancelFunc)}
}

// register binds id to cancel. Registering an id already present replaces
// its cancel func (never expected in production - job ids are minted
// fresh per call - but this keeps register total rather than panicking).
func (r *cancelRegistry) register(id JobID, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.funcs[id] = cancel
}

// deregister removes id unconditionally. Removing an absent id is a
// no-op: this is the TOCTOU guard for the map only, never the terminal-
// event serializer (that is the sync.Once latch in forwardStream/Execute).
func (r *cancelRegistry) deregister(id JobID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.funcs, id)
}

// cancel looks up id and, if present, calls its cancel func. It reports
// whether id was found: true for a known in-flight job (job.cancel's
// success-with-effect path), false for a completed or unknown job
// (job.cancel's success-as-no-op path per §D-21's idempotency rule).
// cancel never itself deregisters id - deregistration happens only from
// the terminal-event sync.Once, so a cancel racing the job's own natural
// completion can never leave the map in an inconsistent state.
func (r *cancelRegistry) cancel(id JobID) bool {
	r.mu.Lock()
	fn, ok := r.funcs[id]
	r.mu.Unlock()
	if ok {
		fn()
	}
	return ok
}

// len reports the current registration count.
func (r *cancelRegistry) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.funcs)
}

// cancels lazily constructs e's cancelRegistry exactly once. This is a
// getter rather than a NewExecutor parameter because pipeline.go's
// ExecutorConfig and NewExecutor are outside this ticket's files_scope
// (see the journal for both sides quoted); every existing production and
// test construction path through NewExecutor keeps working unchanged, and
// the registry comes into being on first use.
func (e *Executor) cancels() *cancelRegistry {
	e.cancelsOnce.Do(func() { e.cancelsRegistry = newCancelRegistry() })
	return e.cancelsRegistry
}

// SetEventBridge installs b as e's SSE publish target. A nil bridge (the
// default - see the same files_scope note above) means every publish is a
// silent no-op: streaming and cancellation both still work with no
// bridge installed, they simply have no SSE audience. Returns e so
// composition-root code (and stream_integration_test.go) can chain it
// onto NewExecutor's result.
func (e *Executor) SetEventBridge(b EventBridge) *Executor {
	e.bridge = b
	return e
}

// publishTerminal publishes id's single terminal event (kind is "done",
// "error", or "cancelled") through the bridge, if one is installed. Uses
// a cancellation-independent context so a terminal publish for a
// cancelled job is never itself skipped because the job's own ctx is
// already Done.
func (e *Executor) publishTerminal(ctx context.Context, id JobID, kind string) {
	if e.bridge == nil {
		return
	}
	payload, _ := json.Marshal(streamEventPayload{Type: kind})
	_, _ = e.bridge.Publish(context.WithoutCancel(ctx), conductorEventNamespace, jobEventKind(id), conductorEventSource, payload)
}

// publishDelta publishes one non-terminal provider.StreamEvent as a typed
// SSE event tagged with id, if a bridge is installed. Only Delta events
// carry their text; every other kind publishes its type tag alone (Usage
// and ToolCall detail are not part of this ticket's wire contract - see
// the journal).
func (e *Executor) publishDelta(ctx context.Context, id JobID, ev provider.StreamEvent) {
	if e.bridge == nil {
		return
	}
	payload, _ := json.Marshal(streamEventPayload{Type: ev.Kind.String(), Delta: ev.Delta})
	_, _ = e.bridge.Publish(ctx, conductorEventNamespace, jobEventKind(id), conductorEventSource, payload)
}

// ExecuteStreamJob is the production streaming entry point (R-40.X9):
// mints a fresh JobID via cascade.NewID() (the SAME generator Execute
// uses; no second one is declared here, R-21.217/TestStream_NoSecondJobIDGenerator),
// registers its cancel func in the cancel registry BEFORE any streaming
// begins, then forwards the underlying ExecuteStream dispatch through
// forwardStream, which owns the sync.Once terminal latch and the bridge
// publish calls. The returned CancelFunc is idempotent (delegates to
// ExecuteStream's own idempotent CancelFunc) and is also what job.cancel
// (cancel.go) invokes via the cancel registry for a known job id.
func (e *Executor) ExecuteStreamJob(ctx context.Context, req provider.ModelRequest) (JobID, <-chan provider.StreamEvent, CancelFunc, error) {
	innerCh, innerCancel, err := e.ExecuteStream(ctx, req)
	if err != nil {
		return "", nil, nil, err
	}
	rawID, err := cascade.NewID()
	if err != nil {
		innerCancel()
		return "", nil, nil, cascade.Wrap(cascade.KindInternal, err, "conductor: minting stream job id")
	}
	id := JobID(rawID)

	var cancelled atomic.Bool
	cancelFn := CancelFunc(func() {
		cancelled.Store(true)
		innerCancel()
	})
	e.cancels().register(id, func() { cancelFn() })

	out := make(chan provider.StreamEvent, 1)
	// forwardStream gets the REAL (possibly-cancelled) ctx, not a
	// detached one: its out<-ev select below is the bound on a consumer
	// that stops reading - once ctx is Done, an undelivered event is
	// dropped rather than blocking the producer forever. publishTerminal
	// separately detaches (context.WithoutCancel) so the terminal
	// publish itself is never skipped just because ctx is already Done.
	go e.forwardStream(ctx, id, innerCh, out, &cancelled)
	return id, out, cancelFn, nil
}

// forwardStream owns the sync.Once terminal latch (R-21.217): it forwards
// every delta event from in to out and the bridge, in order, then - after
// in closes - publishes EXACTLY ONE terminal event (the first of
// done|error|cancelled to apply) and deregisters id, both inside the same
// sync.Once, before returning. Every event observed after cancelled flips
// true is dropped from both out and the bridge (the ordering invariant:
// no delta event follows the cancelled terminal event on the wire). ctx's
// own cancellation bounds the out<-ev select only (a stopped consumer
// never blocks this goroutine forever); publishTerminal always detaches
// from ctx internally so the terminal event itself is never dropped.
func (e *Executor) forwardStream(ctx context.Context, id JobID, in <-chan provider.StreamEvent, out chan<- provider.StreamEvent, cancelled *atomic.Bool) {
	defer close(out)
	terminalKind := "done"
	var once sync.Once
	finish := func() {
		once.Do(func() {
			// Client disconnect (the SSE subscription's own ctx
			// cancelling) takes the SAME "cancelled" path as an
			// explicit job.cancel call - one code path, not two: both
			// are detected here via ctx.Err(), regardless of which one
			// actually caused it (R-21.217's client-disconnect
			// equivalence).
			if cancelled.Load() || ctx.Err() != nil {
				terminalKind = "cancelled"
			}
			e.publishTerminal(ctx, id, terminalKind)
			e.cancels().deregister(id)
		})
	}
	defer finish()

	for ev := range in {
		if cancelled.Load() {
			continue
		}
		if ev.Kind == provider.StreamEventError {
			terminalKind = "error"
		}
		e.publishDelta(ctx, id, ev)
		select {
		case out <- ev:
		case <-ctx.Done():
		}
	}
}
