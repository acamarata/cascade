// Purpose (this file): the vendor-neutral capability priors table
// (task_class x tier -> Beta(2,2) seed score) and the R-21.175 expected-time
// jump-rule constants, the ONE home for both per R-16.71/R-16.37 (this
// ticket, P1-E31-W6-S63-T4).
//
// Inputs: none (static data, transcribed verbatim from R-16.37 §Fleet
// capacity).
// Outputs: Priors (the table), ReworkCyclesEst, JumpHysteresisFactor,
// PReworkFromScore -- consumed by S-63.T2's tier-policy engine
// (expected_time.go) and S-64.T2's capability scoring (internal/learn),
// neither of which has landed yet in this tree (see this file's CONTRACT
// DEVIATION note below).
//
// Constraints: init() panics if any cell is missing or out of [0, 1], or if
// JumpHysteresisFactor is outside (0, 1], or ReworkCyclesEst is below 1 --
// fail closed on a corrupt table rather than routing real work against an
// optimistic guess. TaskClass reuses conductor.TaskClass (the ONE closed
// task-class vocabulary owner per R-16.79/R-14.38) rather than declaring a
// second, competing task-class enum here; Tier is a new closed vocabulary,
// since nothing in the tree names the tier-0/tier-1/tier-2/local execution-
// tier concept yet (S-63.T2's tier-policy engine, which will consume this
// table, has not landed).
//
// CONTRACT DEVIATION (consumers, recorded, not papered over). This
// ticket's full_desc says "S-63.T2's expected_time.go reads these symbols"
// and "internal/learn (S-64.T2) imports it via PriorAlphaBeta." Neither
// S-63.T2 (internal/fleet/capacity/policy.go, tier_policy.go,
// expected_time.go) nor S-64.T2 (internal/learn) exists in this tree as of
// this ticket -- only this ticket's own dependency, S-63.T1 (the
// FleetSnapshot/Compositor this package already carries), has landed. This
// file ships the table and constants as specified; Priors, ReworkCyclesEst,
// JumpHysteresisFactor are exercised by this file's own init() validation
// (a genuine production reference, not a test-only one), but
// PReworkFromScore has no such validation use and no other production
// caller yet -- recorded honestly in internal/build/testonly-allow.json
// under this ticket, retired when S-63.T2 lands.
//
// AUTHORING IS A PER-MODEL CAPABILITY (R-21.170). The local tier's 0.00
// cells for code/reason/review/arbitrate below are a scoring FLOOR, never
// the enabling mechanism: a local lane becomes a candidate for an
// authoring task class only when "authoring" is present in that model's
// advertised capability set, and "authoring" is advertised only when the
// named qualification fixture (providers/ollama/testdata/qualification/)
// passes for that model id -- AD/S-62.T1 owns the capability, the fixture,
// and the per-model record (recorded in config, never per driver).
// AI/S-71.T3 carries qualified_for_authoring, and every capability
// advertisement, on its hard denylist, so no learned proposal can flip
// them without elevation. Capability filtering therefore happens BEFORE
// scoring: a model lacking "authoring" is never ranked at all for an
// authoring task class, so a config flag alone can never admit an
// unqualified local lane to a code job -- the zero below is a floor on an
// already-filtered candidate set, not the filter itself.
//
// SPORT: fleet/capacity/priors (ADD, P1-E31-W6-S63-T4).

package capacity

import (
	"fmt"

	"github.com/acamarata/cascade/internal/conductor"
)

// Tier is the closed tier-0/tier-1/tier-2/local execution-tier vocabulary
// R-16.37's jump rule and this priors table are keyed on. No permissive
// zero value: the empty string is not a member (Valid rejects it), matching
// every other closed vocabulary in this tree's fail-closed convention.
type Tier string

// The four closed Tier members, cheapest/most-available to most
// preserved, per R-16.37's tier-walk order (tier-2 -> tier-1 -> tier-0)
// plus the local-execution tier.
const (
	TierZero  Tier = "tier-0"
	TierOne   Tier = "tier-1"
	TierTwo   Tier = "tier-2"
	TierLocal Tier = "local"
)

// Valid reports whether t is one of the four declared members.
func (t Tier) Valid() bool {
	switch t {
	case TierZero, TierOne, TierTwo, TierLocal:
		return true
	}
	return false
}

// priorTaskClasses is the seven conductor.TaskClass members this priors
// table carries a row for -- a subset of conductor's full nine-row §5.16
// taxonomy (TaskClassSegment and TaskClassChat carry no priors row: R-16.37
// does not price them). Declared here so init() can validate exhaustively
// without a second, competing task-class enum.
var priorTaskClasses = []conductor.TaskClass{
	conductor.TaskClassCode,
	conductor.TaskClassReason,
	conductor.TaskClassReview,
	conductor.TaskClassArbitrate,
	conductor.TaskClassClassify,
	conductor.TaskClassExtract,
	conductor.TaskClassSummarize,
}

