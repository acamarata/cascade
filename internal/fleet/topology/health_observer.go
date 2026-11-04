// Purpose: the post-commit lane health observer seam (P1-TOP-11). A Store
// accepts one LaneHealthObserver via SetHealthObserver; the reconcile lane
// writers (reconcile_upsert.go) call it once per changed lane health, only
// after their transaction commits. A rolled-back transaction publishes nothing.
// Store's struct lives in schema.go, so the binding is held here, keyed by
// the *Store the caller configured.

package topology

import (
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
)

// LaneHealthObserver receives one committed lane health transition.
type LaneHealthObserver func(lane LaneID, from, to LaneHealth)

// HealthTransition is one lane health change staged inside a transaction
// and published only after it commits.
type HealthTransition struct {
	Lane LaneID
	From LaneHealth
	To   LaneHealth
}

var (
	errNilHealthObserver = cascade.New(cascade.KindInvalidInput, "topology: nil lane health observer")
	errHealthObserverSet = cascade.New(cascade.KindConflict, "topology: lane health observer already set")
)

var healthObservers sync.Map // *Store -> LaneHealthObserver

// SetHealthObserver binds obs to s. It refuses a nil observer and refuses a
// second call: the first binding stays in force (fail closed).
func (s *Store) SetHealthObserver(obs LaneHealthObserver) error {
	if s == nil || obs == nil {
		return errNilHealthObserver
	}
	if _, loaded := healthObservers.LoadOrStore(s, obs); loaded {
		return errHealthObserverSet
	}
	return nil
}

// healthObserver returns s's bound observer, or nil.
func (s *Store) healthObserver() LaneHealthObserver {
	v, ok := healthObservers.Load(s)
	if !ok {
		return nil
	}
	return v.(LaneHealthObserver)
}

// publishHealth calls s's observer once per transition, in order. Callers
// invoke it only after Commit returned nil.
func (s *Store) publishHealth(transitions []HealthTransition) {
	obs := s.healthObserver()
	if obs == nil {
		return
	}
	for _, tr := range transitions {
		obs(tr.Lane, tr.From, tr.To)
	}
}
