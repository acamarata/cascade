// Purpose: the R-21.32 VERBATIM multiplier table (nine stored rows x five
//   stored columns, keyed by the R-21.30 lane classes and the AO/S-79.T2
//   mode enum) and the five-row utility-weight table, both as package
//   data with column/row aliases, plus their JSON golden
//   (testdata/goldens/mode_tables.json). Nothing here is tuned or
//   invented -- every cell is copied from the ruling.
//
// Inputs: a topology.LaneClass (or one of the alias/derived keys this
//   file also resolves) and a Mode.
// Outputs: a multiplier float64 or a Weights struct, or
//   ErrUnknownLaneClass/ErrUnknownMode for a key outside the resolvable
//   set.
// Constraints: the package-level table vars are never mutated after
//   init -- tables_override.go's ApplyOverrides returns a NEW Tables
//   value rather than editing these in place.
//
// SPORT: fleet/economics/tables/ADD (P1-E41-W9-S79-T2).

package economics

import (
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/pkg/cascade"
)

// laneRow is one row of the R-21.32 multiplier table: the five stored
// mode-column values, in the fixed order plan, build, crunch, verify,
// incident.
type laneRow struct {
	Plan, Build, Crunch, Verify, Incident float64
}

// laneRows is the R-21.32 VERBATIM multiplier table's nine stored rows,
// keyed by one representative LaneClass per row. Rows that R-21.32
// states share one row (executive/executive-xhigh; api-paid/api-free/
// local/unranked) are expanded into laneRowAliases below rather than
// duplicated here.
var laneRows = map[topology.LaneClass]laneRow{
	topology.LaneClassExecutive:       {Plan: 0.60, Build: 2.5, Crunch: 10.0, Verify: 0.80, Incident: 0.30},
	topology.LaneClassCritic:          {Plan: 0.80, Build: 1.8, Crunch: 8.0, Verify: 1.0, Incident: 0.40},
	topology.LaneClassSpecialist:      {Plan: 1.1, Build: 2.0, Crunch: 10.0, Verify: 0.60, Incident: 0.25},
	topology.LaneClassAdvisor:         {Plan: 0.9, Build: 1.5, Crunch: 8.0, Verify: 0.75, Incident: 0.40},
	topology.LaneClassLead:            {Plan: 1.1, Build: 0.65, Crunch: 2.5, Verify: 1.0, Incident: 0.70},
	topology.LaneClassWorkerDedicated: {Plan: 1.0, Build: 0.65, Crunch: 1.0, Verify: 0.9, Incident: 0.60},
	topology.LaneClassAPIPaid:         {Plan: 0.8, Build: 0.8, Crunch: 0.60, Verify: 0.8, Incident: 0.65},
	topology.LaneClassAPIBatch:        {Plan: 1.5, Build: 1.0, Crunch: 0.35, Verify: 1.2, Incident: 2.00},
	topology.LaneClassPoolCheap:       {Plan: 1.0, Build: 0.8, Crunch: 0.50, Verify: 0.9, Incident: 1.0},
}

// laneRowAliases maps every remaining resolvable lane-class key (a real
// LaneClass sharing another row's values, a row-aliased LaneClass, or the
// R-21.45 derived executive-overflow key) to the laneRows entry it reads.
// Together with laneRows's nine stored keys, this resolves all seventeen
// R-21.30 lane classes plus executive-overflow -- eighteen keys total.
var laneRowAliases = map[topology.LaneClass]topology.LaneClass{
	// Row alias: executive-xhigh shares the executive row (R-21.32).
	topology.LaneClassExecutiveXHigh: topology.LaneClassExecutive,
	// Row alias: api-free, local and unranked share the api-paid row.
	topology.LaneClassAPIFree:  topology.LaneClassAPIPaid,
	topology.LaneClassLocal:    topology.LaneClassAPIPaid,
	topology.LaneClassUnranked: topology.LaneClassAPIPaid,
	// R-21.32 row aliases: deep, harness-sub, worker-fast and
	// pool-premium use the lead row.
	topology.LaneClassDeep:        topology.LaneClassLead,
	topology.LaneClassHarnessSub:  topology.LaneClassLead,
	topology.LaneClassWorkerFast:  topology.LaneClassLead,
	topology.LaneClassPoolPremium: topology.LaneClassLead,
	// R-21.45 derived class: executive-overflow uses the
	// executive/executive-xhigh row. Reuses accounts.go's
	// executiveOverflowClass sentinel (the ONE place that string is
	// declared in this package) rather than a second constant.
	topology.LaneClass(executiveOverflowClass): topology.LaneClassExecutive,
}

