package provider

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestSelectionNodeIDRoundTrip verifies that Selection.NodeID survives a
// JSON round trip and is omitted when empty, while ModelRequest remains
// unchanged in its field set.
func TestSelectionNodeIDRoundTrip(t *testing.T) {
	t.Run("node_id round trip", testSelectionNodeIDSurvives)
	t.Run("node_id omitted when empty", testSelectionNodeIDOmitted)
	t.Run("ModelRequest unchanged", testModelRequestUnchanged)
}

// testSelectionNodeIDSurvives checks node_id survives a JSON round trip.
func testSelectionNodeIDSurvives(t *testing.T) {
	orig := Selection{
		LaneID:      "lane-1",
		Provider:    "anthropic",
		Model:       "claude-3",
		NodeID:      "node-42",
		ReasonFlags: []string{"cost:cheapest"},
	}
	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got Selection
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(orig, got) {
		t.Errorf("round trip mismatch:\norig: %#v\ngot:  %#v", orig, got)
	}
}

// testSelectionNodeIDOmitted checks node_id is absent from the JSON when empty.
func testSelectionNodeIDOmitted(t *testing.T) {
	b, err := json.Marshal(Selection{LaneID: "lane-1", Provider: "anthropic", Model: "claude-3"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("Unmarshal to map: %v", err)
	}
	if _, ok := m["node_id"]; ok {
		t.Errorf("node_id should be omitted when empty, got: %s", string(b))
	}
}

// testModelRequestUnchanged checks ModelRequest kept its field set:
// TaskID, TaskClass, Inputs, Requirements, Sensitivity, Policy,
// RequiredCapabilities, FanOut, ReservationID.
func testModelRequestUnchanged(t *testing.T) {
	if n := reflect.TypeOf(ModelRequest{}).NumField(); n != 9 {
		t.Errorf("ModelRequest field count changed to %d, ticket forbids changes to ModelRequest", n)
	}
}
