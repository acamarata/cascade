package agents

import (
	"errors"
	"testing"
)

func TestAgentDeclarationsCompile(t *testing.T) {
	var state AgentState
	if state != "" {
		t.Errorf("expected zero value for AgentState, got %q", state)
	}
	var agent Agent
	if agent.ID != "" || agent.State != "" {
		t.Error("expected zero values for Agent struct")
	}
	if errInvalidAgentState == nil || errAgentStateConflict == nil {
		t.Fatal("expected sentinels to be initialized")
	}
	if errors.Is(errInvalidAgentState, errAgentStateConflict) {
		t.Error("the two agent errors must be distinct")
	}
	if ACTIVE != "ACTIVE" || SUSPENDED != "SUSPENDED" || RETIRED != "RETIRED" {
		t.Error("unexpected AgentState constants")
	}
}
