package db

import (
	"errors"
	"testing"
)

// TestSchemaConstantsExist verifies that all required table name constants,
// phase status values, and migration SQL strings are defined and non-empty.
// namedConst is one schema constant and the name its test reports.
type namedConst struct {
	name  string
	value string
}

// assertNonEmpty fails for every constant in cs whose value is empty.
func assertNonEmpty(t *testing.T, kind string, cs []namedConst) {
	t.Helper()
	for _, c := range cs {
		if c.value == "" {
			t.Errorf("%s constant %s is empty", kind, c.name)
		}
	}
}

func TestSchemaTableConstantsExist(t *testing.T) {
	assertNonEmpty(t, "table", []namedConst{
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
	})
}

func TestSchemaViewConstantsExist(t *testing.T) {
	assertNonEmpty(t, "view", []namedConst{
		{"ViewTicketState", ViewTicketState},
		{"ViewOpenTicket", ViewOpenTicket},
		{"ViewBlockedBy", ViewBlockedBy},
		{"ViewStaleLeases", ViewStaleLeases},
		{"ViewLiveLease", ViewLiveLease},
		{"ViewReadyFrontier", ViewReadyFrontier},
	})
}

func phaseStatusConsts() []namedConst {
	return []namedConst{
		{"PhaseSeeded", PhaseSeeded},
		{"PhaseRTP", PhaseRTP},
		{"PhaseRTF", PhaseRTF},
		{"PhaseRTB", PhaseRTB},
		{"PhaseRTD", PhaseRTD},
		{"PhaseFinished", PhaseFinished},
		{"PhaseInProgress", PhaseInProgress},
	}
}

func TestSchemaPhaseStatusConstantsExist(t *testing.T) {
	statuses := phaseStatusConsts()
	assertNonEmpty(t, "phase status", statuses)
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
}

func TestSchemaMigrationConstantsExist(t *testing.T) {
	assertNonEmpty(t, "migration", []namedConst{
		{"Migration001", Migration001},
		{"Migration002", Migration002},
		{"Migration003", Migration003},
		{"Migration004", Migration004},
		{"Migration005", Migration005},
		{"Migration006", Migration006},
	})
}

// TestMigrateStubReturnsErrNotImplemented asserts that Migrate is stubbed and returns ErrNotImplemented.
func TestMigrateStubReturnsErrNotImplemented(t *testing.T) {
	err := Migrate(nil)
	if !errors.Is(err, ErrNotImplemented) {
		t.Errorf("Migrate(nil) = %v, want ErrNotImplemented", err)
	}
}
