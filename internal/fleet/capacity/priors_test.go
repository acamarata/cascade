// Purpose: proves the priors table matches R-16.37's ratified values,
// proves validatePriorsTable/validateJumpConstants panic (via
// mustValidatePriors) on a corrupt entry, and proves the R-21.175 jump
// constants and PReworkFromScore. Every table assertion below is a
// hardcoded literal transcription of R-16.37's spec text, never a
// self-comparison against Priors itself -- a check that shares the bug it
// checks for proves nothing.
//
// SPORT: fleet/capacity/priors (ADD, P1-E31-W6-S63-T4).
package capacity

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
)

// specPriors is a hardcoded, independent transcription of R-16.37 §Fleet
// capacity's priors table (18-T0-RULINGS-R16.md), typed exactly as
// TestPriorsMatchSpec below asserts it -- NOT derived from the Priors var
// under test in any way.
var specPriors = map[Tier]map[conductor.TaskClass]float64{
	TierZero: {
		conductor.TaskClassCode: 0.90, conductor.TaskClassReason: 0.92,
		conductor.TaskClassReview: 0.90, conductor.TaskClassArbitrate: 0.95,
		conductor.TaskClassClassify: 0.95, conductor.TaskClassExtract: 0.95,
		conductor.TaskClassSummarize: 0.95,
	},
	TierOne: {
		conductor.TaskClassCode: 0.85, conductor.TaskClassReason: 0.85,
		conductor.TaskClassReview: 0.85, conductor.TaskClassArbitrate: 0.85,
		conductor.TaskClassClassify: 0.95, conductor.TaskClassExtract: 0.95,
		conductor.TaskClassSummarize: 0.95,
	},
	TierTwo: {
		conductor.TaskClassCode: 0.75, conductor.TaskClassReason: 0.70,
		conductor.TaskClassReview: 0.75, conductor.TaskClassArbitrate: 0.60,
		conductor.TaskClassClassify: 0.95, conductor.TaskClassExtract: 0.95,
		conductor.TaskClassSummarize: 0.95,
	},
	TierLocal: {
		conductor.TaskClassCode: 0.00, conductor.TaskClassReason: 0.00,
		conductor.TaskClassReview: 0.00, conductor.TaskClassArbitrate: 0.00,
		conductor.TaskClassClassify: 0.85, conductor.TaskClassExtract: 0.85,
		conductor.TaskClassSummarize: 0.85,
	},
}

// TestPriorsMatchSpec asserts every (tier x task_class) cell against
// specPriors -- the independent spec transcription above, never against
// Priors compared with itself.
func TestPriorsMatchSpec(t *testing.T) {
	for tier, wantRow := range specPriors {
		gotRow, ok := Priors[tier]
		if !ok {
			t.Fatalf("Priors missing tier %q", tier)
		}
		for tc, want := range wantRow {
			got, ok := gotRow[tc]
			if !ok {
				t.Errorf("Priors[%q] missing task class %q", tier, tc)
				continue
			}
			if got != want {
				t.Errorf("Priors[%q][%q] = %v, want %v (R-16.37)", tier, tc, got, want)
			}
		}
		if len(gotRow) != len(wantRow) {
			t.Errorf("Priors[%q] has %d cells, spec has %d -- extra or missing entries", tier, len(gotRow), len(wantRow))
		}
	}
	if len(Priors) != len(specPriors) {
		t.Errorf("Priors has %d tiers, spec has %d", len(Priors), len(specPriors))
	}
}

// TestValidatePriorsTable_PanicsOnCorruptEntry proves the exact function
// init() calls (mustValidatePriors wrapping validatePriorsTable) panics
// when a cell is missing or out of [0, 1] -- fail-closed, never an
// optimistic default.
func TestValidatePriorsTable_PanicsOnCorruptEntry(t *testing.T) {
	cases := []struct {
		name  string
		table map[Tier]map[conductor.TaskClass]float64
	}{
		{"missing tier", map[Tier]map[conductor.TaskClass]float64{
			TierZero: specPriors[TierZero], TierOne: specPriors[TierOne], TierTwo: specPriors[TierTwo],
			// TierLocal omitted.
		}},
		{"missing cell", func() map[Tier]map[conductor.TaskClass]float64 {
			corrupt := cloneTierRow(specPriors[TierZero])
			delete(corrupt, conductor.TaskClassCode)
			return map[Tier]map[conductor.TaskClass]float64{
				TierZero: corrupt, TierOne: specPriors[TierOne], TierTwo: specPriors[TierTwo], TierLocal: specPriors[TierLocal],
			}
		}()},
		{"out of range high", withOverriddenCell(1.5)},
		{"out of range negative", withOverriddenCell(-0.1)},
		{"unknown tier key", map[Tier]map[conductor.TaskClass]float64{
			TierZero: specPriors[TierZero], TierOne: specPriors[TierOne],
			TierTwo: specPriors[TierTwo], TierLocal: specPriors[TierLocal],
			Tier("tier-3-typo"): specPriors[TierZero],
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatal("mustValidatePriors(validatePriorsTable(corrupt)) did not panic")
				}
			}()
			mustValidatePriors(validatePriorsTable(tc.table))
			t.Fatal("unreachable: panic expected before this line")
		})
	}
}

