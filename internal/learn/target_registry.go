package learn

// Purpose: the P1 learned-config registry as data: four safe rows, one
//   behavioral row, one security row per denylist area (its paths are that
//   area's deny matchers, derived from DenyRules so the two cannot drift)
//   and three aliases; plus newRegistry, the validation every table passes
//   before the package will use it.
// Inputs: none at runtime (closed Go literals).
// Outputs: defaultRegistry, built once at package init.
// Constraints: frozen by testdata/target_registry_golden.json. P2 appends
//   rows with the golden in the same reviewed change and never repoints an
//   id. Learned values only tighten: the loose end of each limit bound is
//   the static default (ci.local.timeout_seconds: 300, internal/runtime's
//   defaultCITimeoutSeconds). Conductor RetryPolicy constants and
//   DegradeAfter are Go constants with no target: never learnable in P1.
// SPORT: internal.learn.targetRegistry/ADDED (P1-LRN-01).

import (
	"math"
	"slices"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// defaultRegistry is the package's one registry. A table that fails
// newRegistry stops the process at init rather than classifying anything.
var defaultRegistry = mustRegistry(p1Targets(), p1Aliases())

// p1LearnableTargets are the safe and behavioral rows.
func p1LearnableTargets() []Target {
	return []Target{
		{ID: "retrieval.fusion_weights", Paths: []FieldPath{"retrieval.fusion.weights.*"}, Tier: TierSafe,
			Shape: ShapeNumericBounded, Bound: &Bound{Min: 0, Max: 1}, Direction: DirectionNotALimit},
		{ID: "retrieval.fusion_k", Paths: []FieldPath{"retrieval.fusion.k"}, Tier: TierSafe,
			Shape: ShapeNumericBounded, Bound: &Bound{Min: 1, Max: 200}, Direction: DirectionNotALimit},
		{ID: "ci.local_timeout", Paths: []FieldPath{"ci.local.timeout_seconds"}, Tier: TierSafe,
			Shape: ShapeNumericBounded, Bound: &Bound{Min: 60, Max: 300}, Direction: DirectionLowerIsStricter},
		{ID: "conductor.lane_order", Paths: []FieldPath{"conductor.lane_order"}, Tier: TierSafe,
			Shape: ShapeReorderOnly, Direction: DirectionNotALimit},
		{ID: "ci.local_steps", Paths: []FieldPath{"ci.local.lint", "ci.local.test", "ci.local.build"},
			Tier: TierBehavioral, Shape: ShapeAddOnly, Direction: DirectionNotALimit},
	}
}

// p1Targets is the full P1 table: the learnable rows, then one security row
// per area in DenylistAreas order. A security row's id is its area name
// with the first "_" turned into "." (auth_rules -> auth.rules).
func p1Targets() []Target {
	out := p1LearnableTargets()
	rules := DenyRules()
	for _, area := range DenylistAreas() {
		var paths []FieldPath
		for _, r := range rules {
			if r.Area == area {
				paths = append(paths, r.Matcher)
			}
		}
		out = append(out, Target{
			ID: TargetID(strings.Replace(string(area), "_", ".", 1)), Paths: paths, Tier: TierSecurity,
			Areas: []DenylistArea{area}, Shape: ShapeDenied, Direction: DirectionNotALimit,
		})
	}
	return out
}

// p1Aliases is the closed alias table. An alias is resolved before every
// lookup, so a denied target reached through one is still denied.
func p1Aliases() map[string]TargetID {
	return map[string]TargetID{
		"retrieval.weights": "retrieval.fusion_weights",
		"ci.steps":          "ci.local_steps",
		"auth_rules":        "auth.rules",
	}
}

// mustRegistry builds a registry or stops the process.
func mustRegistry(targets []Target, aliases map[string]TargetID) *registry {
	r, err := newRegistry(targets, aliases)
	if err != nil {
		panic(err)
	}
	return r
}

// newRegistry validates every row and alias and returns the registry, or
// KindInvalidInput naming the first defect.
func newRegistry(targets []Target, aliases map[string]TargetID) (*registry, error) {
	r := &registry{byID: make(map[TargetID]int, len(targets)), aliases: make(map[string]TargetID, len(aliases))}
	for i, t := range targets {
		if err := validateTarget(t); err != nil {
			return nil, err
		}
		if _, dup := r.byID[t.ID]; dup {
			return nil, cascade.Newf(cascade.KindInvalidInput, "learn: target id %q is declared twice", t.ID)
		}
		r.byID[t.ID] = i
		r.targets = append(r.targets, cloneTarget(t))
	}
	for alias, id := range aliases {
		if _, shadow := r.byID[TargetID(alias)]; shadow || alias == "" {
			return nil, cascade.Newf(cascade.KindInvalidInput, "learn: alias %q is empty or shadows a target id", alias)
		}
		if _, ok := r.byID[id]; !ok {
			return nil, cascade.Newf(cascade.KindInvalidInput, "learn: alias %q points at no target", alias)
		}
		r.aliases[alias] = id
	}
	return r, nil
}

// validateTarget checks one row: id syntax, closed tier, areas, shape and
// direction, valid matchers, a consistent bound, and the tier/area split.
func validateTarget(t Target) error {
	why := targetDefect(t)
	if why == "" {
		return nil
	}
	return cascade.Newf(cascade.KindInvalidInput, "learn: target %q: %s", t.ID, why)
}

// targetDefect names the first defect of t, or "".
func targetDefect(t Target) string {
	if !targetIDPattern.MatchString(string(t.ID)) {
		return "id breaks the TargetID syntax"
	}
	if _, err := ParseConfigTier(string(t.Tier)); err != nil || len(t.Paths) == 0 {
		return "needs a closed tier and at least one path"
	}
	if !slices.Contains([]Direction{DirectionLowerIsStricter, DirectionHigherIsStricter, DirectionNotALimit}, t.Direction) {
		return "unknown direction"
	}
	for _, a := range t.Areas {
		if _, err := ParseDenylistArea(string(a)); err != nil {
			return "unknown denylist area"
		}
	}
	if why := pathDefect(t); why != "" {
		return why
	}
	return shapeDefect(t)
}

// pathDefect checks each matcher, and that no learnable path is one the
// denylist would match (a denied key can never sit in a safe row).
func pathDefect(t Target) string {
	for _, p := range t.Paths {
		if validateMatcher(p) != nil {
			return "a path is not a valid matcher"
		}
		if t.Shape == ShapeDenied {
			continue
		}
		probe := FieldPath(strings.ReplaceAll(string(p), "*", "probe"))
		if rules, err := MatchDenylist([]FieldPath{probe}); err != nil || len(rules) > 0 {
			return "a learnable path is matched by the denylist"
		}
	}
	return ""
}

// shapeDefect ties shape, tier, areas and bound together: denied rows are
// security with areas and no bound; learnable rows carry no areas and are
// never security; only numeric rows carry a finite, ordered bound.
func shapeDefect(t Target) string {
	switch t.Shape {
	case ShapeDenied:
		if t.Tier != TierSecurity || len(t.Areas) == 0 || t.Bound != nil {
			return "a denied row is security, names its areas and has no bound"
		}
		return ""
	case ShapeNumericBounded:
		b := t.Bound
		if b == nil || math.IsNaN(b.Min) || math.IsNaN(b.Max) || math.IsInf(b.Min, 0) || math.IsInf(b.Max, 0) || b.Min > b.Max {
			return "a numeric row needs a finite bound with min <= max"
		}
	case ShapeReorderOnly, ShapeAddOnly:
		if t.Bound != nil || t.Direction != DirectionNotALimit {
			return "a reorder or add row has no bound and is not a limit"
		}
	default:
		return "unknown shape"
	}
	if t.Tier == TierSecurity || len(t.Areas) > 0 {
		return "a learnable row is safe or behavioral and names no area"
	}
	return ""
}