// ErrUnknownLaneClass is returned by ModeMultiplier for a class outside
// the eighteen resolvable keys (the seventeen R-21.30 lane classes plus
// the R-21.45 derived executive-overflow).
var ErrUnknownLaneClass = cascade.New(cascade.KindInvalidInput, "economics: unknown lane class")

// resolveLaneRow returns the laneRow class resolves to, following at most
// one alias hop.
func resolveLaneRow(class topology.LaneClass) (laneRow, error) {
	if row, ok := laneRows[class]; ok {
		return row, nil
	}
	if target, ok := laneRowAliases[class]; ok {
		if row, ok := laneRows[target]; ok {
			return row, nil
		}
	}
	return laneRow{}, cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownLaneClass, "economics: unknown lane class %q", class)
}

// modeColumn resolves a Mode to the laneRow field it reads and the
// utilityWeights row it reads, applying the R-21.32 column aliases
// discover=plan, integrate=build, release=verify.
func modeColumn(m Mode) (Mode, error) {
	if !m.Valid() {
		return "", cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownMode, "economics: unknown scheduler mode %q", m)
	}
	switch m {
	case ModeDiscover:
		return ModePlan, nil
	case ModeIntegrate:
		return ModeBuild, nil
	case ModeRelease:
		return ModeVerify, nil
	case ModePlan, ModeBuild, ModeCrunch, ModeVerify, ModeIncident:
		return m, nil
	}
	return "", cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownMode, "economics: unknown scheduler mode %q", m)
}

// ModeMultiplier returns P_mode for class and m: the R-21.32 multiplier
// table cell, resolving both the lane-class row aliases (including the
// R-21.45 derived executive-overflow) and the mode column aliases
// (discover=plan, integrate=build, release=verify). Returns
// ErrUnknownLaneClass for a class outside the eighteen resolvable keys,
// or ErrUnknownMode for a mode outside the eight-member enum.
func ModeMultiplier(class topology.LaneClass, m Mode) (float64, error) {
	row, err := resolveLaneRow(class)
	if err != nil {
		return 0, err
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

// Weights is the R-21.32 five-term utility-weight row for one mode, in
// the fixed order quality, scarcity, diversity, latency, reliability.
// UtilityWeights returns these five R-21.32 rows, but the utility itself
// has SIX terms (§E R-21.120/R-21.135 supersede R-21.46): the sixth term
// reuses Reliability as rw and introduces no seventh weight field here.
type Weights struct {
	Quality     float64
	Scarcity    float64
	Diversity   float64
	Latency     float64
	Reliability float64
}

// utilityWeights is the R-21.32 VERBATIM five-row weight table, keyed by
// the five stored modes; discover/integrate/release alias to
// plan/build/verify respectively (modeColumn).
var utilityWeights = map[Mode]Weights{
	ModePlan:     {Quality: 5.0, Scarcity: 1.4, Diversity: 1.8, Latency: 0.3, Reliability: 1.0},
	ModeBuild:    {Quality: 5.0, Scarcity: 1.8, Diversity: 1.0, Latency: 0.5, Reliability: 1.0},
	ModeCrunch:   {Quality: 4.0, Scarcity: 2.6, Diversity: 0.6, Latency: 0.7, Reliability: 1.0},
	ModeVerify:   {Quality: 5.0, Scarcity: 1.0, Diversity: 2.4, Latency: 0.3, Reliability: 1.0},
	ModeIncident: {Quality: 6.0, Scarcity: 0.4, Diversity: 1.2, Latency: 1.0, Reliability: 1.5},
}

// UtilityWeights returns the R-21.32 weight row for m, resolving the
// discover=plan, integrate=build, release=verify column aliases. Returns
// ErrUnknownMode for a mode outside the eight-member enum.
func UtilityWeights(m Mode) (Weights, error) {
	col, err := modeColumn(m)
	if err != nil {
		return Weights{}, err
	}
	w, ok := utilityWeights[col]
	if !ok {
		return Weights{}, cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownMode, "economics: unknown scheduler mode %q", m)
	}
	return w, nil
}
