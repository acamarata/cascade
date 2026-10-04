// Purpose: a table with a foreign key to jobs_telemetry_outcomes that is
//
//	registered by age (RegisterRetentionTable) instead of as a child is
//	refused at its first sweep, before any delete, and the refusal names
//	RegisterRetentionChild.
//
// SPORT: learn/retention-fkguard/ADD (P1-CAP-02).
package learn

import (
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestRetentionSweepRefusesFKTableRegisteredByAge: the misregistered
// dependent table makes the sweep return KindInvalidInput naming
// RegisterRetentionChild, with every row still stored; the same table with
// no foreign key sweeps.
func TestRetentionSweepRefusesFKTableRegisteredByAge(t *testing.T) {
	const fkTable, plainTable = "jobs_ext_aged_fk_p1_cap_02", "jobs_ext_aged_plain_p1_cap_02"
	now := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	old := now.AddDate(0, 0, -100).Unix()
	for _, tc := range []struct {
		table, fk string
		refuse    bool
	}{
		{fkTable, ` REFERENCES ` + tableTelemetryOutcomes + `(id)`, true},
		{plainTable, ``, false},
	} {
		db := newFKDB(t)
		if _, err := db.Exec(`CREATE TABLE ` + tc.table +
			` (id INTEGER PRIMARY KEY, created_at INTEGER NOT NULL, outcome_id INTEGER NOT NULL` + tc.fk + `)`); err != nil {
			t.Fatalf("create %s: %v", tc.table, err)
		}
		seedWithChildren(t, db, "job-old-1", old)
		if _, err := db.Exec(`INSERT INTO `+tc.table+` (created_at, outcome_id) SELECT ?, id FROM `+
			tableTelemetryOutcomes, old); err != nil {
			t.Fatalf("seed %s: %v", tc.table, err)
		}
		reg := newLearnRegistry(t)
		if err := reg.addTable(tc.table, "created_at"); err != nil {
			t.Fatalf("addTable %s refused at registration: %v", tc.table, err)
		}
		n, err := sweepWith(t, db, reg, now)
		if !tc.refuse {
			if err != nil || countRows(t, db, tc.table) != 0 {
				t.Errorf("%s: sweep n=%d err=%v rows=%d, want a clean sweep", tc.table, n, err, countRows(t, db, tc.table))
			}
			continue
		}
		if err == nil || !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(err.Error(), "RegisterRetentionChild") {
			t.Fatalf("%s: sweep n=%d err=%v, want KindInvalidInput naming RegisterRetentionChild", tc.table, n, err)
		}
		assertCounts(t, db, 1, 1, 1)
		if got := countRows(t, db, tc.table); got != 1 {
			t.Errorf("%s rows = %d after a refused sweep, want 1", tc.table, got)
		}
	}
}
