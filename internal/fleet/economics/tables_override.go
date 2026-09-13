// Purpose: Overrides, the decoded [fleet.economics] hot-override value
//   (08-INIT-CONFIG-SPEC.md §3), and ApplyOverrides, which merges it onto
//   a base Tables value to produce a NEW Tables -- the package-level
//   laneRows/utilityWeights data is never mutated in place.
//
// Inputs: an Overrides value (typically decoded by the daemon composition
//   root from internal/runtime.Config's raw [fleet.economics] tree) and a
//   base Tables snapshot (NewTables()).
// Outputs: a new Tables value, or a typed validation error for an unknown
//   lane class, unknown mode, unknown weight name, or non-finite value --
//   never a silently dropped entry (06-FORGE-SPEC.md §5.15).
//
// Constraints: pure, no I/O -- TOML decoding itself is
//   internal/runtime's job (config_economics.go); this file only
//   validates and applies the already-decoded shape.
//
// SPORT: fleet/economics/tables-override/ADD (P1-E41-W9-S79-T2).

package economics

import (
	"math"

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Tables is an immutable snapshot of the multiplier and weight tables:
// the package data (NewTables) or the package data with zero or more
// [fleet.economics] overrides applied (ApplyOverrides).
type Tables struct {
	rows    map[topology.LaneClass]laneRow
	weights map[Mode]Weights
}

// NewTables returns a Tables snapshot of the unmodified R-21.32 package
// data -- a defensive copy, so mutating the returned value never touches
// the package-level laneRows/utilityWeights vars.
func NewTables() Tables {
	rows := make(map[topology.LaneClass]laneRow, len(laneRows))
	for k, v := range laneRows {
		rows[k] = v
	}
	weights := make(map[Mode]Weights, len(utilityWeights))
	for k, v := range utilityWeights {
		weights[k] = v
	}
	return Tables{rows: rows, weights: weights}
}

// Multiplier resolves class and m against t, following the same
// row/column alias rules ModeMultiplier does against the package data.
func (t Tables) Multiplier(class topology.LaneClass, m Mode) (float64, error) {
	target := class
	if _, ok := t.rows[class]; !ok {
		alias, ok := laneRowAliases[class]
		if !ok {
			return 0, cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownLaneClass, "economics: unknown lane class %q", class)
		}
		target = alias
	}
	row, ok := t.rows[target]
	if !ok {
		return 0, cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownLaneClass, "economics: unknown lane class %q", class)
	}
	col, err := modeColumn(m)
	if err != nil {
		return 0, err
	}
	switch col {
	case ModePlan:
		return row.Plan, nil
	case ModeBuild:
		return row.Build, nil
	case ModeCrunch:
		return row.Crunch, nil
	case ModeVerify:
		return row.Verify, nil
	case ModeIncident:
		return row.Incident, nil
	case ModeDiscover, ModeIntegrate, ModeRelease:
		// modeColumn never returns an alias mode; unreachable in
		// practice, kept only so this switch stays exhaustive over Mode.
	}
	return 0, cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownMode, "economics: unknown scheduler mode %q", m)
}

// Weights resolves m's weight row against t.
func (t Tables) Weights(m Mode) (Weights, error) {
	col, err := modeColumn(m)
	if err != nil {
		return Weights{}, err
	}
	w, ok := t.weights[col]
	if !ok {
		return Weights{}, cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownMode, "economics: unknown scheduler mode %q", m)
	}
	return w, nil
}

// Overrides is the decoded [fleet.economics] block (08-INIT-CONFIG-SPEC.md
// §3): the two hot scalars plus the multiplier/weight override
// sub-tables, keyed exactly on the two tables' own axes (lane class then
// mode; mode then weight name) -- no third table.
type Overrides struct {
	// ExplorationRate overrides the default 0.05, read by the AR/S-85.T2
	// learner. Only applied when Set is true (ExplorationRateSet).
	ExplorationRate    float64
	ExplorationRateSet bool
	// AllowPremiumInCrunch overrides the default false, read by the
	// AO/S-79.T3 eligibility filter. Only applied when
	// AllowPremiumInCrunchSet is true.
	AllowPremiumInCrunch    bool
	AllowPremiumInCrunchSet bool
	// Multipliers is keyed by lane class then mode:
	// `[fleet.economics.multipliers."<lane-class>"] <mode> = <float>`.
	Multipliers map[string]map[string]float64
	// Weights is keyed by mode then weight name:
	// `[fleet.economics.weights."<mode>"] quality|scarcity|diversity|
	// latency|reliability = <float>`.
	Weights map[string]map[string]float64
}

