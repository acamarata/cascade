package census

// Purpose (this file): Reader interface and Collector type
//
//	(P1-E12-W3-S25-T4) — a stable typed surface the telemetry epic
//	(R/S-40: `cascade fleet top` and the headroom model) reads census
//	process counts and session attribution through, without importing
//	this package's Enumerator or Snapshot-producing internals directly.
//
// Inputs: []Snapshot, stored wholesale by Store after each Enumerate
//
//	cycle (S-24.T3's poll consumer is the intended caller of Store).
//
// Outputs: a snapshot copy via Latest, or a live count via Count.
// Constraints: fail-closed (Art.1 + 06 §5.20) — Latest never returns a
//
//	partial slice; the entire stored snapshot is read atomically under
//	the read lock. No bare time.Now; no network; pure Go (Art.5).
//
// SPORT: internal.fleet.census.Reader/ADDED,
//
//	internal.fleet.census.Collector/ADDED (P1-E12-W3-S25-T4).

import "sync"

// Reader is the stable read surface over the most recently stored
// census snapshot. Latest and Count both fail closed: before any Store
// call, Latest returns (nil, false) and Count returns 0 — never a
// partial slice and never a negative count.
type Reader interface {
	// Latest returns a copy of the most recently stored snapshot and
	// true, or (nil, false) when nothing has been stored yet. The
	// returned slice is a copy; the caller may mutate it freely without
	// affecting the Collector's stored state.
	Latest() ([]Snapshot, bool)
	// Count returns the number of entries in the last stored snapshot,
	// or 0 when nothing has been stored yet.
	Count() int
}

// Collector implements Reader over an in-memory snapshot guarded by
// a sync.RWMutex. The zero value is not usable; construct with
// NewCollector.
type Collector struct {
	mu      sync.RWMutex
	stored  []Snapshot
	hasData bool
}

// NewCollector returns a ready-to-use Collector whose zero state is
// (nil, false) from Latest and 0 from Count.
func NewCollector() *Collector {
	return &Collector{}
}

// Store replaces the stored snapshot atomically under the write lock. A
// nil or empty snapshots resets the stored state to empty: Latest then
// returns (nil, false) and Count returns 0 — this is intentional reset
// behavior, never an error.
func (c *Collector) Store(snapshots []Snapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(snapshots) == 0 {
		c.stored = nil
		c.hasData = false
		return
	}
	c.stored = snapshots
	c.hasData = true
}

// Latest returns a copy of the stored snapshot and true, or (nil, false)
// when no data has been stored yet. The copy is made here, under the
// read lock, so a caller mutating the returned slice can never race with
// a concurrent Store.
func (c *Collector) Latest() ([]Snapshot, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.hasData {
		return nil, false
	}
	out := make([]Snapshot, len(c.stored))
	copy(out, c.stored)
	return out, true
}

// Count returns the number of entries in the last stored snapshot, or 0
// when nothing has been stored yet.
func (c *Collector) Count() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.hasData {
		return 0
	}
	return len(c.stored)
}

// Compile-time satisfaction assertion: Collector implements Reader.
var _ Reader = (*Collector)(nil)
