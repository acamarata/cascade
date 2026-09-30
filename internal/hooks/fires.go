package hooks

import (
	"sync"
	"time"
)

// Purpose: the recent-fires ring and the counters the hooks RPCs read.
//
// Inputs: every HookFire the dispatcher records (audit.go), after scrub.
//
// Outputs: Fires (the newest firesRingSize, oldest first), Stats, Hooks.
//
// Constraints: the ring holds HookFire values only, which carry the tagged
//
//	params hash and never params. Memory only; a restart empties it.

// firesRingSize is how many recent fires Fires returns.
const firesRingSize = 100

// Stats is the dispatcher's summary for the hooks RPCs.
type Stats struct {
	// Registered is the number of hooks in the current registry.
	Registered int
	// LastFire is the timestamp of the newest recorded fire (zero if none).
	LastFire time.Time
	// BudgetRefusals counts fires refused with ResultBudget.
	BudgetRefusals uint64
}

// fireLog is the ring and counters. The zero value is ready to use.
type fireLog struct {
	mu             sync.Mutex
	ring           []HookFire
	next           int
	lastFire       time.Time
	budgetRefusals uint64
}

// add records fire, overwriting the oldest once the ring is full.
func (l *fireLog) add(fire HookFire) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.ring) < firesRingSize {
		l.ring = append(l.ring, fire)
	} else {
		l.ring[l.next] = fire
		l.next = (l.next + 1) % firesRingSize
	}
	l.lastFire = fire.Ts
	if fire.ResultCode == ResultBudget {
		l.budgetRefusals++
	}
}

// snapshot returns the ring oldest first.
func (l *fireLog) snapshot() []HookFire {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]HookFire, 0, len(l.ring))
	out = append(out, l.ring[l.next:]...)
	return append(out, l.ring[:l.next]...)
}

// Fires returns the newest recorded fires, at most 100, oldest first.
func (d *Dispatcher) Fires() []HookFire {
	return d.fires.snapshot()
}

// Stats returns the dispatcher's counters.
func (d *Dispatcher) Stats() Stats {
	d.fires.mu.Lock()
	last, refusals := d.fires.lastFire, d.fires.budgetRefusals
	d.fires.mu.Unlock()
	return Stats{Registered: len(d.currentRegistry().List()), LastFire: last, BudgetRefusals: refusals}
}

// Hooks returns a copy of every hook in the current registry, sorted by
// ID. ActionParams are the operator's configured values verbatim; a
// caller exposing them outside the process must decide what to redact.
func (d *Dispatcher) Hooks() []HookConfig {
	return d.currentRegistry().List()
}
