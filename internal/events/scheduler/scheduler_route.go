package scheduler

// Purpose: the scheduler's policy gate. Every cron-triggered dispatch is
//   routed through the one authorization middleware before the job's
//   Runnable is called.
// Inputs: the injected ActionGate, the subject and capability scheduled
//   dispatches are evaluated as, and the CronJob about to fire.
// Outputs: nil when the dispatch may proceed, or the typed refusal Tick
//   records instead of firing.
// Constraints: fail closed. A scheduler with no gate fires NOTHING: an
//   unrouted dispatch is the defect this gate exists to prevent, so the
//   absence of wiring refuses rather than reverting to the ungated
//   behaviour. An ask does not dispatch either — a scheduled job runs on
//   an allow and on nothing else.
// SPORT: internal.events.scheduler.ActionGate/ADDED (P1-E09-W2-S18-T5).

import (
	"context"

	"github.com/acamarata/cascade/internal/events/routing"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ActionGate is the policy-routing seam. It is satisfied by
// *routing.ActionRouter and declared as an interface so this package never
// constructs one.
type ActionGate interface {
	// RouteAction decides whether action may run.
	RouteAction(ctx context.Context, action routing.Action) (policy.Verdict, policy.Trace, error)
}

// ScheduledActionText is the canonical action text a scheduled dispatch is
// classified from. A scheduled job has no command line of its own: it
// names a registered runnable, so the text names that runnable rather
// than inventing a command the classifier would reason about wrongly.
// Because the classifier cannot resolve this text to a rung, it lands at
// the top rung and the dispatch is refused unless a standing grant on the
// scheduler capability permits it, which is the intended operator gesture.
// ScheduledActionText is the action text a cron dispatch carries, and it is
// deliberately EMPTY. A scheduled dispatch runs an in-process job: there is
// no command line, so there is nothing for the shell classifier to read.
// This previously returned "scheduler.fire <owner>", a command-shaped string
// invented to fill the field, which the classifier then correctly refused to
// recognise, pinning every dispatch at L4 where no grant could reach it
// (R-14.211). The owner is carried in the action's attributes, where it is
// data rather than a fabricated command.
func ScheduledActionText(string) string { return "" }

// SetActionGate installs the policy gate every cron-triggered dispatch is
// routed through, together with the subject and capability those
// dispatches are evaluated as. All three are required.
//
// It must be called before Activate. Until it is, Tick fires nothing.
func (s *Scheduler) SetActionGate(gate ActionGate, subject policy.Subject, capability string) error {
	if gate == nil {
		return cascade.New(cascade.KindInvalidInput, "scheduler: action gate is required")
	}
	if err := subject.Validate(); err != nil {
		return err
	}
	if capability == "" {
		return cascade.New(cascade.KindInvalidInput, "scheduler: routing capability is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gate = gate
	s.gateSubject = subject
	s.gateCapability = capability
	return nil
}

// gateConfig returns the routing collaborators under the lock, so a Tick
// reads a consistent set.
func (s *Scheduler) gateConfig() (ActionGate, policy.Subject, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gate, s.gateSubject, s.gateCapability
}

// routeDispatch asks the policy engine whether job may fire. A nil return
// authorizes this one dispatch; every other outcome refuses it.
func (s *Scheduler) routeDispatch(ctx context.Context, job CronJob) error {
	gate, subject, capability := s.gateConfig()
	if gate == nil {
		return cascade.Newf(cascade.KindPolicyDenied,
			"scheduler: job %q cannot fire: no policy routing is wired (%s)",
			job.ID, routing.RouteDeniedCode)
	}
	verdict, _, err := gate.RouteAction(ctx, routing.Action{
		Subject:    subject,
		Capability: capability,
		Verb:       string(EventKindSchedulerFired),
		Command:    ScheduledActionText(job.Owner),
		Params:     []byte(job.ID + "\x00" + job.Spec),
		Origin:     routing.OriginScheduler,
		Ref:        job.ID,
		Summary:    "scheduled job " + job.ID,
	})
	if verdict == policy.VerdictAllow && err == nil {
		return nil
	}
	if err != nil {
		return err
	}
	return cascade.Newf(cascade.KindPolicyDenied,
		"scheduler: job %q was not authorized to fire (verdict %s) (%s)",
		job.ID, verdict, routing.RouteDeniedCode)
}
