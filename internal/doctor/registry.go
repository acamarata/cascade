package doctor

import (
	"fmt"
	"sort"
	"sync"
)

// Purpose: CheckRegistry — the composition-root registration point every
//
//	subsystem calls Register on for its Check implementations.
//
// Inputs: Check values (check.go).
// Outputs: a stable, name-sorted list of registered checks for the Runner
//
//	to execute, optionally filtered to FirstRun=true.
//
// Constraints: Register is called once per check at init time from many
//
//	packages; it must be safe under concurrent init (Go runs package
//	init serially per-package but composition roots may call Register
//	from goroutines in tests), hence the mutex. A duplicate Name is a
//	programmer error, not a runtime condition to tolerate silently.
//
// SPORT: placeholder: doctor/framework (ADD).

// CheckRegistry holds every registered Check, keyed by Name.
type CheckRegistry struct {
	mu     sync.Mutex
	checks map[string]Check
}

// NewCheckRegistry returns an empty registry.
func NewCheckRegistry() *CheckRegistry {
	return &CheckRegistry{checks: map[string]Check{}}
}

// Register adds check to the registry. It panics on a duplicate Name or
// an empty Name — both are programmer errors caught at composition-root
// init time, long before any user-facing run, so failing loud here is
// preferable to a silently-dropped or silently-overwritten check.
func (r *CheckRegistry) Register(check Check) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := check.Name()
	if name == "" {
		panic("doctor: Register called with an empty Check.Name()")
	}
	if _, exists := r.checks[name]; exists {
		panic(fmt.Sprintf("doctor: duplicate Check registration for name %q", name))
	}
	r.checks[name] = check
}

// Lookup returns the registered check named name, if any.
func (r *CheckRegistry) Lookup(name string) (Check, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.checks[name]
	return c, ok
}

// List returns every registered check, sorted by Name for deterministic
// iteration order (registration order is not guaranteed across packages'
// init funcs).
func (r *CheckRegistry) List() []Check {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Check, 0, len(r.checks))
	for _, c := range r.checks {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// FirstRun returns only the registered checks whose Metadata().FirstRun
// is true, sorted by Name — the set `cascade doctor --first-run` runs.
func (r *CheckRegistry) FirstRun() []Check {
	all := r.List()
	out := make([]Check, 0, len(all))
	for _, c := range all {
		if c.Metadata().FirstRun {
			out = append(out, c)
		}
	}
	return out
}

// Len reports how many checks are registered.
func (r *CheckRegistry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.checks)
}
