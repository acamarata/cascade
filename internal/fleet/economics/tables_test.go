package economics

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/topology"
)

// wantRow is a hand-typed literal of the R-21.32 multiplier table,
// authored independently from tables.go's laneRows/laneRowAliases so this
// test asserts the PRODUCTION table against the spec, never against a
// second copy of itself.
type wantRow struct{ plan, build, crunch, verify, incident float64 }

// wantMultipliers covers all eighteen resolvable keys: the seventeen
// R-21.30 lane classes plus the R-21.45 derived executive-overflow.
var wantMultipliers = map[topology.LaneClass]wantRow{
	topology.LaneClassExecutive:                {0.60, 2.5, 10.0, 0.80, 0.30},
	topology.LaneClassExecutiveXHigh:           {0.60, 2.5, 10.0, 0.80, 0.30},
	topology.LaneClassCritic:                   {0.80, 1.8, 8.0, 1.0, 0.40},
	topology.LaneClassSpecialist:               {1.1, 2.0, 10.0, 0.60, 0.25},
	topology.LaneClassAdvisor:                  {0.9, 1.5, 8.0, 0.75, 0.40},
	topology.LaneClassLead:                     {1.1, 0.65, 2.5, 1.0, 0.70},
	topology.LaneClassDeep:                     {1.1, 0.65, 2.5, 1.0, 0.70},
	topology.LaneClassHarnessSub:               {1.1, 0.65, 2.5, 1.0, 0.70},
	topology.LaneClassWorkerDedicated:          {1.0, 0.65, 1.0, 0.9, 0.60},
	topology.LaneClassWorkerFast:               {1.1, 0.65, 2.5, 1.0, 0.70},
	topology.LaneClassPoolPremium:              {1.1, 0.65, 2.5, 1.0, 0.70},
	topology.LaneClassPoolCheap:                {1.0, 0.8, 0.50, 0.9, 1.0},
	topology.LaneClassAPIPaid:                  {0.8, 0.8, 0.60, 0.8, 0.65},
	topology.LaneClassAPIFree:                  {0.8, 0.8, 0.60, 0.8, 0.65},
	topology.LaneClassAPIBatch:                 {1.5, 1.0, 0.35, 1.2, 2.00},
	topology.LaneClassLocal:                    {0.8, 0.8, 0.60, 0.8, 0.65},
	topology.LaneClassUnranked:                 {0.8, 0.8, 0.60, 0.8, 0.65},
	topology.LaneClass(executiveOverflowClass): {0.60, 2.5, 10.0, 0.80, 0.30},
}

// wantColumnAliases: discover=plan, integrate=build, release=verify.
var wantColumnAliases = map[Mode]Mode{
	ModeDiscover:  ModePlan,
	ModeIntegrate: ModeBuild,
	ModeRelease:   ModeVerify,
}

func TestModeMultiplierEveryCell(t *testing.T) {
	if len(wantMultipliers) != 18 {
		t.Fatalf("test literal has %d classes, want 18 (17 R-21.30 + executive-overflow)", len(wantMultipliers))
	}
	for class, want := range wantMultipliers {
		for _, col := range []struct {
			mode Mode
			want float64
		}{
			{ModePlan, want.plan}, {ModeBuild, want.build}, {ModeCrunch, want.crunch},
			{ModeVerify, want.verify}, {ModeIncident, want.incident},
		} {
			got, err := ModeMultiplier(class, col.mode)
			if err != nil {
				t.Fatalf("ModeMultiplier(%q, %q): %v", class, col.mode, err)
			}
			if got != col.want {
				t.Errorf("ModeMultiplier(%q, %q) = %v, want %v", class, col.mode, got, col.want)
			}
		}
		for alias, canonical := range wantColumnAliases {
			got, err := ModeMultiplier(class, alias)
			if err != nil {
				t.Fatalf("ModeMultiplier(%q, %q): %v", class, alias, err)
			}
			canonicalGot, err := ModeMultiplier(class, canonical)
			if err != nil {
				t.Fatalf("ModeMultiplier(%q, %q): %v", class, canonical, err)
			}
			if got != canonicalGot {
				t.Errorf("ModeMultiplier(%q, %q) = %v, want alias of %q = %v", class, alias, got, canonical, canonicalGot)
			}
		}
	}
}

func TestModeMultiplierUnknownLaneClass(t *testing.T) {
	if _, err := ModeMultiplier(topology.LaneClass("nonexistent"), ModePlan); !errors.Is(err, ErrUnknownLaneClass) {
		t.Errorf("error = %v, want ErrUnknownLaneClass", err)
	}
}

func TestModeMultiplierUnknownMode(t *testing.T) {
	if _, err := ModeMultiplier(topology.LaneClassLead, Mode("nonexistent")); !errors.Is(err, ErrUnknownMode) {
		t.Errorf("error = %v, want ErrUnknownMode", err)
	}
}

