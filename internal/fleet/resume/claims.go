// Purpose: the per-daemon in-flight claim table for fan-out ids
//   (contract:fanout-producer, CLAIMS). The conductor.execute fan-out
//   branch, Scan and Sweep all claim the bare fan-out id before they read
//   or write a fan-out entity, so a live call, a re-attach and a sweep
//   never work on the same fan-out at once.
// Inputs: bare fan-out ids (never the "fanout:" entity id).
// Outputs: TryClaim reports whether the caller now holds the id.
// Constraints: in-memory only; the store's exclusive lock already means
//   one daemon per store, so one table per daemon is the whole truth. A
//   nil table never grants a claim.
// SPORT: internal.fleet.resume.Claims/ADDED (P1-CORE-19).

package resume

import "sync"

// Claims is the in-flight table. The zero value is not usable; build it
// with NewClaims.
type Claims struct {
	mu   sync.Mutex
	held map[string]struct{}
}

// NewClaims returns an empty claim table.
func NewClaims() *Claims {
	return &Claims{held: make(map[string]struct{})}
}

// TryClaim claims id and reports true, or reports false when id is
// already held. A nil table returns false, never true.
func (c *Claims) TryClaim(id string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, busy := c.held[id]; busy {
		return false
	}
	c.held[id] = struct{}{}
	return true
}

// Release gives id back. Releasing an id that is not held is a no-op.
func (c *Claims) Release(id string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.held, id)
}
