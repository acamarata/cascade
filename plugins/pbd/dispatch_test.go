package pbd

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/plugins/pbd/internal/pews"
)

// Purpose (this file): the dispatch seam's contract — what reaches the
//   model door, the fail-closed sensitivity stamp, and that an abandoned
//   dispatch actually CANCELS its job rather than merely returning.
// Constraints: no socket. The door is the injected RPCCaller seam, which is
//   the same one-method interface the real client satisfies.
// SPORT: plugins/pbd tests (ADD) — P1-E14-W3-S30-T2.

// recordingCaller records every JSON-RPC call and answers from a canned
// response.
type recordingCaller struct {
	mu      sync.Mutex
	methods []string
	req     provider.ModelRequest
	jobID   provider.JobID
	err     error
	// block, when set, holds model.execute until it is closed.
	block chan struct{}
}

func (c *recordingCaller) Do(_ context.Context, method string, params, out any) error {
	c.mu.Lock()
	c.methods = append(c.methods, method)
	c.mu.Unlock()

	if strings.Contains(method, "cancel") {
		return nil
	}
	if c.block != nil {
		// Deliberately NOT selecting on ctx.Done: this models the race
		// that (2) covers — the call completes just as the caller gives
		// up, so a job id exists and can be cancelled by name.
		<-c.block
	}
	if c.err != nil {
		return c.err
	}
	if resp, ok := out.(*provider.ModelResponse); ok {
		resp.JobID = c.jobID
		resp.Output = "done"
	}
	_ = params
	return nil
}

// abortingCaller models the other race: the RPC is aborted by the caller's
// own cancellation, so no job id ever comes back.
type abortingCaller struct{ recordingCaller }

func (c *abortingCaller) Do(ctx context.Context, method string, _, _ any) error {
	if strings.Contains(method, "cancel") {
		c.mu.Lock()
		c.methods = append(c.methods, method)
		c.mu.Unlock()
		return nil
	}
	c.mu.Lock()
	c.methods = append(c.methods, method)
	c.mu.Unlock()
	<-ctx.Done()
	return ctx.Err()
}

// called reports the methods seen so far.
func (c *recordingCaller) called() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.methods...)
}

// buildTicket is a ticket every rule admits.
func buildTicket() *pews.Ticket {
	return &pews.Ticket{
		ID:         "P1-E01-W1-S01-T1",
		Title:      "Do the thing",
		ModelClass: pews.ModelClassBuild,
		Tasks:      []string{"first task", "second task"},
		SpecRefs:   []string{"06 §5.18"},
	}
}

// TestTheTicketReachesTheModelDoorAsOneRequest asserts what actually goes
// on the wire: the door that was called, the task class the §5.18 mapping
// produced, and the ticket's work in the prompt.
func TestTheTicketReachesTheModelDoorAsOneRequest(t *testing.T) {
	caller := &recordingCaller{jobID: "job-1"}
	got, err := NewConductorDispatcher(caller).Dispatch(context.Background(), buildTicket())
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got.JobID != "job-1" || got.Output != "done" {
		t.Errorf("result = %+v", got)
	}
	if got.TaskClass != "code" {
		t.Errorf("task class = %q, want build → code", got.TaskClass)
	}
	methods := caller.called()
	if len(methods) != 1 {
		t.Fatalf("calls = %v, want exactly one model door call", methods)
	}
}

// TestSensitivityIsStampedFailClosed is §5.16's rule at this seam. The
// seam never widens: an unresolvable caller tier can only narrow routing.
func TestSensitivityIsStampedFailClosed(t *testing.T) {
	d := &ConductorDispatcher{}
	req, err := d.request(buildTicket())
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if req.Sensitivity != provider.SensitivityRestricted {
		t.Fatalf("sensitivity = %v, want restricted (the fail-closed default)", req.Sensitivity)
	}
	// And it is the ZERO value, so a future field added without a stamp
	// still cannot read as permissive.
	var unset provider.SensitivityTier
	if unset != provider.SensitivityRestricted {
		t.Error("SensitivityTier's zero value is no longer restricted; the fail-closed default has moved")
	}
}

