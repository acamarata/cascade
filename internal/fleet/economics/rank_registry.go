// Purpose: ordered lane-rank extensions, sealed atomically on first use.
// Inputs: named predicates and terms. Outputs: immutable evaluation snapshots.
// Constraints: callbacks run outside the lock; builtins always precede extensions.

package economics

import (
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
)

type rankRegistry struct {
	mu         sync.Mutex
	sealed     bool
	predicates []Predicate
	terms      []UtilityTerm
}

var laneRankRegistry = newRankRegistry()

func newRankRegistry() *rankRegistry {
	return &rankRegistry{predicates: EligibilityPredicates(), terms: BuiltinUtilityTerms()}
}

// RegisterPredicate appends a named predicate before the first Rank call.
// Duplicate names and registration after sealing return KindConflict.
func RegisterPredicate(p Predicate) error { return laneRankRegistry.registerPredicate(p) }

// RegisterTerm appends a named utility term before the first Rank call.
// Duplicate names and registration after sealing return KindConflict.
func RegisterTerm(t UtilityTerm) error { return laneRankRegistry.registerTerm(t) }

func (r *rankRegistry) registerPredicate(p Predicate) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return cascade.New(cascade.KindConflict, "economics: rank registry sealed")
	}
	for _, existing := range r.predicates {
		if existing.Name == p.Name {
			return cascade.New(cascade.KindConflict, "economics: duplicate rank predicate")
		}
	}
	if p.Name == "" || p.Eval == nil {
		return cascade.New(cascade.KindInvalidInput, "economics: invalid rank predicate")
	}
	r.predicates = append(r.predicates, p)
	return nil
}

func (r *rankRegistry) registerTerm(t UtilityTerm) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return cascade.New(cascade.KindConflict, "economics: rank registry sealed")
	}
	for _, existing := range r.terms {
		if existing.Name == t.Name {
			return cascade.New(cascade.KindConflict, "economics: duplicate rank term")
		}
	}
	if t.Name == "" || t.Eval == nil {
		return cascade.New(cascade.KindInvalidInput, "economics: invalid rank term")
	}
	r.terms = append(r.terms, t)
	return nil
}

func (r *rankRegistry) seal() ([]Predicate, []UtilityTerm) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sealed = true
	return append([]Predicate(nil), r.predicates...), append([]UtilityTerm(nil), r.terms...)
}
