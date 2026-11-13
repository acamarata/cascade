package farm

import (
	"testing"
	"time"
)

// TestTypesCompile_FARM23 verifies the stream planner's public data types.
func TestTypesCompile_FARM23(t *testing.T) {
	bindings := []Binding{BindingReady, BindingTarget, BindingCeiling, BindingRPM}
	seenBindings := make(map[Binding]bool)
	for _, binding := range bindings {
		if binding == "" || seenBindings[binding] {
			t.Fatalf("binding must be nonempty and unique: %q", binding)
		}
		seenBindings[binding] = true
	}

	deadline := time.Unix(100, 0)
	seenActions := make(map[ActionType]bool)
	for _, actionType := range []ActionType{ActionStart, ActionWait, ActionEscalate} {
		if seenActions[actionType] {
			t.Fatalf("action types must be unique: %d", actionType)
		}
		seenActions[actionType] = true
		plan := Plan{
			Streams: 64,
			Binding: BindingRPM,
			Action: Action{
				Type:       actionType,
				WaitUntil:  deadline,
				EscalateTo: "next",
			},
		}
		var streams = plan.Streams
		var binding = plan.Binding
		var action = plan.Action
		if streams != 64 || binding != BindingRPM || action.Type != actionType ||
			!action.WaitUntil.Equal(deadline) || action.EscalateTo != "next" {
			t.Fatalf("plan fields did not retain their values: %+v", plan)
		}
	}
}
