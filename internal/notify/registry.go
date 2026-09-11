// Purpose: the Subscriber contract and a goroutine-safe registry of live
//
//	subscriptions the dispatch loop fans a Notification out to.
//
// Inputs: Subscribe takes a Subscriber plus the CandidateSession
//
//	dispatch.go evaluates ScopeDeliveryPredicate against for that
//	subscriber.
//
// Outputs: Registry.Snapshot returns a stable slice for one drain tick,
//
//	so a concurrent Subscribe/Unsubscribe never mutates a slice
//	dispatch.go is mid-iteration over.
//
// Constraints: every method is safe for concurrent use; Snapshot copies
//
//	rather than exposing the live map.
//
// SPORT: internal.notify.Subscriber/ADDED, internal.notify.Registry/ADDED
//
//	(P1-E23-W5-S49-T1).

package notify

import "sync"

// Subscriber receives dispatched Notifications. Match narrows delivery
// further than the scope predicate does (e.g. a CLI surface subscribed
// only to Priority <= High); a Subscriber whose Match returns false for a
// given Notification is never handed it, regardless of scope eligibility.
type Subscriber interface {
	// ID uniquely identifies this Subscriber for the delivery ledger and
	// Unsubscribe.
	ID() string
	// Match reports whether this Subscriber wants n, independent of
	// scope eligibility (evaluated separately via ScopeDeliveryPredicate).
	Match(n Notification) bool
	// Receive delivers n. A non-nil error is logged by the dispatch loop
	// and never blocks delivery to any other Subscriber.
	Receive(n Notification) error
}

// Registration is one active Subscribe call's stored state, returned by
// Snapshot for the dispatch loop to range over.
type Registration struct {
	Sub     Subscriber
	Session CandidateSession
}

// Registry is a goroutine-safe set of active subscriptions, keyed by
// Subscriber.ID.
type Registry struct {
	mu   sync.RWMutex
	subs map[string]Registration
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{subs: make(map[string]Registration)}
}

// Subscribe registers sub, paired with the CandidateSession the dispatch
// loop evaluates ScopeDeliveryPredicate against for it. Subscribing again
// under the same Subscriber.ID replaces the prior registration.
func (r *Registry) Subscribe(sub Subscriber, session CandidateSession) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.subs[sub.ID()] = Registration{Sub: sub, Session: session}
}

// Unsubscribe removes id's registration, if any. Unsubscribing an unknown
// id is a no-op, not an error: callers are not required to track whether
// they already unsubscribed.
func (r *Registry) Unsubscribe(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.subs, id)
}

// Snapshot returns a stable, independently-owned copy of every active
// registration, safe to range over while other goroutines Subscribe or
// Unsubscribe concurrently.
func (r *Registry) Snapshot() []Registration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Registration, 0, len(r.subs))
	for _, reg := range r.subs {
		out = append(out, reg)
	}
	return out
}

// Len reports the number of active registrations.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.subs)
}
