package agents

import "testing"

func TestAgentDeclarationsCompile(t *testing.T) {
	var state AgentState
	if state != "" {
		t.Errorf("expected zero value for AgentState, got %q", state)
	}

	var agent Agent
	if agent.ID != "" || agent.State != "" {
		t.Error("expected zero values for Agent struct")
	}

	if ErrNotImplemented == nil || errInvalidAgentState == nil || errAgentStateConflict == nil {
		t.Error("expected sentinels to be initialized")
	}

	if ACTIVE != "ACTIVE" || SUSPENDED != "SUSPENDED" || RETIRED != "RETIRED" {
		t.Error("unexpected AgentState constants")
	}

	_, err := NewAgent("test")
	if err == nil {
		t.Error("NewAgent should return an error stub")
	}

	a := &Agent{ID: "test", State: ACTIVE}
	if err := a.Transition(SUSPENDED); err == nil {
		t.Error("Transition should return an error stub")
	}
}
