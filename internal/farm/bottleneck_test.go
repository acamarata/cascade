package farm

import "testing"

// TestTypesCompile_FARM26 verifies that the bottleneck report types and the
// seven limit-kind constants exist and can be used for read-only reporting.
func TestTypesCompile_FARM26(t *testing.T) {
	kinds := []LimitKind{
		GC_RPM_Headroom,
		GC_Stream_Ceiling,
		Node_Load,
		Swap,
		Account_Quota_Left,
		Local_Model_RAM,
		Queue_Wait,
	}

	if len(kinds) != 7 {
		t.Fatalf("expected 7 limit kinds, got %d", len(kinds))
	}

	seen := make(map[LimitKind]bool, len(kinds))
	for _, kind := range kinds {
		if seen[kind] {
			t.Fatalf("duplicate limit kind %d", int(kind))
		}
		seen[kind] = true
	}

	evidence := Evidence{
		UsageLogTable:  "usage-log-table",
		BrokerSnapshot: "broker-snapshot",
	}
	if evidence.UsageLogTable == nil {
		t.Fatal("Evidence.UsageLogTable must be assignable")
	}
	if evidence.BrokerSnapshot == nil {
		t.Fatal("Evidence.BrokerSnapshot must be assignable")
	}

	row := BottleneckRow{
		Kind:     GC_RPM_Headroom,
		Value:    12.5,
		Unit:     "req/min",
		Source:   "usage-log",
		Measured: true,
	}
	if row.Kind != GC_RPM_Headroom {
		t.Fatal("BottleneckRow.Kind must hold the assigned limit kind")
	}
	if row.Value != 12.5 {
		t.Fatal("BottleneckRow.Value must hold the assigned float64")
	}

	report := BottleneckReport{
		Rows: []BottleneckRow{row},
	}
	if len(report.Rows) != 1 {
		t.Fatal("BottleneckReport.Rows must hold the assigned slice")
	}
}
