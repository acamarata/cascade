package pbd

import (
	"context"
	"errors"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/plugins/pbd/internal/dispatch"
	"github.com/acamarata/cascade/plugins/pbd/internal/pews"
)

// Purpose (this file): the seam that sends a claimed PEWS ticket through
//
//	the Conductor's model.execute door — the ONLY model door (06 §2).
//
// Inputs: a ticket, and an RPC caller reaching the daemon.
// Outputs: the model's output and the job it ran as, or a typed refusal.
// Constraints: this package may import pkg/** and stdlib ONLY, never
//
//	internal/** (02-TARGET-STRUCTURE, enforced by depguard). The typed
//	door is pkg/provider's Client over the frozen D/S-07.T3 client
//	(R-21.280/R-21.281), which is why the seam takes an RPCCaller rather
//	than a transport of its own.
//
//	Journals are NOT written here: S-30.T1 owns them. This file dispatches
//	and reports, nothing else.
//
//	P1 scope (06 §5.18, R-16.12): one-shot and fan-out dispatch only.
//	Agentic AgentProvider execution lands in W6 (AC/S-60.T4); a call site
//	needing it fails with an actionable error rather than a silent no-op.
//
// SPORT: plugins/pbd:dispatch (ADD) — P1-E14-W3-S30-T2.

// dispatchOutcome is one in-flight model.execute call's result, carried
// off the goroutine that made it.
type dispatchOutcome struct {
	resp provider.ModelResponse
	err  error
}

// DispatchResult is what one dispatched ticket produced.
type DispatchResult struct {
	// JobID is the job the Conductor ran this ticket as, kept so a caller
	// can correlate a later cancellation or a streamed event with it.
	JobID provider.JobID
	// TaskClass is the §5.18 class the ticket resolved to. Returned rather
	// than left implicit because it decides which lane ran the work, and a
	// caller reading a surprising result needs to see it.
	TaskClass string
	// Output is the model's final text output.
	Output string
}

// Dispatcher sends one ticket through the model door.
type Dispatcher interface {
	Dispatch(ctx context.Context, ticket *pews.Ticket) (DispatchResult, error)
}

// ConductorDispatcher dispatches through the Conductor's model.execute
// door over the typed pkg/provider client.
type ConductorDispatcher struct {
	client *provider.Client
}

// NewConductorDispatcher builds a dispatcher over caller.
//
// caller is the frozen D/S-07.T3 client (or anything satisfying its
// one-method shape); this package never constructs a transport, because it
// cannot import one.
func NewConductorDispatcher(caller provider.RPCCaller) *ConductorDispatcher {
	return &ConductorDispatcher{client: provider.NewClient(caller)}
}

var _ Dispatcher = (*ConductorDispatcher)(nil)

// Dispatch runs ticket through model.execute and returns its result.
//
// CANCELLATION. When ctx ends the caller is released immediately with
// context.Canceled, and the job is cancelled two ways, because neither
// alone covers both races:
//
//  1. The Conductor derives each job's own context from the REQUEST's
//     (internal/conductor/execute.go), so aborting this RPC cancels the job
//     server-side without the client naming it. This is what ends a job
//     that is still running.
//  2. If the in-flight call instead COMPLETES just as the caller gives up —
//     the job id then exists and the work may have side effects still
//     settling — the idempotent job.cancel door is called with it.
//
// A JobID is minted server-side per call and is not derivable from the
// ticket id, so (2) is only possible once the call returns one; this is why
// it cannot replace (1). Claiming otherwise would be claiming to cancel
// something this seam cannot name.
func (d *ConductorDispatcher) Dispatch(ctx context.Context, ticket *pews.Ticket) (DispatchResult, error) {
	req, err := d.request(ticket)
	if err != nil {
		return DispatchResult{}, err
	}

	done := make(chan dispatchOutcome, 1)
	go func() {
		resp, execErr := d.client.ModelExecute(ctx, req)
		done <- dispatchOutcome{resp: resp, err: execErr}
	}()

	select {
	case got := <-done:
		if got.err != nil {
			return DispatchResult{}, got.err
		}
		return DispatchResult{
			JobID:     got.resp.JobID,
			TaskClass: req.TaskClass,
			Output:    got.resp.Output,
		}, nil
	case <-ctx.Done():
		d.cancelInFlight(done)
		return DispatchResult{}, ctx.Err()
	}
}

