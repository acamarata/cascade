package tierops

import (
	"cmp"
	"slices"

	"github.com/acamarata/cascade/pkg/provider"
)

// greater hides an ordered comparison behind a type parameter whose
// constraint carries type terms; the gate reports the instantiation.
func greater[T ~uint8](a, b T) bool { return a > b }

func orderedMax[T cmp.Ordered](a, b T) T {
	if a > b {
		return a
	}
	return b
}

type ordered[T cmp.Ordered] struct{ v T }

// orderedAlias is a generic alias: its own type parameter carries the terms.
type orderedAlias[T cmp.Ordered] = ordered[T]

type tierRef[P ~*provider.SensitivityTier] struct{ p P }

func comparableOnly[T comparable](a, b T) bool { return a == b }

func safeGenerics(a, b alias) {
	_ = comparableOnly(a, b)
	_ = slices.Contains([]alias{a}, b)
	_ = slices.Index([]provider.SensitivityTier{a}, b)
	_ = greater(uint8(1), uint8(2))
}

func genericViolations(a, b provider.SensitivityTier, c alias, p *alias) {
	_ = greater(a, b)                             // want tier violation
	_ = greater[alias](c, c)                      // want tier violation
	_ = orderedMax(*p, b)                         // want tier violation
	_ = ordered[provider.SensitivityTier]{v: a}   // want tier violation
	_ = slices.Max([]provider.SensitivityTier{a}) // want tier violation
	slices.Sort([]alias{a, b})                    // want tier violation
	_ = orderedAlias[alias]{v: c}                 // want tier violation
	_ = tierRef[*provider.SensitivityTier]{p: p}  // want tier violation
}
