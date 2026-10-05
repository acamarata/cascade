package learn

// Purpose: ClassifyChange, the one classification rule for a learned change
//   (P1-LRN-02 and P1-LRN-03 reuse it; nothing else computes a tier).
// Inputs: a target alias or id and one Change.
// Outputs: a Classification: resolved target, paths, tier, denylist areas
//   and whether the change leaves the target's tighten-only envelope.
// Constraints: the alias resolves before every deny and tier lookup. The
//   change must stay inside the resolved target's paths. Learned values only
//   tighten: a value outside the bound (whose loose end is the static
//   default) or an op the target's shape does not admit sets LoosensBound
//   and makes the change security, so it is never applied (C11). An unknown
//   op, an invalid path and an unreadable numeric or reorder value are
//   KindInvalidInput. An add_step outside KnownStepValues is security.
// SPORT: internal.learn.ClassifyChange/ADDED (P1-LRN-01).

import (
	"math"
	"regexp"
	"slices"
	"strconv"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ChangeOp is what a change does to its path.
type ChangeOp string

// The closed op set.
const (
	OpSet     ChangeOp = "set"
	OpAddStep ChangeOp = "add_step"
	OpReorder ChangeOp = "reorder"
)

// Change is one proposed edit. Value is a TOML literal.
type Change struct {
	Op    ChangeOp
	Path  FieldPath
	Value string
}

// Classification is ClassifyChange's verdict. LoosensBound reports that the
// change leaves the target's tighten-only envelope: a value past the bound
// or an op the target's shape does not admit.
type Classification struct {
	Target       TargetID
	Paths        []FieldPath
	Tier         ConfigTier
	Areas        []DenylistArea
	LoosensBound bool
}

// classifierView is the set of lookups ClassifyChange makes, in the order it
// makes them. defaultView binds it to the package's exported functions.
type classifierView interface {
	resolve(aliasOrID string) (TargetID, error)
	lookup(id TargetID) (Target, bool)
	denyMatch(paths []FieldPath) ([]DenyRule, error)
	tierFor(paths []FieldPath) ConfigTier
}

// defaultView answers through ResolveTarget, MatchDenylist and TierFor.
type defaultView struct{}

func (defaultView) resolve(n string) (TargetID, error) { return ResolveTarget(n) }
func (defaultView) lookup(id TargetID) (Target, bool) {
	resolved, err := ResolveTarget(string(id))
	if err != nil {
		return Target{}, false
	}
	return defaultRegistry.lookup(resolved)
}
func (defaultView) denyMatch(p []FieldPath) ([]DenyRule, error) { return MatchDenylist(p) }
func (defaultView) tierFor(p []FieldPath) ConfigTier            { return TierFor(p) }

// ClassifyChange resolves target (alias first), checks ch against the
// resolved row, and returns its classification.
func ClassifyChange(target string, ch Change) (Classification, error) {
	return classifyWith(defaultView{}, target, ch)
}

// classifyWith is ClassifyChange over v.
func classifyWith(v classifierView, target string, ch Change) (Classification, error) {
	id, err := v.resolve(target)
	if err != nil {
		return Classification{}, err
	}
	t, ok := v.lookup(id)
	if !ok {
		return Classification{}, cascade.New(cascade.KindNotFound, "learn: resolved target has no registry row")
	}
	if err := checkChangeShape(t, ch); err != nil {
		return Classification{}, err
	}
	loosens, err := loosensEnvelope(t, ch)
	if err != nil {
		return Classification{}, err
	}
	paths := []FieldPath{ch.Path}
	rules, err := v.denyMatch(paths)
	if err != nil {
		return Classification{}, err
	}
	areas := unionAreas(t.Areas, rules)
	tier := maxTier(t.Tier, v.tierFor(paths))
	if len(areas) > 0 || loosens {
		tier = TierSecurity
	}
	return Classification{Target: id, Paths: paths, Tier: tier, Areas: areas, LoosensBound: loosens}, nil
}

// checkChangeShape refuses an unknown op, an invalid path and a path outside
// the target's paths.
func checkChangeShape(t Target, ch Change) error {
	if !slices.Contains([]ChangeOp{OpSet, OpAddStep, OpReorder}, ch.Op) {
		return cascade.New(cascade.KindInvalidInput, "learn: change op must be set, add_step or reorder")
	}
	if err := validateConcretePath(ch.Path); err != nil {
		return err
	}
	if !pathInTarget(t, ch.Path) {
		return cascade.New(cascade.KindInvalidInput, "learn: change path is outside the resolved target's paths")
	}
	return nil
}

// loosensEnvelope reports whether ch leaves t's tighten-only envelope. The
// one op each shape admits is checked on its value; any other op loosens.
// A denied row reports false: its areas already make the change security.
func loosensEnvelope(t Target, ch Change) (bool, error) {
	switch t.Shape {
	case ShapeDenied:
		return false, nil
	case ShapeNumericBounded:
		if ch.Op != OpSet {
			return true, nil
		}
		return outsideBound(t.Bound, ch.Value)
	case ShapeReorderOnly:
		if ch.Op != OpReorder {
			return true, nil
		}
		return false, requireMatch(stringArrayLiteral, ch.Value, "a TOML array of lane names")
	case ShapeAddOnly:
		if ch.Op != OpAddStep {
			return true, nil
		}
		for _, value := range KnownStepValues() {
			if ch.Value == strconv.Quote(value) {
				return false, nil
			}
		}
		return true, nil
	default:
		return true, nil
	}
}

var (
	// numberLiteral is a plain TOML decimal integer or float.
	numberLiteral = regexp.MustCompile(`^[+-]?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)
	// stringArrayLiteral is a non-empty TOML array of quoted lane names.
	stringArrayLiteral = regexp.MustCompile(`^\[\s*"[a-z0-9_-]+"(\s*,\s*"[a-z0-9_-]+")*\s*,?\s*\]$`)
)

// requireMatch is KindInvalidInput unless value matches re.
func requireMatch(re *regexp.Regexp, value, want string) error {
	if !re.MatchString(value) {
		return cascade.Newf(cascade.KindInvalidInput, "learn: change value must be %s", want)
	}
	return nil
}

// outsideBound parses value as a finite TOML number and reports whether it
// lies outside b (inclusive). A missing bound is outside by definition.
func outsideBound(b *Bound, value string) (bool, error) {
	if err := requireMatch(numberLiteral, value, "a TOML decimal number"); err != nil {
		return false, err
	}
	v, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return false, cascade.New(cascade.KindInvalidInput, "learn: change value must be a finite number")
	}
	if b == nil {
		return true, nil
	}
	return v < b.Min || v > b.Max, nil
}

// unionAreas merges a target's areas with the matched rules' areas, in
// DenylistAreas order, without duplicates.
func unionAreas(base []DenylistArea, rules []DenyRule) []DenylistArea {
	var out []DenylistArea
	for _, a := range DenylistAreas() {
		hit := slices.Contains(base, a)
		for _, r := range rules {
			hit = hit || r.Area == a
		}
		if hit {
			out = append(out, a)
		}
	}
	return out
}
