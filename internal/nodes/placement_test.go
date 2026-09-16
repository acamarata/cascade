package nodes

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// placementCase is one row of the decision table in
// testdata/placement/eligibility.json. Each row carries the rule it
// encodes, so a row can only be changed by changing a stated rule — never
// by regenerating it from whatever the engine currently returns.
type placementCase struct {
	Name        string `json:"name"`
	Rule        string `json:"rule"`
	Requirement struct {
		Capabilities []string `json:"capabilities"`
		Sensitivity  string   `json:"sensitivity"`
	} `json:"requirement"`
	Candidates []struct {
		NodeID       string   `json:"node_id"`
		Tier         string   `json:"tier"`
		Presence     string   `json:"presence"`
		Tunnel       string   `json:"tunnel"`
		Drained      bool     `json:"drained"`
		Capabilities []string `json:"capabilities"`
	} `json:"candidates"`
	Eligible []string `json:"eligible"`
}

// loadPlacementTable reads the decision table.
func loadPlacementTable(t *testing.T) []placementCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "placement", "eligibility.json"))
	if err != nil {
		t.Fatalf("read the placement decision table: %v", err)
	}
	var doc struct {
		Cases []placementCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode the placement decision table: %v", err)
	}
	if len(doc.Cases) == 0 {
		t.Fatal("the placement decision table is empty")
	}
	return doc.Cases
}

// tunnelStateFromName maps the table's tunnel column onto TunnelState,
// refusing an unknown spelling rather than defaulting it — a typo in the
// table must fail loudly, not silently become "down" and make a row pass
// for the wrong reason.
func tunnelStateFromName(t *testing.T, name string) TunnelState {
	t.Helper()
	switch name {
	case "down":
		return TunnelDown
	case "reconnecting":
		return TunnelReconnecting
	case "up":
		return TunnelUp
	default:
		t.Fatalf("decision table names an unknown tunnel state %q", name)
		return TunnelDown
	}
}

// TestPlacementEligibility_DecisionTable runs every row of the table
// through the real engine. It is the combination test: the trust, state and
// capability filters each have focused tests of their own, and this exists
// to catch an ordering or interaction mistake between them.
func TestPlacementEligibility_DecisionTable(t *testing.T) {
	for _, tc := range loadPlacementTable(t) {
		t.Run(tc.Name, func(t *testing.T) {
			if tc.Rule == "" {
				t.Fatal("this row states no rule; a table row without its rule cannot be reviewed")
			}

			states := map[string]TunnelState{}
			candidates := make([]Candidate, 0, len(tc.Candidates))
			for _, c := range tc.Candidates {
				states[c.NodeID] = tunnelStateFromName(t, c.Tunnel)
				candidates = append(candidates, Candidate{
					Record: DeviceRecord{
						NodeID:   c.NodeID,
						Tier:     Tier(c.Tier),
						Presence: Presence(c.Presence),
						Drained:  c.Drained,
					},
					Report: CapabilityReport{Capabilities: c.Capabilities},
				})
			}
			engine := Engine{Tunnels: func(id string) TunnelState { return states[id] }}

			got, err := engine.Eligible(Requirement{
				Capabilities: tc.Requirement.Capabilities,
				Sensitivity:  Sensitivity(tc.Requirement.Sensitivity),
			}, candidates)

			if len(tc.Eligible) == 0 {
				if err == nil {
					t.Fatalf("expected no eligible node (rule: %s), got %v", tc.Rule, ids(got))
				}
				if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
					t.Fatalf("error kind = %v (typed=%v), want %v", kind, ok, cascade.KindUnavailable)
				}
				return
			}

			if err != nil {
				t.Fatalf("expected %v eligible (rule: %s), got error: %v", tc.Eligible, tc.Rule, err)
			}
			assertIDs(t, got, tc.Eligible, tc.Rule)
		})
	}
}

// ids extracts node ids for a failure message.
func ids(recs []DeviceRecord) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.NodeID)
	}
	return out
}

// assertIDs compares the eligible set to the expected one BY ORDER, not as
// a set: the engine promises candidate order so the router's own selection
// is deterministic, and a comparison that sorted first would not notice
// that promise being broken.
func assertIDs(t *testing.T, got []DeviceRecord, want []string, rule string) {
	t.Helper()
	gotIDs := ids(got)
	if len(gotIDs) != len(want) {
		t.Fatalf("eligible = %v, want %v (rule: %s)", gotIDs, want, rule)
	}
	for i := range want {
		if gotIDs[i] != want[i] {
			t.Fatalf("eligible = %v, want %v in this order (rule: %s)", gotIDs, want, rule)
		}
	}
}

// TestPlacementIsPure proves the engine decides only from its inputs:
// running the same table row twice yields identical answers, so nothing
// consults a clock, a counter or ambient state.
func TestPlacementIsPure(t *testing.T) {
	engine := Engine{Tunnels: func(string) TunnelState { return TunnelUp }}
	req := Requirement{Capabilities: []string{"browser"}, Sensitivity: SensitivityNormal}
	candidates := []Candidate{
		{
			Record: DeviceRecord{NodeID: "n1", Tier: TierWorkerTrusted, Presence: PresenceReachable},
			Report: CapabilityReport{Capabilities: []string{"browser"}},
		},
	}

	first, err := engine.Eligible(req, candidates)
	if err != nil {
		t.Fatal(err)
	}
	second, err := engine.Eligible(req, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len(second) || first[0].NodeID != second[0].NodeID {
		t.Fatalf("two identical calls disagreed: %v vs %v", ids(first), ids(second))
	}
}

// TestPlacementNeverFallsBackToTheController is the rule the contract
// states most strongly: an empty eligible set is an error, and the error
// must not carry a node the caller could mistake for a placement.
func TestPlacementNeverFallsBackToTheController(t *testing.T) {
	engine := Engine{Tunnels: func(string) TunnelState { return TunnelUp }}
	got, err := engine.Eligible(
		Requirement{Sensitivity: SensitivityLocalOnly},
		[]Candidate{{Record: DeviceRecord{NodeID: "n1", Tier: TierController, Presence: PresenceReachable}}},
	)
	if err == nil {
		t.Fatal("local-only work found an eligible node")
	}
	if got != nil {
		t.Fatalf("a failed placement returned %v; a caller could treat it as a placement", ids(got))
	}
}

// TestPlacementWithoutAConnectionSourcePlacesNothing proves the zero-value
// engine fails closed. An engine whose composition root forgot to wire the
// tunnel lookup must place nothing, never everything.
func TestPlacementWithoutAConnectionSourcePlacesNothing(t *testing.T) {
	var engine Engine // no Tunnels lookup
	_, err := engine.Eligible(
		Requirement{Sensitivity: SensitivityNormal},
		[]Candidate{{Record: DeviceRecord{NodeID: "n1", Tier: TierWorkerTrusted, Presence: PresenceReachable}}},
	)
	if err == nil {
		t.Fatal("an engine with no connection source placed work anyway")
	}
}
