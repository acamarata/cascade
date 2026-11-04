package jobs

// Purpose: terminal observers on jobs.Store. After a transition commits a
// job into a terminal state, each registered observer receives the closed
// outcome mapping (accepted->completed, rejected->failed, failed->failed,
// cancelled->cancelled). The SetTerminalHook lease release keeps its place
// and runs first; observers never replace it.
//
// Inputs: TerminalObserver values registered via RegisterTerminalObserver;
// an optional error sink set via SetTerminalObserverErrorSink.
// Outputs: one ObserveTerminalOutcome call per observer per committed
// terminal transition; observer errors go to the sink.
//
// Constraints: observers run only after the state write committed (a
// refused or failed write never reaches afterTransition). An observer
// error never fails the committed transition and never stops the lease
// release or the remaining observers.
//
// SPORT: jobs/terminal-observer/ADD (P1-TOP-10b).

import (
	"context"
	"sync"
)

// TerminalOutcome is the closed outcome vocabulary a TerminalObserver
// receives for a job's terminal state.
type TerminalOutcome string

// The closed TerminalOutcome values.
const (
	TerminalOutcomeCompleted TerminalOutcome = "completed"
	TerminalOutcomeFailed    TerminalOutcome = "failed"
	TerminalOutcomeCancelled TerminalOutcome = "cancelled"
)

// TerminalObserver is notified after each committed terminal transition.
// An implementation must not block; a returned error goes to the Store's
// observer error sink.
type TerminalObserver interface {
	ObserveTerminalOutcome(ctx context.Context, jobID string, outcome TerminalOutcome) error
}

// terminalObservers is the Store's observer list and error sink.
type terminalObservers struct {
	mu   sync.RWMutex
	list []TerminalObserver
	sink func(error)
}

// RegisterTerminalObserver adds obs to the observers called, in
// registration order, after each committed terminal transition. A nil obs
// is ignored.
func (s *Store) RegisterTerminalObserver(obs TerminalObserver) {
	if obs == nil {
		return
	}
	s.observers.mu.Lock()
	defer s.observers.mu.Unlock()
	s.observers.list = append(s.observers.list, obs)
}

// SetTerminalObserverErrorSink installs sink as the receiver of every
// non-nil error a terminal observer returns. nil removes the sink.
func (s *Store) SetTerminalObserverErrorSink(sink func(error)) {
	s.observers.mu.Lock()
	defer s.observers.mu.Unlock()
	s.observers.sink = sink
}

// notifyTerminalObservers calls every registered observer with the
// outcome for to. afterTransition calls it after the lease-release hook.
func (s *Store) notifyTerminalObservers(ctx context.Context, id string, to JobState) {
	outcome, ok := mapTerminalOutcome(to)
	if !ok {
		return
	}
	s.observers.mu.RLock()
	list := append([]TerminalObserver(nil), s.observers.list...)
	sink := s.observers.sink
	s.observers.mu.RUnlock()
	for _, obs := range list {
		if err := obs.ObserveTerminalOutcome(ctx, id, outcome); err != nil && sink != nil {
			sink(err)
		}
	}
}

// mapTerminalOutcome is the closed mapping from a terminal JobState to its
// TerminalOutcome. A non-terminal state reports false and is never
// observed.
func mapTerminalOutcome(to JobState) (TerminalOutcome, bool) {
	switch to {
	case JobStateAccepted:
		return TerminalOutcomeCompleted, true
	case JobStateRejected, JobStateFailed:
		return TerminalOutcomeFailed, true
	case JobStateCancelled:
		return TerminalOutcomeCancelled, true
	case JobStatePending, JobStateLeased, JobStateRunning, JobStateVerifying,
		JobStateReviewing, JobStateCancelling:
		return "", false
	}
	return "", false
}
