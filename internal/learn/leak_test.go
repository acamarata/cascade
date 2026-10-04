// Purpose: leak_test.go -- TestTelemetryNoLongStrings (a real PRAGMA
//
//	introspection of every telemetry column, not a parsed copy of the
//	schema) and TestCredentialCanary (a planted credential-shaped value
//	fails Record closed, and never reaches the table).
//
// SPORT: learn/leak/ADD (P1-E31-W6-S64-T1).
package learn

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// textColumns returns table's TEXT column names via PRAGMA table_info --
// a real introspection of the live schema, not a copy of migration.go's
// own TableDef literal.
func textColumns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	var cols []string
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info row: %v", err)
		}
		if strings.EqualFold(ctype, "TEXT") {
			cols = append(cols, name)
		}
	}
	return cols
}

// assertNoLongStrings scans every row of table's TEXT columns and fails
// on any value longer than 64 characters.
func assertNoLongStrings(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	cols := textColumns(t, db, table)
	if len(cols) == 0 {
		t.Fatalf("table %s: no TEXT columns found -- introspection likely broken", table)
	}
	rows, err := db.Query(`SELECT ` + strings.Join(cols, ",") + ` FROM ` + table)
	if err != nil {
		t.Fatalf("select %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("scan %s row: %v", table, err)
		}
		for i, v := range vals {
			if v.Valid && len(v.String) > 64 {
				t.Errorf("table %s column %s: value %q is %d chars, exceeds the 64-char bound", table, cols[i], v.String, len(v.String))
			}
		}
	}
}

// TestTelemetryNoLongStrings covers both telemetry tables against a real
// seeded row.
func TestTelemetryNoLongStrings(t *testing.T) {
	db := newTestOutcomeDB(t)
	ctx := context.Background()
	ow := NewSQLiteOutcomeWriter(db, newTestClock())
	if err := ow.Record(ctx, baseOutcome("job-nls-1")); err != nil {
		t.Fatalf("seed outcome: %v", err)
	}
	fw := NewSQLiteFindingWriter(db)
	if err := fw.WriteFinding(ctx, Finding{JobID: "job-nls-1", Family: FamilyReview, Category: CategoryStyle, Severity: SeverityLow, Count: 1}); err != nil {
		t.Fatalf("seed finding: %v", err)
	}
	assertNoLongStrings(t, db, tableTelemetryOutcomes)
	assertNoLongStrings(t, db, tableTelemetryFinding)
}

// credentialCanary is a representative AWS-access-key-shaped secret --
// exactly the class of value R-21.152 says must never reach a table.
const credentialCanary = "AKIA" + "ABCDEFGHIJKLMNOP"

// TestCredentialCanary: a planted credential-shaped value in a Record()
// argument fails the call closed, and the row is never written -- the
// canary reaches no table, and (since Record's write and the canary
// check happen before any event/log emission in this package) no event
// or log either.
func TestCredentialCanary(t *testing.T) {
	db := newTestOutcomeDB(t)
	ctx := context.Background()
	w := NewSQLiteOutcomeWriter(db, newTestClock())
	o := baseOutcome("job-canary-1")
	o.RepoID = credentialCanary
	if err := w.Record(ctx, o); err == nil {
		t.Fatal("Record with a credential-shaped RepoID = nil error, want refusal")
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+tableTelemetryOutcomes+` WHERE job_id = ?`, o.JobID).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("got %d jobs_telemetry_outcomes rows after a refused Record, want 0 -- the canary reached the table", count)
	}
	// Sweep every column of every telemetry table (any row) for the
	// canary text, proving it landed nowhere in this package's storage.
	for _, table := range []string{tableTelemetryOutcomes, tableTelemetryFinding} {
		for _, col := range textColumns(t, db, table) {
			var got int
			q := `SELECT COUNT(*) FROM ` + table + ` WHERE ` + col + ` = ?`
			if err := db.QueryRowContext(ctx, q, credentialCanary).Scan(&got); err != nil {
				t.Fatalf("scan %s.%s for canary: %v", table, col, err)
			}
			if got != 0 {
				t.Errorf("credential canary found in %s.%s", table, col)
			}
		}
	}
}

// TestCredentialCanaryEveryStringField proves the gate is not
// single-field-specific: planting the canary in each individual string
// field of TelemetryOutcome refuses Record.
func TestCredentialCanaryEveryStringField(t *testing.T) {
	fields := map[string]func(*TelemetryOutcome){
		"RepoID":            func(o *TelemetryOutcome) { o.RepoID = credentialCanary },
		"Component":         func(o *TelemetryOutcome) { o.Component = credentialCanary },
		"LaneTier":          func(o *TelemetryOutcome) { o.LaneTier = credentialCanary },
		"NodeID":            func(o *TelemetryOutcome) { o.NodeID = credentialCanary },
		"ScopeRef":          func(o *TelemetryOutcome) { o.ScopeRef = credentialCanary },
		"RetrievalStrategy": func(o *TelemetryOutcome) { o.RetrievalStrategy = credentialCanary },
	}
	for name, mutate := range fields {
		db := newTestOutcomeDB(t)
		w := NewSQLiteOutcomeWriter(db, newTestClock())
		o := baseOutcome("job-canary-" + name)
		mutate(&o)
		if err := w.Record(context.Background(), o); err == nil {
			t.Errorf("field %s: Record with a credential-shaped value = nil error, want refusal", name)
		}
	}
}
