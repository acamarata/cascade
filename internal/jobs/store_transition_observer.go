package jobs

// Purpose: one transition observer on jobs.Store. After every committed
// state write (PutTransition and the completion commit, both of which end
// in afterTransition) the observer receives the job's new state, its
// updated_at and its consecutive failed attempts. PublishTransitions is
// the observer that puts those transitions on the core bus under the
// "jobs" namespace with the rpc.EventJob* kinds the jobs SSE topic serves.
//
// Inputs: a TransitionObserver installed via SetTransitionObserver; an
// *events.Bus for PublishTransitions.
// Outputs: one observer call per committed state write; job.transitioned
// plus at most one of job.leased, job.completed, job.failed per call.
//
// Constraints: the observer runs only after the write committed; a read
// failure or a missing row goes to the terminal observer error sink and
// the observer is not called. The observer never fails the transition.
//
// SPORT: jobs/transition-observer/ADD (P1-EXE-01e).

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TransitionNamespace is the events.Bus namespace job transitions publish to.
const TransitionNamespace = "jobs"

// transitionSource is the Source every published transition carries.
const transitionSource = "jobs.store"

// Transition is one committed job state write.
type Transition struct {
	JobID                     string   `json:"job_id"`
	To                        JobState `json:"to"`
	At                        int64    `json:"at"`
	ConsecutiveFailedAttempts int64    `json:"consecutive_failed_attempts"`
}

// TransitionObserver receives every committed job state write. It must
// not block.
type TransitionObserver func(ctx context.Context, t Transition)

// transitionObserver is the Store's mutex-guarded observer slot.
type transitionObserver struct {
	mu sync.RWMutex
	fn TransitionObserver
}

// SetTransitionObserver installs fn as the observer of every committed
// state write. nil removes it.
func (s *Store) SetTransitionObserver(fn TransitionObserver) {
	s.transition.mu.Lock()
	defer s.transition.mu.Unlock()
	s.transition.fn = fn
}

// notifyTransition reads id's committed row and calls the observer once.
// afterTransition defers it so every state write reaches it.
func (s *Store) notifyTransition(ctx context.Context, id string, to JobState) {
	s.transition.mu.RLock()
	fn := s.transition.fn
	s.transition.mu.RUnlock()
	if fn == nil {
		return
	}
	j, ok, err := s.GetJob(ctx, id)
	if err == nil && !ok {
		err = cascade.Newf(cascade.KindNotFound, "jobs: transition observer: job %q not found", id)
	}
	if err != nil {
		s.observers.mu.RLock()
		sink := s.observers.sink
		s.observers.mu.RUnlock()
		if sink != nil {
			sink(err)
		}
		return
	}
	fn(ctx, Transition{JobID: id, To: to, At: j.UpdatedAt, ConsecutiveFailedAttempts: j.ConsecutiveFailedAttempts})
}

// PublishTransitions returns the observer that publishes each Transition
// on bus under TransitionNamespace: job.transitioned always, plus
// job.leased for leased, job.completed for accepted and job.failed for
// failed or rejected, each with the Transition as its JSON payload. A
// publish error goes to sink when sink is non-nil.
func PublishTransitions(bus *events.Bus, sink func(error)) TransitionObserver {
	report := func(err error) {
		if err != nil && sink != nil {
			sink(err)
		}
	}
	return func(ctx context.Context, t Transition) {
		if bus == nil {
			report(cascade.New(cascade.KindUnavailable, "jobs: publish transition: nil bus"))
			return
		}
		// Transition holds only strings and integers; Marshal cannot fail.
		payload, _ := json.Marshal(t)
		kinds := []events.EventKind{rpc.EventJobTransitioned}
		if extra, ok := transitionExtraKind(t.To); ok {
			kinds = append(kinds, extra)
		}
		for _, kind := range kinds {
			_, err := bus.Publish(ctx, TransitionNamespace, kind, transitionSource, payload)
			report(err)
		}
	}
}

// transitionExtraKind is the closed mapping from a state to the second
// kind its transition publishes.
func transitionExtraKind(to JobState) (events.EventKind, bool) {
	switch to {
	case JobStateLeased:
		return rpc.EventJobLeased, true
	case JobStateAccepted:
		return rpc.EventJobCompleted, true
	case JobStateFailed, JobStateRejected:
		return rpc.EventJobFailed, true
	case JobStatePending, JobStateRunning, JobStateVerifying, JobStateReviewing, JobStateCancelling, JobStateCancelled:
		return "", false
	}
	return "", false
}
