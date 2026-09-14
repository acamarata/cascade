// Purpose: the acceptance-gate golden harness for Epic K's Conductor
//   router (K/S-22.T2): a table-driven matrix over every (task_class,
//   sensitivity_tier) pair from §5.16, plus one representative fixture
//   per five-filter-stage outcome, and a determinism check. Values come
//   from testdata/routing_matrix/goldens.yaml, whose provenance is that
//   directory's README.md - hand-derived from reading the real filter
//   source before this test was ever run, never captured from this
//   test's own output.
// Inputs: testdata/routing_matrix/goldens.yaml.
// Outputs: none - test file.
// Constraints: drives the real Router.Select/SelectExplain entry point
//   only; never asserts a golden against a second copy of itself.
// SPORT: conductor.router/ADD (P1-E11-W3-S23-T5).

package conductor

import (
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
	"gopkg.in/yaml.v3"
)

// pairRow mirrors goldens.yaml's "pairs" entry shape: one (task_class,
// sensitivity_tier) golden row over the uniform single-healthy-local-lane
// fixture (see the README for why the fixture is uniform).
type pairRow struct {
	TaskClass   string   `yaml:"task_class"`
	Sensitivity string   `yaml:"sensitivity"`
	WantLaneID  string   `yaml:"want_lane_id"`
	WantFlags   []string `yaml:"want_flags"`
}

// stageRow mirrors goldens.yaml's "filter_stages" entry shape: one
// representative fixture per K/S-22.T2 filter-stage outcome, each varying
// exactly one filter's input against the same base fixture.
type stageRow struct {
	Name             string   `yaml:"name"`
	Sensitivity      string   `yaml:"sensitivity"`
	BaseURL          string   `yaml:"base_url"`
	Health           string   `yaml:"health"`
	PoolMembership   string   `yaml:"pool_membership"`
	RequiredSearch   bool     `yaml:"required_search"`
	CapabilitySearch string   `yaml:"capability_search"`
	WantLaneID       string   `yaml:"want_lane_id"`
	WantFlags        []string `yaml:"want_flags"`
	WantErr          string   `yaml:"want_err"`
}

// goldenFile mirrors goldens.yaml's top-level shape.
type goldenFile struct {
	Pairs        []pairRow  `yaml:"pairs"`
	FilterStages []stageRow `yaml:"filter_stages"`
}

// loadGoldens reads and parses testdata/routing_matrix/goldens.yaml once
// per call. Any parse or read failure fails the calling test immediately -
// a missing/malformed golden file is never treated as zero rows to cover.
func loadGoldens(t *testing.T) goldenFile {
	t.Helper()
	raw, err := os.ReadFile("testdata/routing_matrix/goldens.yaml")
	if err != nil {
		t.Fatalf("reading goldens.yaml: %v", err)
	}
	var gf goldenFile
	if err := yaml.Unmarshal(raw, &gf); err != nil {
		t.Fatalf("parsing goldens.yaml: %v", err)
	}
	if len(gf.Pairs) != 36 {
		t.Fatalf("goldens.yaml pairs = %d rows, want 36 (9 task classes x 4 sensitivity tiers)", len(gf.Pairs))
	}
	return gf
}

// sensitivityByName maps goldens.yaml's sensitivity strings to the real
// provider.SensitivityTier members (pkg/provider/model.go). An unset/
// unrecognized name resolves to the zero value, SensitivityRestricted -
// the same fail-closed default ResolveSensitivity applies.
var sensitivityByName = map[string]provider.SensitivityTier{
	"":           provider.SensitivityRestricted,
	"restricted": provider.SensitivityRestricted,
	"local-only": provider.SensitivityLocalOnly,
	"internal":   provider.SensitivityInternal,
	"public":     provider.SensitivityPublic,
}

// capStateByName maps goldens.yaml's capability-state strings to the real
// provider.CapabilityState members.
var capStateByName = map[string]provider.CapabilityState{
	"":            provider.CapabilityUnknown,
	"unknown":     provider.CapabilityUnknown,
	"supported":   provider.CapabilitySupported,
	"unsupported": provider.CapabilityUnsupported,
}

// stageErrByName maps goldens.yaml's want_err strings to the real
// exported sentinel errors this package declares. An unrecognized
// non-empty name fails the test rather than silently matching nil.
var stageErrByName = map[string]error{
	"ErrNoCapableProvider":   ErrNoCapableProvider,
	"ErrAllProvidersEvicted": ErrAllProvidersEvicted,
	"ErrNoLane":              ErrNoLane,
}

// pairRegistry builds the uniform single-healthy-local-lane fixture the
// 36-pair matrix uses for every row (see the README's "Fixture design"
// section for why it is uniform).
func pairRegistry() *fakeRegistry {
	return &fakeRegistry{
		providers: []provider.ProviderInfo{{
			Name:         "prov-a",
			BaseURL:      "http://127.0.0.1:8080",
			KnownModels:  []string{"model-a"},
			HealthStatus: "healthy",
		}},
		lanes: []provider.LaneInfo{{
			LaneName:     "lane-a",
			ProviderName: "prov-a",
			State:        "active",
		}},
	}
}

// pairRequest builds the ModelRequest for one pairRow.
func pairRequest(row pairRow) provider.ModelRequest {
	req := chatReq()
	req.TaskClass = row.TaskClass
	req.Sensitivity = sensitivityByName[row.Sensitivity]
	return req
}