// TestTheTicketsWorkAndSpecsAreInThePrompt proves the ratified field
// mapping: tasks become the prompt body, spec_refs the citations.
func TestTheTicketsWorkAndSpecsAreInThePrompt(t *testing.T) {
	req, err := (&ConductorDispatcher{}).request(buildTicket())
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Inputs) != 1 {
		t.Fatalf("inputs = %d turns, want 1", len(req.Inputs))
	}
	body := req.Inputs[0].Content
	for _, want := range []string{"Do the thing", "first task", "second task", "06 §5.18"} {
		if !strings.Contains(body, want) {
			t.Errorf("the prompt is missing %q:\n%s", want, body)
		}
	}
	if req.TaskID != "P1-E01-W1-S01-T1" {
		t.Errorf("task id = %q, want the ticket id so the job can be correlated", req.TaskID)
	}
}

// TestAnUnmappableTicketNeverReachesTheDoor proves a model_class this
// build cannot classify is refused BEFORE dispatch. A guessed task class
// routes the work to the wrong lane, which is worse than not running it.
func TestAnUnmappableTicketNeverReachesTheDoor(t *testing.T) {
	caller := &recordingCaller{jobID: "job-1"}
	ticket := buildTicket()
	ticket.ModelClass = "oracle"

	if _, err := NewConductorDispatcher(caller).Dispatch(context.Background(), ticket); err == nil {
		t.Fatal("a ticket with an unmappable model_class was dispatched")
	}
	if len(caller.called()) != 0 {
		t.Errorf("the model door was called %v for a ticket that could not be classified", caller.called())
	}
}

// TestAnUnidentifiedTicketIsRefused covers the id the job is correlated by.
func TestAnUnidentifiedTicketIsRefused(t *testing.T) {
	caller := &recordingCaller{}
	d := NewConductorDispatcher(caller)
	if _, err := d.Dispatch(context.Background(), nil); err == nil {
		t.Error("a nil ticket was dispatched")
	}
	ticket := buildTicket()
	ticket.ID = "  "
	if _, err := d.Dispatch(context.Background(), ticket); err == nil {
		t.Error("a ticket with no id was dispatched")
	}
	if len(caller.called()) != 0 {
		t.Errorf("the model door was called %v for tickets that never validated", caller.called())
	}
}

// TestAnAbandonedDispatchCancelsItsJob asserts the CALL rather than the
// return: when a dispatch is abandoned and its call nonetheless yields a
// job id, that job is cancelled by name at the daemon. Without this the
// work would settle against a job nobody is waiting for.
func TestAnAbandonedDispatchCancelsItsJob(t *testing.T) {
	caller := &recordingCaller{jobID: "job-9", block: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := NewConductorDispatcher(caller).Dispatch(ctx, buildTicket())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	// Release the in-flight call so the cancel goroutine can see its job id.
	close(caller.block)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, m := range caller.called() {
			if strings.Contains(m, "cancel") {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no cancel reached the daemon; calls were %v", caller.called())
}

// TestADoorFailureIsSurfaced proves a refused dispatch is reported rather
// than returned as an empty success.
func TestADoorFailureIsSurfaced(t *testing.T) {
	caller := &recordingCaller{err: errors.New("no lane available")}
	if _, err := NewConductorDispatcher(caller).Dispatch(context.Background(), buildTicket()); err == nil {
		t.Fatal("a refused dispatch reported success")
	}
}

// TestAgenticDispatchRefusesActionably proves the P1 scope boundary is a
// typed, actionable refusal and never a silent no-op (Art.1).
func TestAgenticDispatchRefusesActionably(t *testing.T) {
	_, err := DispatchAgentic(context.Background(), buildTicket())
	if err == nil {
		t.Fatal("agentic dispatch silently succeeded")
	}
	if !errors.Is(err, ErrAgenticDispatchUnavailable) {
		t.Errorf("err = %v, want it to wrap ErrAgenticDispatchUnavailable", err)
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnsupported {
		t.Errorf("kind = %v (ok=%v), want KindUnsupported", kind, ok)
	}
}

// TestAnAbortedDispatchDoesNotInventAJobToCancel is the other half of the
// cancellation contract. When the RPC is aborted by the caller's own
// cancellation no job id ever comes back, so there is nothing to name —
// and the seam must NOT fabricate one. The job is ended server-side
// instead, by the request-scoped context the Conductor derives it from.
func TestAnAbortedDispatchDoesNotInventAJobToCancel(t *testing.T) {
	caller := &abortingCaller{}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := NewConductorDispatcher(caller).Dispatch(ctx, buildTicket())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	time.Sleep(100 * time.Millisecond)
	for _, m := range caller.called() {
		if strings.Contains(m, "cancel") {
			t.Fatalf("the seam cancelled a job it never had an id for; calls were %v", caller.called())
		}
	}
}
