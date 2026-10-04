// Purpose: doc.go's package Example -- apply the schema, record one
//
//	accepted outcome, and read it back, demonstrating the documented
//	package usage (Art.10.6).
//
// SPORT: learn/doc/ADD (P1-E31-W6-S64-T1).
package learn_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/learn"
	"github.com/acamarata/cascade/internal/storage/migrate"
)

type docClock struct{ t time.Time }

func (c docClock) Now() time.Time { return c.t }

// Example demonstrates: apply the learn schema, record one accepted
// outcome, and read its final_outcome back.
func Example() {
	db, err := openLearnDBForExample()
	if err != nil {
		fmt.Println("open db error:", err)
		return
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	clock := docClock{t: time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)}
	if err := learn.ApplyLearnSchema(ctx, db, migrate.SQLiteEmitter{}, clock); err != nil {
		fmt.Println("schema error:", err)
		return
	}
	// The jobs_usage table Record's cost/quota join reads (R-16.52) --
	// registerLearnJobs applies this in production too (see this ticket's
	// build report's declared deviation #2).
	if err := conductor.ApplyUsageMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		fmt.Println("usage schema error:", err)
		return
	}

	w := learn.NewSQLiteOutcomeWriter(db, clock)
	o := learn.TelemetryOutcome{
		JobID: "example-job-1", TaskClass: "code", RepoID: "repo-1", Language: learn.LanguageGo,
		Component: "example", RiskClass: "normal", LaneTier: "tier-1", NodeID: "node-1",
		ScopeRef: "scope-1", FinalOutcome: learn.OutcomeAccepted,
	}
	if err := w.Record(ctx, o); err != nil {
		fmt.Println("record error:", err)
		return
	}

	var finalOutcome string
	row := db.QueryRowContext(ctx, `SELECT final_outcome FROM jobs_telemetry_outcomes WHERE job_id = ?`, o.JobID)
	if err := row.Scan(&finalOutcome); err != nil {
		fmt.Println("read error:", err)
		return
	}
	fmt.Println(finalOutcome)
	// Output: accepted
}

// openLearnDBForExample mirrors internal/jobs/doc_test.go's own
// openDBForExample: Example functions have no *testing.T, so
// os.MkdirTemp stands in for t.TempDir.
func openLearnDBForExample() (*sql.DB, error) {
	dir, err := os.MkdirTemp("", "learn-example-*")
	if err != nil {
		return nil, err
	}
	return sql.Open("sqlite", filepath.Join(dir, "example.db"))
}
