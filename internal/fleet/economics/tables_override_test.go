package economics

import (
	"errors"
	"math"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/topology"
)

func TestApplyOverridesReturnsNewValueLeavesPackageDataUnchanged(t *testing.T) {
	base := NewTables()
	before, err := base.Multiplier(topology.LaneClassLead, ModeBuild)
	if err != nil {
		t.Fatal(err)
	}
	ov := Overrides{
		Multipliers: map[string]map[string]float64{"lead": {"build": 9.99}},
	}
	out, err := ApplyOverrides(base, ov)
	if err != nil {
		t.Fatalf("ApplyOverrides: %v", err)
	}
	got, err := out.Multiplier(topology.LaneClassLead, ModeBuild)
	if err != nil {
		t.Fatal(err)
	}
	if got != 9.99 {
		t.Errorf("overridden Multiplier(lead, build) = %v, want 9.99", got)
	}
	// base and the package data must be unmodified.
	afterBase, err := base.Multiplier(topology.LaneClassLead, ModeBuild)
	if err != nil {
		t.Fatal(err)
	}
	if afterBase != before {
		t.Errorf("ApplyOverrides mutated base: %v -> %v", before, afterBase)
	}
	pkgVal, err := ModeMultiplier(topology.LaneClassLead, ModeBuild)
	if err != nil {
		t.Fatal(err)
	}
	if pkgVal != before {
		t.Errorf("ApplyOverrides mutated package data: %v -> %v", before, pkgVal)
	}
}

func TestApplyOverridesWeights(t *testing.T) {
	base := NewTables()
	out, err := ApplyOverrides(base, Overrides{
		Weights: map[string]map[string]float64{"plan": {"quality": 42}},
	})
	if err != nil {
		t.Fatalf("ApplyOverrides: %v", err)
	}
	w, err := out.Weights(ModePlan)
	if err != nil {
		t.Fatal(err)
	}
	if w.Quality != 42 {
		t.Errorf("overridden Weights(plan).Quality = %v, want 42", w.Quality)
	}
	if w.Scarcity != 1.4 {
		t.Errorf("overridden Weights(plan).Scarcity = %v, want the unmodified 1.4", w.Scarcity)
	}
}

func TestApplyOverridesUnknownLaneClass(t *testing.T) {
	_, err := ApplyOverrides(NewTables(), Overrides{
		Multipliers: map[string]map[string]float64{"nonexistent": {"build": 1}},
	})
	if !errors.Is(err, ErrUnknownLaneClass) {
		t.Errorf("error = %v, want ErrUnknownLaneClass", err)
	}
}

func TestApplyOverridesUnknownModeInMultiplier(t *testing.T) {
	_, err := ApplyOverrides(NewTables(), Overrides{
		Multipliers: map[string]map[string]float64{"lead": {"nonexistent": 1}},
	})
	if !errors.Is(err, ErrUnknownMode) {
		t.Errorf("error = %v, want ErrUnknownMode", err)
	}
}

func TestApplyOverridesUnknownModeInWeights(t *testing.T) {
	_, err := ApplyOverrides(NewTables(), Overrides{
		Weights: map[string]map[string]float64{"nonexistent": {"quality": 1}},
	})
	if !errors.Is(err, ErrUnknownMode) {
		t.Errorf("error = %v, want ErrUnknownMode", err)
	}
}

func TestApplyOverridesUnknownWeightName(t *testing.T) {
	_, err := ApplyOverrides(NewTables(), Overrides{
		Weights: map[string]map[string]float64{"plan": {"nonexistent": 1}},
	})
	if err == nil {
		t.Fatal("want a typed validation error for an unknown weight name")
	}
}

func TestApplyOverridesNonFiniteValue(t *testing.T) {
	_, err := ApplyOverrides(NewTables(), Overrides{
		Multipliers: map[string]map[string]float64{"lead": {"build": math.Inf(1)}},
	})
	if err == nil {
		t.Fatal("want a typed validation error for a non-finite value")
	}
}

func TestApplyOverridesResolvesRowAlias(t *testing.T) {
	out, err := ApplyOverrides(NewTables(), Overrides{
		Multipliers: map[string]map[string]float64{"deep": {"plan": 3.5}},
	})
	if err != nil {
		t.Fatalf("ApplyOverrides: %v", err)
	}
	// deep aliases to lead, so overriding deep must also move lead's plan
	// cell (they are the same stored row).
	got, err := out.Multiplier(topology.LaneClassLead, ModePlan)
	if err != nil {
		t.Fatal(err)
	}
	if got != 3.5 {
		t.Errorf("Multiplier(lead, plan) after overriding deep = %v, want 3.5", got)
	}
}
