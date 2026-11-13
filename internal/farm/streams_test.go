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

// TestStreamTargetIsMinOfReadyAndCeiling verifies that PlanStreams returns
// the minimum of ready, 200, and ceiling.
func TestStreamTargetIsMinOfReadyAndCeiling(t *testing.T) {
	tests := []struct {
		name     string
		ready    int
		ceiling  int
		expected int
	}{
		{"ready is smallest", 50, 150, 50},
		{"ceiling is smallest", 300, 80, 80},
		{"target 200 is smallest", 500, 400, 200},
		{"all equal at 200", 200, 200, 200},
		{"ready and ceiling both below 200, ready smaller", 100, 120, 100},
		{"ready and ceiling both below 200, ceiling smaller", 120, 100, 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := PlanStreams(tt.ready, tt.ceiling, 0, 0, nil)
			if plan.Streams != tt.expected {
				t.Errorf("PlanStreams(%d, %d, 0, 0, nil).Streams = %d, want %d",
					tt.ready, tt.ceiling, plan.Streams, tt.expected)
			}
		})
	}
}

// TestTwoHundredReadyPlansTwoHundred verifies that when ready is at least 200
// and ceiling is at least 200, the plan produces exactly 200 streams.
func TestTwoHundredReadyPlansTwoHundred(t *testing.T) {
	tests := []struct {
		name    string
		ready   int
		ceiling int
	}{
		{"exactly 200 ready, exactly 200 ceiling", 200, 200},
		{"300 ready, 250 ceiling", 300, 250},
		{"500 ready, 1000 ceiling", 500, 1000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := PlanStreams(tt.ready, tt.ceiling, 0, 0, nil)
			if plan.Streams != 200 {
				t.Errorf("PlanStreams(%d, %d, 0, 0, nil).Streams = %d, want 200",
					tt.ready, tt.ceiling, plan.Streams)
			}
		})
	}
}

// TestBindingNamesTheLimit verifies that Plan.Binding correctly identifies
// which constraint limited the stream count.
func TestBindingNamesTheLimit(t *testing.T) {
	tests := []struct {
		name            string
		ready           int
		ceiling         int
		expectedBinding Binding
	}{
		{"ready limits at 50", 50, 150, BindingReady},
		{"ceiling limits at 80", 300, 80, BindingCeiling},
		{"target 200 limits", 500, 400, BindingTarget},
		{"ready and target tie at 200", 200, 300, BindingReady},
		{"ceiling and target tie at 200", 300, 200, BindingCeiling},
		{"all three tie at 200", 200, 200, BindingReady},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := PlanStreams(tt.ready, tt.ceiling, 0, 0, nil)
			if plan.Binding != tt.expectedBinding {
				t.Errorf("PlanStreams(%d, %d, 0, 0, nil).Binding = %q, want %q",
					tt.ready, tt.ceiling, plan.Binding, tt.expectedBinding)
			}
		})
	}
}

// TestRPMWaitOrEscalate verifies that when RPM limit is reached,
// the plan chooses between waiting and escalating based on cost.
func TestRPMWaitOrEscalate(t *testing.T) {
	ladder := []string{"tier1", "tier2", "tier3"}

	tests := []struct {
		name           string
		rpmUsed        int
		rpmLimit       int
		ladder         []string
		wantActionType ActionType
		wantEscalateTo string
	}{
		{
			name:           "wait when no ladder available",
			rpmUsed:        100,
			rpmLimit:       100,
			ladder:         []string{},
			wantActionType: ActionWait,
			wantEscalateTo: "",
		},
		{
			name:           "escalate when ladder has rungs",
			rpmUsed:        100,
			rpmLimit:       100,
			ladder:         ladder,
			wantActionType: ActionEscalate,
			wantEscalateTo: "tier2",
		},
		{
			name:           "no RPM limit reached",
			rpmUsed:        50,
			rpmLimit:       100,
			ladder:         ladder,
			wantActionType: ActionStart,
			wantEscalateTo: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := PlanStreams(100, 200, tt.rpmUsed, tt.rpmLimit, tt.ladder)
			if plan.Action.Type != tt.wantActionType {
				t.Errorf("PlanStreams action type = %v, want %v", plan.Action.Type, tt.wantActionType)
			}
			if tt.wantEscalateTo != "" && plan.Action.EscalateTo != tt.wantEscalateTo {
				t.Errorf("PlanStreams escalate to = %q, want %q", plan.Action.EscalateTo, tt.wantEscalateTo)
			}
			if tt.wantActionType == ActionWait && plan.Action.WaitUntil.IsZero() {
				t.Errorf("PlanStreams wait action must have non-zero WaitUntil")
			}
		})
	}
}

// TestNeverStalls verifies that PlanStreams never returns a blocking action
// and always provides a path forward.
func TestNeverStalls(t *testing.T) {
	ladder := []string{"tier1", "tier2", "tier3"}

	tests := []struct {
		name     string
		ready    int
		ceiling  int
		rpmUsed  int
		rpmLimit int
		ladder   []string
	}{
		{"normal operation", 100, 200, 50, 100, ladder},
		{"RPM exhausted with ladder", 100, 200, 100, 100, ladder},
		{"RPM exhausted empty ladder", 100, 200, 100, 100, []string{}},
		{"RPM exhausted single rung", 100, 200, 100, 100, []string{"tier1"}},
		{"zero streams possible", 0, 200, 50, 100, ladder},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := PlanStreams(tt.ready, tt.ceiling, tt.rpmUsed, tt.rpmLimit, tt.ladder)

			// Plan must always have a valid action type
			if plan.Action.Type != ActionStart &&
				plan.Action.Type != ActionWait &&
				plan.Action.Type != ActionEscalate {
				t.Errorf("PlanStreams returned invalid action type: %v", plan.Action.Type)
			}

			// If action is Wait, WaitUntil must be set
			if plan.Action.Type == ActionWait && plan.Action.WaitUntil.IsZero() {
				t.Errorf("PlanStreams Wait action has zero WaitUntil")
			}

			// If action is Escalate, EscalateTo should be set if ladder exists
			if plan.Action.Type == ActionEscalate && len(tt.ladder) > 1 && plan.Action.EscalateTo == "" {
				t.Errorf("PlanStreams Escalate action has empty EscalateTo with available ladder")
			}
		})
	}
}
