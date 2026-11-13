package db

import "testing"

// TestSchemaConstantsExist verifies that all required table name constants,
// phase status values, and migration SQL strings are defined and non-empty.
func TestSchemaConstantsExist(t *testing.T) {
	tables := []struct {
		name  string
		value string
	}{
		{"TableMeta", TableMeta},
		{"TablePlanSource", TablePlanSource},
		{"TablePhase", TablePhase},
		{"TableTicket", TableTicket},
		{"TableEdge", TableEdge},
		{"TableContractRef", TableContractRef},
		{"TableAttempt", TableAttempt},
		{"TableLease", TableLease},
		{"TableLanding", TableLanding},
		{"TablePark", TablePark},
		{"TableRun", TableRun},
		{"TableEvent", TableEvent},
	}
	for _, tc := range tables {
		if tc.value == "" {
			t.Errorf("table constant %s is empty", tc.name)
		}
	}

	views := []struct {
		name  string
		value string
	}{
		{"ViewTicketState", ViewTicketState},
		{"ViewOpenTicket", ViewOpenTicket},
		{"ViewBlockedBy", ViewBlockedBy},
		{"ViewStaleLeases", ViewStaleLeases},
		{"ViewLiveLease", ViewLiveLease},
		{"ViewReadyFrontier", ViewReadyFrontier},
	}
	for _, v := range views {
		if v.value == "" {
			t.Errorf("view constant %s is empty", v.name)
		}
	}

	statuses := []struct {
		name  string
		value string
	}{
		{"PhaseSeeded", PhaseSeeded},
		{"PhaseRTP", PhaseRTP},
		{"PhaseRTF", PhaseRTF},
		{"PhaseRTB", PhaseRTB},
		{"PhaseRTD", PhaseRTD},
		{"PhaseFinished", PhaseFinished},
		{"PhaseInProgress", PhaseInProgress},
	}
	for _, s := range statuses {
		if s.value == "" {
			t.Errorf("phase status constant %s is empty", s.name)
		}
	}

	expectedStatuses := map[string]bool{
		"seeded": false, "rtp": false, "rtf": false, "rtb": false,
		"rtd": false, "finished": false, "in_progress": false,
	}
	for _, s := range statuses {
		if _, ok := expectedStatuses[s.value]; ok {
			expectedStatuses[s.value] = true
		}
	}
	for k, v := range expectedStatuses {
		if !v {
			t.Errorf("missing expected phase status value: %s", k)
		}
	}

	migrations := []struct {
		name  string
		value string
	}{
		{"Migration001", Migration001},
		{"Migration002", Migration002},
		{"Migration003", Migration003},
		{"Migration004", Migration004},
		{"Migration005", Migration005},
		{"Migration006", Migration006},
	}
	for _, m := range migrations {
		if m.value == "" {
			t.Errorf("migration constant %s is empty", m.name)
		}
	}
}