// wantWeights is a hand-typed literal of the R-21.32 utility-weight table.
var wantWeights = map[Mode]Weights{
	ModePlan:     {Quality: 5.0, Scarcity: 1.4, Diversity: 1.8, Latency: 0.3, Reliability: 1.0},
	ModeBuild:    {Quality: 5.0, Scarcity: 1.8, Diversity: 1.0, Latency: 0.5, Reliability: 1.0},
	ModeCrunch:   {Quality: 4.0, Scarcity: 2.6, Diversity: 0.6, Latency: 0.7, Reliability: 1.0},
	ModeVerify:   {Quality: 5.0, Scarcity: 1.0, Diversity: 2.4, Latency: 0.3, Reliability: 1.0},
	ModeIncident: {Quality: 6.0, Scarcity: 0.4, Diversity: 1.2, Latency: 1.0, Reliability: 1.5},
}

func TestUtilityWeightsEveryRow(t *testing.T) {
	for mode, want := range wantWeights {
		got, err := UtilityWeights(mode)
		if err != nil {
			t.Fatalf("UtilityWeights(%q): %v", mode, err)
		}
		if got != want {
			t.Errorf("UtilityWeights(%q) = %+v, want %+v", mode, got, want)
		}
	}
	for alias, canonical := range wantColumnAliases {
		got, err := UtilityWeights(alias)
		if err != nil {
			t.Fatalf("UtilityWeights(%q): %v", alias, err)
		}
		want, err := UtilityWeights(canonical)
		if err != nil {
			t.Fatalf("UtilityWeights(%q): %v", canonical, err)
		}
		if got != want {
			t.Errorf("UtilityWeights(%q) = %+v, want alias of %q = %+v", alias, got, canonical, want)
		}
	}
}

func TestUtilityWeightsUnknownMode(t *testing.T) {
	if _, err := UtilityWeights(Mode("nonexistent")); !errors.Is(err, ErrUnknownMode) {
		t.Errorf("error = %v, want ErrUnknownMode", err)
	}
}

// goldenMultiplierRow/goldenWeightRow/goldenFile mirror
// testdata/goldens/mode_tables.json's shape.
type goldenMultiplierRow struct {
	Class    string  `json:"class"`
	Plan     float64 `json:"plan"`
	Build    float64 `json:"build"`
	Crunch   float64 `json:"crunch"`
	Verify   float64 `json:"verify"`
	Incident float64 `json:"incident"`
}

type goldenWeightRow struct {
	Mode        string  `json:"mode"`
	Quality     float64 `json:"quality"`
	Scarcity    float64 `json:"scarcity"`
	Diversity   float64 `json:"diversity"`
	Latency     float64 `json:"latency"`
	Reliability float64 `json:"reliability"`
}

type goldenFile struct {
	Multipliers []goldenMultiplierRow `json:"multipliers"`
	Weights     []goldenWeightRow     `json:"weights"`
}

// TestModeTablesGolden asserts BOTH the golden JSON and the production
// tables against wantMultipliers/wantWeights (the independent literals
// above), never the golden against the production table directly -- a
// bug present in both the code and a captured golden would otherwise pass
// silently.
func TestModeTablesGolden(t *testing.T) {
	data, err := os.ReadFile("testdata/goldens/mode_tables.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var g goldenFile
	if err := json.Unmarshal(data, &g); err != nil {
		t.Fatalf("unmarshal golden: %v", err)
	}
	if len(g.Multipliers) != 18 {
		t.Fatalf("golden has %d multiplier rows, want 18", len(g.Multipliers))
	}
	seen := map[string]bool{}
	for _, row := range g.Multipliers {
		seen[row.Class] = true
		want, ok := wantMultipliers[topology.LaneClass(row.Class)]
		if !ok {
			t.Fatalf("golden row %q not in the independent spec literal", row.Class)
		}
		if row.Plan != want.plan || row.Build != want.build || row.Crunch != want.crunch ||
			row.Verify != want.verify || row.Incident != want.incident {
			t.Errorf("golden row %q = %+v, want %+v", row.Class, row, want)
		}
	}
	for class := range wantMultipliers {
		if !seen[string(class)] {
			t.Errorf("golden missing class %q present in the spec literal", class)
		}
	}
	if len(g.Weights) != 5 {
		t.Fatalf("golden has %d weight rows, want 5", len(g.Weights))
	}
	for _, row := range g.Weights {
		want, ok := wantWeights[Mode(row.Mode)]
		if !ok {
			t.Fatalf("golden weight row %q not in the independent spec literal", row.Mode)
		}
		if row.Quality != want.Quality || row.Scarcity != want.Scarcity || row.Diversity != want.Diversity ||
			row.Latency != want.Latency || row.Reliability != want.Reliability {
			t.Errorf("golden weight row %q = %+v, want %+v", row.Mode, row, want)
		}
	}
}

// TestModeMultiplierProvesModeChangesOutcome proves at least one mode
// materially changes ModeMultiplier's result for the same lane class --
// guarding against a table collapsed to one constant value.
func TestModeMultiplierProvesModeChangesOutcome(t *testing.T) {
	plan, err := ModeMultiplier(topology.LaneClassExecutive, ModePlan)
	if err != nil {
		t.Fatal(err)
	}
	crunch, err := ModeMultiplier(topology.LaneClassExecutive, ModeCrunch)
	if err != nil {
		t.Fatal(err)
	}
	if plan == crunch {
		t.Fatalf("ModeMultiplier(executive, plan) == ModeMultiplier(executive, crunch) == %v; mode has no effect", plan)
	}
}
