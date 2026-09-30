package hooks

import (
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: the fire budget. Every fire is checked before anything runs:
//
//	a lineage bound (chain depth and fires per root event) over the bus's
//	in-memory cause table, a per-hook rate backstop on the injected clock
//	for loops the lineage cannot see (a publisher that drops the runner's
//	context), and a same-fire redelivery check.
//
// Inputs: the matched hook id, the event's (namespace, seq), the bus's
//
//	CauseOf answer, and the clock's now.
//
// Outputs: the fire's chain and either admission or a refusal the
//
//	dispatcher records as one HookFire with result_code budget (or
//	duplicate for a redelivered fire).
//
// Constraints: fail closed; every limit refuses and never runs. Every
//
//	counter is memory only: a restart resets the cause table and every
//	counter, so a crash loop gets up to MaxFiresPerHookPerWindow fires
//	per hook per restart.

// Budget limits.
const (
	// MaxChainDepth is the deepest hop a chain may run at: depths 0..4 run
	// and a depth-5 fire is refused.
	MaxChainDepth = 4
	// MaxFiresPerRoot is the number of fires one root event's whole chain
	// may admit.
	MaxFiresPerRoot = 32
	// RateWindow and MaxFiresPerHookPerWindow bound one hook id to 32
	// admitted fires in any 60 seconds of the injected clock.
	RateWindow               = 60 * time.Second
	MaxFiresPerHookPerWindow = 32
)

// budgetMemory bounds how many roots and recent fires the budget tracks.
const budgetMemory = 4096

// rootKey identifies a chain's root event.
type rootKey struct {
	namespace string
	seq       uint64
}

// fireKey identifies one (hook, event) pair.
type fireKey struct {
	hookID string
	root   rootKey
}

// budget is the dispatcher's fire accounting. The zero value is not
// usable; construct with newBudget.
type budget struct {
	mu        sync.Mutex
	roots     map[rootKey]int
	rootOrder []rootKey
	seen      map[fireKey]bool
	seenOrder []fireKey
	rate      map[string][]time.Time
}

func newBudget() *budget {
	return &budget{
		roots: make(map[rootKey]int),
		seen:  make(map[fireKey]bool),
		rate:  make(map[string][]time.Time),
	}
}

// chainFor returns the chain a fire on (ns, seq) belongs to.
func chainFor(bus *events.Bus, ns string, seq uint64) events.Cause {
	if c, ok := bus.CauseOf(ns, seq); ok {
		return events.Cause{RootNamespace: c.RootNamespace, RootSeq: c.RootSeq, Depth: c.Depth + 1}
	}
	return events.Cause{RootNamespace: ns, RootSeq: seq}
}

// admit decides one fire. It returns ResultBudget or ResultDuplicate with
// the refusal error, or "" and nil when the fire may proceed.
func (b *budget) admit(hookID, ns string, seq uint64, chain events.Cause, now time.Time) (ResultCode, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	fk := fireKey{hookID: hookID, root: rootKey{namespace: ns, seq: seq}}
	if b.seen[fk] {
		return ResultDuplicate, cascade.Newf(cascade.KindConflict,
			"hooks: hook %q already fired for %s seq %d", hookID, ns, seq)
	}
	if chain.Depth > MaxChainDepth {
		return ResultBudget, budgetError(hookID, "chain depth %d exceeds %d", chain.Depth, MaxChainDepth)
	}
	rk := rootKey{namespace: chain.RootNamespace, seq: chain.RootSeq}
	if b.roots[rk] >= MaxFiresPerRoot {
		return ResultBudget, budgetError(hookID, "root event already admitted %d fires", MaxFiresPerRoot)
	}
	recent := b.recentLocked(hookID, now)
	if len(recent) >= MaxFiresPerHookPerWindow {
		return ResultBudget, budgetError(hookID, "%d fires inside %s", MaxFiresPerHookPerWindow, RateWindow)
	}
	b.rate[hookID] = append(recent, now)
	b.countRootLocked(rk)
	b.markSeenLocked(fk)
	return "", nil
}

// recentLocked returns hookID's admitted fire times still inside the
// window ending at now, pruning older ones.
func (b *budget) recentLocked(hookID string, now time.Time) []time.Time {
	times := b.rate[hookID]
	kept := times[:0]
	for _, t := range times {
		if now.Sub(t) < RateWindow {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(b.rate, hookID)
		return nil
	}
	return kept
}

// countRootLocked counts one admitted fire against rk, forgetting the
// oldest tracked root once budgetMemory roots are held.
func (b *budget) countRootLocked(rk rootKey) {
	if _, tracked := b.roots[rk]; !tracked {
		if len(b.rootOrder) == budgetMemory {
			delete(b.roots, b.rootOrder[0])
			b.rootOrder = b.rootOrder[1:]
		}
		b.rootOrder = append(b.rootOrder, rk)
	}
	b.roots[rk]++
}

// markSeenLocked remembers fk, forgetting the oldest once budgetMemory
// fires are held.
func (b *budget) markSeenLocked(fk fireKey) {
	if len(b.seenOrder) == budgetMemory {
		delete(b.seen, b.seenOrder[0])
		b.seenOrder = b.seenOrder[1:]
	}
	b.seenOrder = append(b.seenOrder, fk)
	b.seen[fk] = true
}

// budgetError is a budget refusal.
func budgetError(hookID, format string, args ...any) error {
	return cascade.Newf(cascade.KindQuotaExhausted, "hooks: hook %q refused by the fire budget: "+format,
		append([]any{hookID}, args...)...)
}
