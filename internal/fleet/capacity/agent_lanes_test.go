package capacity

import (
	"reflect"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestAgentLaneSlots(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	posture := provider.NewCompliancePosture([]string{"oauth"}, true, false, []string{"agent"}, "steady", false)
	lanes := []AgentLane{
		{DriverID: "first", Posture: posture, LaneType: conductor.LaneControllerLocal},
		{DriverID: "second", LaneType: conductor.LaneExternalAPI},
		{DriverID: "third", LaneType: conductor.LaneUnresolved},
	}
	got, err := AgentLaneSlots(lanes, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(lanes) {
		t.Fatalf("got %d lanes, want %d", len(got), len(lanes))
	}
	for _, lane := range lanes {
		id := "agent:" + lane.DriverID
		want := ProviderSlot{LaneID: conductor.LaneID(id), ProfileRef: lane.DriverID, Posture: lane.Posture,
			State: StateAvailable, Tier: TierZero, LaneType: lane.LaneType, UpdatedAt: now}
		if !reflect.DeepEqual(got[id], want) {
			t.Errorf("slot %q = %#v, want %#v", id, got[id], want)
		}
	}
	for _, invalid := range [][]AgentLane{{{}}, {lanes[0], {}}} {
		got, err := AgentLaneSlots(invalid, now)
		if got != nil || !cascade.HasKind(err, cascade.KindInvalidInput) || err.Error() != "invalid-input: capacity: agent driver ID is required" {
			t.Fatalf("empty driver: slots=%v err=%v", got, err)
		}
	}
	if got, err := AgentLaneSlots(nil, now); err != nil || len(got) != 0 {
		t.Fatalf("empty lanes: %v, %v", got, err)
	}
}
