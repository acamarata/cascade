package learn

// Purpose: the learned-config target registry: immutable TargetIDs, their
//   field paths, tier, denylist areas, shape and tighten-only bound, the
//   closed alias table, and the lookups every classification goes through
//   (ResolveTarget, TargetForConfigKey, TierFor).
// Inputs: target ids, aliases and dotted config keys.
// Outputs: resolved TargetIDs, registry rows and tiers.
// Constraints: aliases resolve before every deny and tier lookup. The
//   registry is closed Go data (target_registry.go) built once at package
//   init; an id that breaks the TargetID syntax, a duplicate id, an alias
//   that shadows an id or points nowhere, or a safe or behavioral path the
//   denylist matches refuses there, so the process never starts with a bad
//   table. Ids are append-only: never reused, removed or repointed. For a
//   limit target the bound's loose end IS the static default, so a learned
//   value can only tighten it.
// SPORT: internal.learn.Targets/ADDED, internal.learn.ResolveTarget/ADDED,
//   internal.learn.TargetForConfigKey/ADDED, internal.learn.TierFor/ADDED
//   (P1-LRN-01).

import (
	"regexp"
	"slices"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TargetID names one learnable (or denied) configuration target. Syntax:
// ^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$. Append-only, never reused or
// repointed.
type TargetID string

// KnownStepValues returns a fresh copy of the closed command vocabulary for
// learned add_step changes. Change.Value carries the quoted TOML literal.
// Callers cannot extend the vocabulary by modifying the returned slice.
func KnownStepValues() []string {
	return []string{"cascade context generate --check"}
}

// targetIDPattern is the TargetID syntax.
var targetIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

// Shape is the kind of change a target admits.
type Shape string

// The closed shape set.
const (
	ShapeNumericBounded Shape = "numeric_bounded"
	ShapeReorderOnly    Shape = "reorder_only"
	ShapeAddOnly        Shape = "add_only"
	ShapeDenied         Shape = "denied"
)

// Direction says which way a limit tightens.
type Direction string

// The closed direction set.
const (
	DirectionLowerIsStricter  Direction = "lower_is_stricter"
	DirectionHigherIsStricter Direction = "higher_is_stricter"
	DirectionNotALimit        Direction = "not_a_limit"
)

// Bound is an inclusive numeric range. For a limit, the loose end equals
// the static default: Max for lower_is_stricter, Min for higher_is_stricter.
type Bound struct {
	Min, Max float64
}

// Target is one registry row.
type Target struct {
	ID        TargetID
	Paths     []FieldPath
	Tier      ConfigTier
	Areas     []DenylistArea
	Shape     Shape
	Bound     *Bound
	Direction Direction
}

// registry is the validated, immutable target table plus its aliases.
type registry struct {
	targets []Target
	byID    map[TargetID]int
	aliases map[string]TargetID
}

// Targets returns a deep copy of the registry rows in registry order.
func Targets() []Target {
	out := make([]Target, len(defaultRegistry.targets))
	for i, t := range defaultRegistry.targets {
		out[i] = cloneTarget(t)
	}
	return out
}

// cloneTarget copies t so a caller cannot mutate the registry through it.
func cloneTarget(t Target) Target {
	t.Paths = slices.Clone(t.Paths)
	t.Areas = slices.Clone(t.Areas)
	if t.Bound != nil {
		b := *t.Bound
		t.Bound = &b
	}
	return t
}

// ResolveTarget maps an alias or a target id to its TargetID. An unknown
// name is KindNotFound. Every classification calls it before any deny or
// tier lookup.
func ResolveTarget(aliasOrID string) (TargetID, error) {
	return defaultRegistry.resolve(aliasOrID)
}

// resolve is ResolveTarget over r: alias first, then id.
func (r *registry) resolve(name string) (TargetID, error) {
	if id, ok := r.aliases[name]; ok {
		return id, nil
	}
	if _, ok := r.byID[TargetID(name)]; ok {
		return TargetID(name), nil
	}
	return "", cascade.New(cascade.KindNotFound, "learn: unknown learned-config target or alias")
}

// lookup returns the row for a resolved id.
func (r *registry) lookup(id TargetID) (Target, bool) {
	i, ok := r.byID[id]
	if !ok {
		return Target{}, false
	}
	return cloneTarget(r.targets[i]), true
}

// TargetForConfigKey maps a concrete dotted config key to the target that
// governs it: a denied key maps to its area's security target, a learnable
// key to its row. An unregistered key is ("", false, nil); an invalid key
// is KindInvalidInput.
func TargetForConfigKey(dotted string) (TargetID, bool, error) {
	rules, err := MatchDenylist([]FieldPath{FieldPath(dotted)})
	if err != nil {
		return "", false, err
	}
	for _, t := range Targets() {
		if len(rules) > 0 && t.Shape == ShapeDenied && slices.Contains(t.Areas, rules[0].Area) {
			return t.ID, true, nil
		}
		if len(rules) == 0 && t.Shape != ShapeDenied && pathInTarget(t, FieldPath(dotted)) {
			return t.ID, true, nil
		}
	}
	return "", false, nil
}

// pathInTarget reports whether concrete path p is one of t's paths.
func pathInTarget(t Target, p FieldPath) bool {
	for _, m := range t.Paths {
		if matcherMatches(m, p) {
			return true
		}
	}
	return false
}

// TierFor is the tier of a change touching paths. It is pure and takes no
// tier from anywhere: an empty set, an invalid or unlisted path, and any
// path the denylist matches are each security; otherwise the strictest
// tier among the learnable rows the paths fall in.
func TierFor(paths []FieldPath) ConfigTier {
	return defaultRegistry.tierFor(paths)
}

// tierFor is TierFor over r.
func (r *registry) tierFor(paths []FieldPath) ConfigTier {
	if len(paths) == 0 {
		return TierSecurity
	}
	tier := TierSafe
	for _, p := range paths {
		tier = maxTier(tier, r.pathTier(p))
	}
	return tier
}

// pathTier is the tier of one path; anything not positively listed as a
// learnable path is security.
func (r *registry) pathTier(p FieldPath) ConfigTier {
	rules, err := MatchDenylist([]FieldPath{p})
	if err != nil || len(rules) > 0 {
		return TierSecurity
	}
	for _, t := range r.targets {
		if t.Shape != ShapeDenied && t.Tier != TierSecurity && pathInTarget(t, p) {
			return t.Tier
		}
	}
	return TierSecurity
}
