package jobs_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/storage/migrate"
)

type docClock struct{ t time.Time }

func (c docClock) Now() time.Time { return c.t }

// Example demonstrates the documented package usage (Art.10.6): apply
// the schema, put a job, and drive it through one legal transition.
func Example() {
	db, err := openDBForExample()
	if err != nil {
		fmt.Println("open db error:", err)
		return
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	clock := docClock{t: time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)}
	if err := jobs.ApplyJobsSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		fmt.Println("schema error:", err)
		return
	}

	store := jobs.NewStore(db)
	j := jobs.Job{
		ID: "job-example-1", State: jobs.JobStatePending,
		CreatedAt: 1, UpdatedAt: 1, Capabilities: []string{"code"},
		MutableScope: "repo:/tmp/example", RiskClass: "normal", MinTaskClass: "code",
		NodeRequirements: "{}", TimeoutSeconds: 60, CostCeiling: 1,
		Priority: 1, ConsequenceClass: jobs.ConsequenceNormal, DataClass: jobs.DataClassInternal,
	}
	if err := store.PutJob(ctx, j); err != nil {
		fmt.Println("put job error:", err)
		return
	}
	if err := store.PutTransition(ctx, j.ID, jobs.JobStateLeased, 2); err != nil {
		fmt.Println("transition error:", err)
		return
	}
	got, ok, err := store.GetJob(ctx, j.ID)
	if err != nil || !ok {
		fmt.Println("get job error:", err, ok)
		return
	}
	fmt.Println(got.State)
	// Output: leased
}

// openDBForExample opens a real modernc-sqlite database file under a
// fresh temp directory (Example functions have no *testing.T, so
// os.MkdirTemp stands in for t.TempDir here; the directory is not
// cleaned up, matching the standard library's own Example convention of
// leaving ephemeral scratch state for the test binary's process
// lifetime).
func openDBForExample() (*sql.DB, error) {
	dir, err := os.MkdirTemp("", "jobs-example-*")
	if err != nil {
		return nil, err
	}
	return sql.Open("sqlite", filepath.Join(dir, "example.db"))
}
