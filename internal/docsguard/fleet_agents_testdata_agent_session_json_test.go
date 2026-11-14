package docsguard

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestAgentRPCFixture verifies that the agent-session.json fixture
// and its documentation exist and match the contract schema.
func TestAgentRPCFixture(t *testing.T) {
	fixturePath := "../fleet/agents/testdata/agent-session.json"
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("failed to read agent-session.json: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("agent-session.json is empty")
	}

	var records []map[string]json.RawMessage
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatalf("agent-session.json is not a valid JSON array: %v", err)
	}

	hasGet := false
	hasList := false
	for _, rec := range records {
		var req struct {
			Method string `json:"method"`
		}
		if raw, ok := rec["request"]; ok {
			_ = json.Unmarshal(raw, &req)
		}
		if strings.HasSuffix(req.Method, "agent.get") {
			hasGet = true
		}
		if strings.HasSuffix(req.Method, "agent.list") {
			hasList = true
		}
		if _, ok := rec["provenance"]; !ok {
			t.Errorf("record for %s lacks provenance stamp", req.Method)
		}
	}
	if !hasGet {
		t.Error("agent-session.json lacks agent.get record")
	}
	if !hasList {
		t.Error("agent-session.json lacks agent.list record")
	}

	readmePath := "../fleet/agents/testdata/README.md"
	readme, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("failed to read README.md: %v", err)
	}
	if len(readme) == 0 {
		t.Fatal("README.md is empty")
	}
	readmeStr := string(readme)
	if !strings.Contains(readmeStr, "fixture") && !strings.Contains(readmeStr, "Fixture") {
		t.Error("README.md does not document fixture format")
	}
	if !strings.Contains(readmeStr, "replay") && !strings.Contains(readmeStr, "Replay") && !strings.Contains(readmeStr, "usage") && !strings.Contains(readmeStr, "Usage") {
		t.Error("README.md does not document usage/replay instructions")
	}
}