// cloneTierRow returns a shallow copy of row so a test can mutate it
// without corrupting specPriors for later subtests.
func cloneTierRow(row map[conductor.TaskClass]float64) map[conductor.TaskClass]float64 {
	out := make(map[conductor.TaskClass]float64, len(row))
	for k, v := range row {
		out[k] = v
	}
	return out
}

// withOverriddenCell returns a full four-tier table identical to
// specPriors except TierZero's code cell is set to score.
func withOverriddenCell(score float64) map[Tier]map[conductor.TaskClass]float64 {
	corrupt := cloneTierRow(specPriors[TierZero])
	corrupt[conductor.TaskClassCode] = score
	return map[Tier]map[conductor.TaskClass]float64{
		TierZero: corrupt, TierOne: specPriors[TierOne], TierTwo: specPriors[TierTwo], TierLocal: specPriors[TierLocal],
	}
}

// TestValidateJumpConstants_PanicsOnCorruptValue proves the constants'
// own bounds are enforced the same fail-closed way.
func TestValidateJumpConstants_PanicsOnCorruptValue(t *testing.T) {
	cases := []struct {
		name       string
		hysteresis float64
		rework     int
	}{
		{"hysteresis zero", 0, ReworkCyclesEst},
		{"hysteresis above one", 1.2, ReworkCyclesEst},
		{"rework below one", JumpHysteresisFactor, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatal("mustValidatePriors(validateJumpConstants(...)) did not panic")
				}
			}()
			mustValidatePriors(validateJumpConstants(tc.hysteresis, tc.rework))
			t.Fatal("unreachable: panic expected before this line")
		})
	}
}

// TestPriorsJumpConstants asserts ReworkCyclesEst, JumpHysteresisFactor and
// PReworkFromScore's values (R-21.175), and that a grep over internal/
// finds no second definition of either constant (R-16.71: one home).
func TestPriorsJumpConstants(t *testing.T) {
	if ReworkCyclesEst != 1 {
		t.Errorf("ReworkCyclesEst = %v, want 1", ReworkCyclesEst)
	}
	if JumpHysteresisFactor != 0.8 {
		t.Errorf("JumpHysteresisFactor = %v, want 0.8", JumpHysteresisFactor)
	}
	if got := PReworkFromScore(0.75); got != 0.25 {
		t.Errorf("PReworkFromScore(0.75) = %v, want 0.25", got)
	}

	for _, name := range []string{"ReworkCyclesEst", "JumpHysteresisFactor"} {
		decls, err := countConstDeclarations(name)
		if err != nil {
			t.Fatalf("scanning internal/ for %s declarations: %v", name, err)
		}
		if decls != 1 {
			t.Errorf("expected exactly one declaration of %s (internal/fleet/capacity/priors.go), found %d", name, decls)
		}
	}
}

// countConstDeclarations walks internal/ (a pure-Go scan, not a shelled-out
// grep, so this test runs identically on every platform CI covers,
// including Windows) and counts non-test .go files declaring a top-level
// "<name> ... =" const line -- R-16.71's "one home" check.
func countConstDeclarations(name string) (int, error) {
	root := filepath.Join("..", "..", "..", "internal")
	constNeedle := "const " + name + " "
	varNeedle := "var " + name + " "
	var count int
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for _, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if (strings.HasPrefix(trimmed, constNeedle) || strings.HasPrefix(trimmed, varNeedle)) && strings.Contains(trimmed, "=") {
				count++
			}
		}
		return nil
	})
	return count, err
}

// TestTierValid proves Tier's closed vocabulary: the four declared members
// are valid, the zero value and an arbitrary string are not.
func TestTierValid(t *testing.T) {
	for _, tier := range []Tier{TierZero, TierOne, TierTwo, TierLocal} {
		if !tier.Valid() {
			t.Errorf("Tier(%q).Valid() = false, want true", tier)
		}
	}
	for _, bad := range []Tier{"", "tier-3", "TIER-0"} {
		if bad.Valid() {
			t.Errorf("Tier(%q).Valid() = true, want false (fail closed)", bad)
		}
	}
}