// cancelInFlight cancels the job the abandoned dispatch started, once it
// reports its job id.
//
// The cancel rides its OWN context: ctx is already done, and issuing the
// cancel on it would be refused by the transport before it left the
// process — which is the one moment the call actually matters.
func (d *ConductorDispatcher) cancelInFlight(done <-chan dispatchOutcome) {
	go func() {
		got := <-done
		if got.err != nil || got.resp.JobID == "" {
			return
		}
		_ = d.client.JobCancel(context.WithoutCancel(context.Background()), got.resp.JobID)
	}()
}

// request assembles the envelope for ticket.
//
// The field mapping is T0-ratified, not chosen here: tasks become the
// prompt body and spec_refs become context citations.
func (d *ConductorDispatcher) request(ticket *pews.Ticket) (provider.ModelRequest, error) {
	if ticket == nil {
		return provider.ModelRequest{}, cascade.New(cascade.KindInvalidInput,
			"pbd: no ticket to dispatch")
	}
	if strings.TrimSpace(ticket.ID) == "" {
		return provider.ModelRequest{}, cascade.New(cascade.KindInvalidInput,
			"pbd: a ticket needs an id to be dispatched and correlated")
	}
	// A ticket with no tasks carries no work. Dispatching it would send the
	// model a bare title and bill a lane for it, and the empty result would
	// read as a completed ticket — a silent no-op of exactly the kind Art.1
	// forbids. The refusal is here rather than at the caller because every
	// caller reaches the door through this one function.
	if len(ticket.Tasks) == 0 {
		return provider.ModelRequest{}, cascade.Newf(cascade.KindInvalidInput,
			"pbd: ticket %s declares no tasks, so there is no work to dispatch", ticket.ID)
	}
	taskClass, ok := dispatch.TaskClassFor(ticket.ModelClass)
	if !ok {
		return provider.ModelRequest{}, cascade.Newf(cascade.KindInvalidInput,
			"pbd: ticket %s carries model_class %q, which this build does not map to a task class",
			ticket.ID, ticket.ModelClass)
	}
	return provider.ModelRequest{
		TaskID:    ticket.ID,
		TaskClass: taskClass,
		Inputs:    ticketInputs(ticket),
		// Fail-closed per §5.16: the seam STAMPS the request and never
		// widens it. Restricted is SensitivityTier's zero value, so an
		// unresolvable caller tier can only ever narrow the routing, never
		// open it. The Conductor (K/S-22.T3) does the enforcing.
		Sensitivity: provider.SensitivityRestricted,
	}, nil
}

// ticketInputs renders the ticket as the conversation the model executes
// over: the work to do, then the specs it must be done against.
func ticketInputs(ticket *pews.Ticket) []provider.ChatMessage {
	var b strings.Builder
	b.WriteString(ticket.Title)
	for _, task := range ticket.Tasks {
		b.WriteString("\n- ")
		b.WriteString(task)
	}
	if len(ticket.SpecRefs) > 0 {
		b.WriteString("\n\nSpec references:")
		for _, ref := range ticket.SpecRefs {
			b.WriteString("\n- ")
			b.WriteString(ref)
		}
	}
	return []provider.ChatMessage{{Role: "user", Content: b.String()}}
}

// ErrAgenticDispatchUnavailable is what a call site needing agentic
// execution gets in P1.
//
// It is an actionable refusal rather than a silent no-op (Art.1): P1 ships
// one-shot and fan-out dispatch only, and the agentic door is AC/S-60.T4's
// in W6.
var ErrAgenticDispatchUnavailable = errors.New(
	"pbd: agentic dispatch is not available in this build; P1 dispatches through model.execute only")

// DispatchAgentic refuses agentic execution with an actionable error.
//
// CASCADE-ALLOW: agentic-dispatch (AC/S-60.T4)
func DispatchAgentic(_ context.Context, _ *pews.Ticket) (DispatchResult, error) {
	return DispatchResult{}, cascade.Wrap(cascade.KindUnsupported, ErrAgenticDispatchUnavailable,
		"pbd: this ticket needs an agent runtime")
}