// TestRoutingMatrix is the acceptance-gate golden matrix: every one of
// the 36 (task_class, sensitivity_tier) pairs from testdata/routing_matrix
// /goldens.yaml is driven through the real Router.SelectExplain, and its
// resolved lane_id and explainable_reason (ReasonFlags) are asserted
// against the hand-derived golden. It also asserts the resolved
// sensitivity tier is never silently downgraded (widened): ResolveSensitivity
// returns exactly the tier the row requested, never a looser one.
func TestRoutingMatrix(t *testing.T) {
	gf := loadGoldens(t)
	seen := make(map[string]bool, 36)
	for _, row := range gf.Pairs {
		row := row
		name := row.TaskClass + "/" + row.Sensitivity
		t.Run(name, func(t *testing.T) {
			seen[name] = true
			req := pairRequest(row)

			resolved := ResolveSensitivity(req)
			if resolved != req.Sensitivity {
				t.Fatalf("ResolveSensitivity downgraded/widened %v to %v, want unchanged", req.Sensitivity, resolved)
			}

			reg := pairRegistry()
			quota := &fakeQuota{order: []LaneID{"lane-a"}}
			r := NewRouter(reg, quota, nil, nil)
			sel, flags, err := r.SelectExplain(context.Background(), req)
			if err != nil {
				t.Fatalf("SelectExplain: %v", err)
			}
			if sel.LaneID != row.WantLaneID {
				t.Fatalf("LaneID = %q, want %q", sel.LaneID, row.WantLaneID)
			}
			if !reflect.DeepEqual(flags, row.WantFlags) {
				t.Fatalf("flags = %v, want %v", flags, row.WantFlags)
			}
		})
	}
	if len(seen) != 36 {
		t.Fatalf("covered %d distinct (task_class, sensitivity) pairs, want 36 - zero undecided pairs required", len(seen))
	}
}

// TestRoutingMatrix_FilterStages drives the ten filter-stage fixture rows
// from goldens.yaml, each varying exactly one of the five filters against
// the base fixture, through the real Router.SelectExplain.
func TestRoutingMatrix_FilterStages(t *testing.T) {
	gf := loadGoldens(t)
	if len(gf.FilterStages) == 0 {
		t.Fatal("goldens.yaml filter_stages is empty, want ten representative rows")
	}
	for _, row := range gf.FilterStages {
		row := row
		t.Run(row.Name, func(t *testing.T) {
			reg := &fakeRegistry{
				providers: []provider.ProviderInfo{{
					Name:         "prov-a",
					BaseURL:      row.BaseURL,
					KnownModels:  []string{"model-a"},
					HealthStatus: row.Health,
					Capabilities: provider.Capabilities{Search: capStateByName[row.CapabilitySearch]},
				}},
				lanes: []provider.LaneInfo{{
					LaneName:       "lane-a",
					ProviderName:   "prov-a",
					State:          "active",
					PoolMembership: row.PoolMembership,
				}},
			}
			quota := &fakeQuota{order: []LaneID{"lane-a"}}
			r := NewRouter(reg, quota, nil, nil)
			req := chatReq()
			req.Sensitivity = sensitivityByName[row.Sensitivity]
			req.RequiredCapabilities.Search = row.RequiredSearch

			sel, flags, err := r.SelectExplain(context.Background(), req)
			assertStageOutcome(t, row, sel, flags, err)
		})
	}
}

// assertStageOutcome checks one stageRow's expected error and, on
// success, its expected lane_id, against the real Select outcome. Split
// out of TestRoutingMatrix_FilterStages to keep that function under the
// 50-line cap.
func assertStageOutcome(t *testing.T, row stageRow, sel provider.Selection, flags []string, err error) {
	t.Helper()
	if row.WantErr != "" {
		wantErr, ok := stageErrByName[row.WantErr]
		if !ok {
			t.Fatalf("goldens.yaml names unrecognized want_err %q", row.WantErr)
		}
		if err != wantErr {
			t.Fatalf("err = %v, want %v", err, wantErr)
		}
	} else if err != nil {
		t.Fatalf("SelectExplain: %v", err)
	} else if sel.LaneID != row.WantLaneID {
		t.Fatalf("LaneID = %q, want %q", sel.LaneID, row.WantLaneID)
	}
	if !reflect.DeepEqual(flags, row.WantFlags) {
		t.Fatalf("flags = %v, want %v", flags, row.WantFlags)
	}
}

// TestRoutingMatrixDeterminism proves determinism by calling
// SelectExplain TWICE, in the same process, with byte-identical inputs
// for every pairs row, and comparing the two real returned outcomes with
// reflect.DeepEqual - never by asserting a sorted order the code itself
// produces (LANE-RULES §6).
func TestRoutingMatrixDeterminism(t *testing.T) {
	gf := loadGoldens(t)
	for _, row := range gf.Pairs {
		row := row
		t.Run(row.TaskClass+"/"+row.Sensitivity, func(t *testing.T) {
			req := pairRequest(row)
			quota1 := &fakeQuota{order: []LaneID{"lane-a"}}
			r1 := NewRouter(pairRegistry(), quota1, nil, nil)
			sel1, flags1, err1 := r1.SelectExplain(context.Background(), req)

			quota2 := &fakeQuota{order: []LaneID{"lane-a"}}
			r2 := NewRouter(pairRegistry(), quota2, nil, nil)
			sel2, flags2, err2 := r2.SelectExplain(context.Background(), req)

			if err1 != err2 {
				t.Fatalf("errors differ across two calls: %v vs %v", err1, err2)
			}
			if !reflect.DeepEqual(sel1, sel2) {
				t.Fatalf("Selection differs across two calls: %+v vs %+v", sel1, sel2)
			}
			if !reflect.DeepEqual(flags1, flags2) {
				t.Fatalf("flags differ across two calls: %v vs %v", flags1, flags2)
			}
		})
	}
}