// weightFieldSetters maps a weight-name key to the Weights field it sets.
var weightFieldSetters = map[string]func(*Weights, float64){
	"quality":     func(w *Weights, v float64) { w.Quality = v },
	"scarcity":    func(w *Weights, v float64) { w.Scarcity = v },
	"diversity":   func(w *Weights, v float64) { w.Diversity = v },
	"latency":     func(w *Weights, v float64) { w.Latency = v },
	"reliability": func(w *Weights, v float64) { w.Reliability = v },
}

// multiplierFieldSetters maps a mode-column key to the laneRow field it
// sets. Only the five stored columns are override targets -- an override
// keyed by an ALIAS mode name (discover/integrate/release) is refused by
// applyMultiplierOverrides below, since it would otherwise silently
// shadow the column it aliases without the caller realising it changed
// two modes at once.
var multiplierFieldSetters = map[Mode]func(*laneRow, float64){
	ModePlan:     func(r *laneRow, v float64) { r.Plan = v },
	ModeBuild:    func(r *laneRow, v float64) { r.Build = v },
	ModeCrunch:   func(r *laneRow, v float64) { r.Crunch = v },
	ModeVerify:   func(r *laneRow, v float64) { r.Verify = v },
	ModeIncident: func(r *laneRow, v float64) { r.Incident = v },
}

// ApplyOverrides returns a NEW Tables value with ov's multiplier/weight
// overrides applied on top of base, leaving base itself (and the package
// data NewTables snapshots from) unmodified. An unknown lane class,
// unknown mode, unknown weight name, or non-finite value is a typed
// validation error rather than a silently dropped entry.
func ApplyOverrides(base Tables, ov Overrides) (Tables, error) {
	out := Tables{
		rows:    make(map[topology.LaneClass]laneRow, len(base.rows)),
		weights: make(map[Mode]Weights, len(base.weights)),
	}
	for k, v := range base.rows {
		out.rows[k] = v
	}
	for k, v := range base.weights {
		out.weights[k] = v
	}
	if err := applyMultiplierOverrides(out.rows, ov.Multipliers); err != nil {
		return Tables{}, err
	}
	if err := applyWeightOverrides(out.weights, ov.Weights); err != nil {
		return Tables{}, err
	}
	return out, nil
}

// isFinite reports whether v is neither NaN nor +/-Inf.
func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

// applyMultiplierOverrides validates and applies ov's multiplier
// sub-table onto rows in place.
func applyMultiplierOverrides(rows map[topology.LaneClass]laneRow, ov map[string]map[string]float64) error {
	for classKey, byMode := range ov {
		class := topology.LaneClass(classKey)
		target := class
		if _, ok := rows[class]; !ok {
			alias, ok := laneRowAliases[class]
			if !ok {
				return cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownLaneClass, "economics: [fleet.economics.multipliers.%q]: unknown lane class", classKey)
			}
			target = alias
		}
		row := rows[target]
		for modeKey, v := range byMode {
			if !isFinite(v) {
				return cascade.Newf(cascade.KindInvalidInput, "economics: [fleet.economics.multipliers.%q].%s: value %v is not finite", classKey, modeKey, v)
			}
			setter, ok := multiplierFieldSetters[Mode(modeKey)]
			if !ok {
				return cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownMode, "economics: [fleet.economics.multipliers.%q]: unknown mode %q", classKey, modeKey)
			}
			setter(&row, v)
		}
		rows[target] = row
	}
	return nil
}

// applyWeightOverrides validates and applies ov's weight sub-table onto
// weights in place.
func applyWeightOverrides(weights map[Mode]Weights, ov map[string]map[string]float64) error {
	for modeKey, byName := range ov {
		col, err := modeColumn(Mode(modeKey))
		if err != nil {
			return cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownMode, "economics: [fleet.economics.weights.%q]: unknown mode", modeKey)
		}
		w := weights[col]
		for nameKey, v := range byName {
			if !isFinite(v) {
				return cascade.Newf(cascade.KindInvalidInput, "economics: [fleet.economics.weights.%q].%s: value %v is not finite", modeKey, nameKey, v)
			}
			setter, ok := weightFieldSetters[nameKey]
			if !ok {
				return cascade.Newf(cascade.KindInvalidInput, "economics: [fleet.economics.weights.%q]: unknown weight name %q", modeKey, nameKey)
			}
			setter(&w, v)
		}
		weights[col] = w
	}
	return nil
}
