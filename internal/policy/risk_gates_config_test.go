package policy

import "testing"

// TestParseRiskGates_Absent asserts an absent risk_gates key leaves
// Config.RiskGates nil.
func TestParseRiskGates_Absent(t *testing.T) {
	cfg, err := ParseConfig(policyTree(map[string]interface{}{}))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.RiskGates != nil {
		t.Errorf("RiskGates = %v, want nil", cfg.RiskGates)
	}
}

// TestParseRiskGates_Valid asserts a well-formed risk_gates table
// decodes into the raw class -> gate-name-list map.
func TestParseRiskGates_Valid(t *testing.T) {
	tree := policyTree(map[string]interface{}{
		"risk_gates": map[string]interface{}{
			"low":      []interface{}{"human_approval"},
			"critical": []interface{}{"format", "static"},
		},
	})
	cfg, err := ParseConfig(tree)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if len(cfg.RiskGates["low"]) != 1 || cfg.RiskGates["low"][0] != "human_approval" {
		t.Errorf("RiskGates[low] = %v, want [human_approval]", cfg.RiskGates["low"])
	}
	if len(cfg.RiskGates["critical"]) != 2 {
		t.Errorf("RiskGates[critical] = %v, want 2 entries", cfg.RiskGates["critical"])
	}
}

// TestParseRiskGates_UnknownClass asserts an unrecognised risk-class
// key is a typed refusal, never a silently dropped entry.
func TestParseRiskGates_UnknownClass(t *testing.T) {
	tree := policyTree(map[string]interface{}{
		"risk_gates": map[string]interface{}{
			"extreme": []interface{}{"format"},
		},
	})
	if _, err := ParseConfig(tree); err == nil {
		t.Fatal("ParseConfig with an unknown risk_gates class: expected an error, got nil")
	}
}

// TestParseRiskGates_NotATable asserts risk_gates itself must be a table.
func TestParseRiskGates_NotATable(t *testing.T) {
	tree := policyTree(map[string]interface{}{"risk_gates": "not-a-table"})
	if _, err := ParseConfig(tree); err == nil {
		t.Fatal("ParseConfig with risk_gates as a string: expected an error, got nil")
	}
}

// TestParseRiskGates_ValueNotAList asserts a class's value must be a list.
func TestParseRiskGates_ValueNotAList(t *testing.T) {
	tree := policyTree(map[string]interface{}{
		"risk_gates": map[string]interface{}{"low": "human_approval"},
	})
	if _, err := ParseConfig(tree); err == nil {
		t.Fatal("ParseConfig with a non-list risk_gates value: expected an error, got nil")
	}
}

// TestParseRiskGates_EntryNotAString asserts every list entry must be a
// string.
func TestParseRiskGates_EntryNotAString(t *testing.T) {
	tree := policyTree(map[string]interface{}{
		"risk_gates": map[string]interface{}{"low": []interface{}{42}},
	})
	if _, err := ParseConfig(tree); err == nil {
		t.Fatal("ParseConfig with a non-string risk_gates entry: expected an error, got nil")
	}
}

// TestParseRiskGates_EmptyEntry asserts an empty-string entry refuses
// rather than silently carrying a blank gate name forward.
func TestParseRiskGates_EmptyEntry(t *testing.T) {
	tree := policyTree(map[string]interface{}{
		"risk_gates": map[string]interface{}{"low": []interface{}{"  "}},
	})
	if _, err := ParseConfig(tree); err == nil {
		t.Fatal("ParseConfig with an empty risk_gates entry: expected an error, got nil")
	}
}
