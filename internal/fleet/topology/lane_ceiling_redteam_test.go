package topology_test

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/pkg/provider"
)

// laneTypes maps the topology wire names onto the conductor's LaneType.
var laneTypes = map[topology.LaneKind]conductor.LaneType{
	topology.LaneKindControllerLocal: conductor.LaneControllerLocal,
	topology.LaneKindExternalAPI:     conductor.LaneExternalAPI,
	topology.LaneKindUnresolved:      conductor.LaneUnresolved,
}

// init wires the conductor's one lane table into topology for the whole
// test binary, so every ceiling test reads the real table, never a copy.
func init() {
	topology.SensitivityTable = func(t provider.SensitivityTier, lane topology.LaneKind, permitted bool) bool {
		lt, ok := laneTypes[lane]
		if !ok {
			return false
		}
		return conductor.SensitivityAllows(t, lt, permitted)
	}
}

// TestRemoteOllamaRefusesLocalOnly: an ollama runtime on a LAN host is an
// external lane, so local-only work never runs on it; on loopback it does.
func TestRemoteOllamaRefusesLocalOnly(t *testing.T) {
	ctx := context.Background()
	s := topology.NewCeilingTestStore(t)
	topology.SetPaidEvidenceForTest(t, func(context.Context, *topology.Store, topology.QuotaDomain) (bool, error) { return true, nil })
	known := map[string]string{"training": "false", "retention": "none"}
	remote := topology.SeedCeilingLane(t, s, "ollama-lan", topology.BillingTierPaid, topology.RuntimeOllama, "10.0.0.5", known)
	local := topology.SeedCeilingLane(t, s, "ollama-local", topology.BillingTierPaid, topology.RuntimeOllama, "127.0.0.1", known)
	rc, err := s.CeilingForLane(ctx, remote, false)
	if err != nil {
		t.Fatalf("remote ceiling: %v", err)
	}
	if rc.LaneType != topology.LaneKindExternalAPI || topology.TierAdmits(provider.SensitivityLocalOnly, rc) {
		t.Fatalf("remote ollama ceiling %+v admits local-only", rc)
	}
	lc, err := s.CeilingForLane(ctx, local, false)
	if err != nil {
		t.Fatalf("local ceiling: %v", err)
	}
	if lc.Tier != provider.SensitivityLocalOnly || lc.Reason != topology.CeilingLocalEndpoint {
		t.Fatalf("loopback ollama ceiling = %+v, want local-only/local-endpoint", lc)
	}
}
