package daemon

// Purpose (this file): the AC/S-59.T1 jobs-domain source for
// status.widget's active_jobs_count (R-16.50) — split out of
// status_widget.go under the repo's 300-line file cap.
//
// Opens its OWN sqlite connection to the daemon's cascade.db, mirroring
// cmd/cascade/daemon_unix_jobs_rpc.go's wireJobRPC and
// daemon_unix_scheduler_dag.go's openSchedulerResumeJobsStore precedent
// exactly ("a second connection is a documented no-op after the first
// opens it", registerContextEngineHandlers' own doc comment) — this
// package (internal/daemon) already imports internal/jobs directly
// (subsystems_scheduler.go), so no cmd/cascade-side adapter file is
// needed the way daemon_unix_jobs_rpc.go's own CONTRACT NOTE requires for
// internal/rpc (a real internal/jobs -> internal/rpc import cycle that
// does not exist between internal/jobs and internal/daemon).
//
// "Active" (recorded, not defined by the ticket text): a job whose State
// is JobStateLeased or JobStateRunning — claimed or executing work, the
// two states a human reading "how many jobs are active right now" would
// expect to count. Pending/verifying/reviewing/terminal states are not
// counted.
//
// Inputs: the daemon's PathProvider and Clock.
// Outputs: openWidgetJobsStore returns a live counter closure plus a
// closer; activeJobsCount(ctx) returns the current count or a typed
// error.
// SPORT: daemon.status_widget.jobs (ADD, P1-E38-W8-S74-T1).

import (
	"context"
	"database/sql"
	"path/filepath"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// activeJobStates is the closed set of JobState values this ticket counts
// as "active" — see this file's header note.
var activeJobStates = []jobs.JobState{jobs.JobStateLeased, jobs.JobStateRunning}

// widgetJobsCounter is the seam StatusWidgetDeps.activeJobsCount closes
// over — exactly *jobs.Store's own ListJobs, duck-typed so tests supply a
// fake without a real sqlite file.
type widgetJobsCounter interface {
	ListJobs(ctx context.Context, filter jobs.JobFilter) ([]jobs.Job, string, error)
}

// countActiveJobs sums ListJobs across every activeJobStates value,
// paginating each state fully (never truncating at one page — an
// undercount would be a fabricated number, exactly what this ticket's
// never-fabricate rule forbids).
func countActiveJobs(ctx context.Context, store widgetJobsCounter) (int, error) {
	total := 0
	for _, state := range activeJobStates {
		cursor := ""
		for {
			page, next, err := store.ListJobs(ctx, jobs.JobFilter{State: state, Cursor: cursor})
			if err != nil {
				return 0, err
			}
			total += len(page)
			if next == "" {
				break
			}
			cursor = next
		}
	}
	return total, nil
}

// openWidgetJobsStore opens the daemon's own second connection to
// cascade.db (see this file's header), applies the jobs domain's schemas,
// and returns an activeJobsCount closure plus a closer the caller must
// invoke on shutdown (RegisterStatusWidgetHandler's caller today does not
// track per-registration close hooks either — see
// daemon_unix_scheduler_dag.go's wireJobScheduler's identical disclosed
// gap: "lives for the daemon process's lifetime and is reclaimed on
// process exit").
func openWidgetJobsStore(ctx context.Context, paths runtime.PathProvider, clock runtime.Clock) (func(context.Context) (*int, error), func() error, error) {
	dbPath := filepath.Join(paths.DataDir(), "cascade.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		return nil, nil, cascade.Wrapf(cascade.KindUnavailable, err, "status.widget: open %s", dbPath)
	}
	db.SetMaxOpenConns(1)
	if err := jobs.ApplyJobsSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	if err := jobs.ApplyOutboxSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	store := jobs.NewStore(db)
	counter := func(ctx context.Context) (*int, error) {
		n, err := countActiveJobs(ctx, store)
		if err != nil {
			return nil, err
		}
		return &n, nil
	}
	return counter, db.Close, nil
}