// Priors is the R-16.37 vendor-neutral Beta(2,2) seed score per
// (tier x task_class) cell, verbatim from the ratified table. It is the
// ONE priors table in the tree (R-16.71): a future consumer imports this
// map rather than declaring a second one.
var Priors = map[Tier]map[conductor.TaskClass]float64{
	TierZero: {
		conductor.TaskClassCode:      0.90,
		conductor.TaskClassReason:    0.92,
		conductor.TaskClassReview:    0.90,
		conductor.TaskClassArbitrate: 0.95,
		conductor.TaskClassClassify:  0.95,
		conductor.TaskClassExtract:   0.95,
		conductor.TaskClassSummarize: 0.95,
	},
	TierOne: {
		conductor.TaskClassCode:      0.85,
		conductor.TaskClassReason:    0.85,
		conductor.TaskClassReview:    0.85,
		conductor.TaskClassArbitrate: 0.85,
		conductor.TaskClassClassify:  0.95,
		conductor.TaskClassExtract:   0.95,
		conductor.TaskClassSummarize: 0.95,
	},
	TierTwo: {
		conductor.TaskClassCode:      0.75,
		conductor.TaskClassReason:    0.70,
		conductor.TaskClassReview:    0.75,
		conductor.TaskClassArbitrate: 0.60,
		conductor.TaskClassClassify:  0.95,
		conductor.TaskClassExtract:   0.95,
		conductor.TaskClassSummarize: 0.95,
	},
	// TierLocal's authoring cells (code/reason/review/arbitrate) are 0.00:
	// a scoring floor, not an enabling switch -- see this file's top-level
	// AUTHORING IS A PER-MODEL CAPABILITY note.
	TierLocal: {
		conductor.TaskClassCode:      0.00,
		conductor.TaskClassReason:    0.00,
		conductor.TaskClassReview:    0.00,
		conductor.TaskClassArbitrate: 0.00,
		conductor.TaskClassClassify:  0.85,
		conductor.TaskClassExtract:   0.85,
		conductor.TaskClassSummarize: 0.85,
	},
}

// ReworkCyclesEst is the R-21.175 expected-time rework-cycle estimate: a
// single retry is assumed when a rework is needed. init() panics if this
// drops below 1 (a value below one whole cycle is not a rework estimate).
const ReworkCyclesEst = 1

// JumpHysteresisFactor is the R-21.175 20% hysteresis margin the jump rule
// applies so a score hovering at the tier-0 threshold does not flap between
// tiers call to call. init() panics if this is outside (0, 1]: zero would
// erase the margin entirely and a value above 1 is not a margin at all.
const JumpHysteresisFactor = 0.8

// PReworkFromScore is the R-21.175 probability-of-rework formula: the
// complement of the lane's capability score for this (task_class, tier)
// cell. score is expected in [0, 1] (a Priors cell or a learned posterior
// mean); this function performs no range validation of its own -- the
// caller's score already came from a validated Priors cell or a learned
// scorer that enforces its own [0, 1] invariant.
func PReworkFromScore(score float64) float64 {
	return 1 - score
}

// validatePriorsTable checks that priors carries every (tier x task_class)
// cell this file's own priorTaskClasses/four-tier set requires, each within
// [0, 1]. Split out from init() (rather than inlined) so priors_test.go can
// drive the exact function init() calls against a deliberately corrupted
// table, proving the panic path fires for a real cause -- not a copy of it.
func validatePriorsTable(priors map[Tier]map[conductor.TaskClass]float64) error {
	for key := range priors {
		if !key.Valid() {
			return fmt.Errorf("capacity: priors table has unknown tier key %q", key)
		}
	}
	for _, tier := range []Tier{TierZero, TierOne, TierTwo, TierLocal} {
		row, ok := priors[tier]
		if !ok {
			return fmt.Errorf("capacity: priors table missing tier %q", tier)
		}
		for _, tc := range priorTaskClasses {
			score, ok := row[tc]
			if !ok {
				return fmt.Errorf("capacity: priors table missing cell (%q, %q)", tier, tc)
			}
			if score < 0 || score > 1 {
				return fmt.Errorf("capacity: priors table cell (%q, %q) = %v, want [0, 1]", tier, tc, score)
			}
		}
	}
	return nil
}

// validateJumpConstants checks the R-21.175 constants' own bounds. Split
// out for the same reason as validatePriorsTable above.
func validateJumpConstants(hysteresis float64, reworkCycles int) error {
	if hysteresis <= 0 || hysteresis > 1 {
		return fmt.Errorf("capacity: JumpHysteresisFactor = %v, want (0, 1]", hysteresis)
	}
	if reworkCycles < 1 {
		return fmt.Errorf("capacity: ReworkCyclesEst = %v, want >= 1", reworkCycles)
	}
	return nil
}

// mustValidatePriors panics with err's message when a validator fails.
// init() calls it against the real Priors/constants; priors_test.go calls
// it directly against a corrupted table inside a recover() subtest, so the
// test exercises the identical panic path init() uses at package load.
func mustValidatePriors(err error) {
	if err != nil {
		panic(err.Error())
	}
}

// init validates Priors and the R-21.175 constants at package load, so a
// corrupt table or constant fails the process immediately rather than
// silently mis-routing real work with an optimistic guess later. This
// validation is Priors/ReworkCyclesEst/JumpHysteresisFactor's genuine
// production reference (not merely this file's own tests) -- see the
// CONTRACT DEVIATION note above.
func init() {
	mustValidatePriors(validatePriorsTable(Priors))
	mustValidatePriors(validateJumpConstants(JumpHysteresisFactor, ReworkCyclesEst))
}
