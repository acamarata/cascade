//go:build !windows

// Purpose: the composition-root call site for AC/S-59.T5's DAG scheduler
//
//	(internal/jobs.Scheduler) — builds the real
//	nodes.ControllerBindingBackend over the daemon's data directory and
//	calls daemon.Manifest.RegisterScheduler with it, real *events.Bus,
//	and real runtime.Clock. Distinct file and name from
//	daemon_unix_scheduler.go, which wires the UNRELATED
//	internal/events/scheduler retention/cron scheduler — the two share a
//	generic name only in the tickets that built them, not in this tree's
//	own type system.
//
// Inputs: the *rpc.Registry-building buildRPCServer's own Manifest,
//
//	runtime.PathProvider and runtime.Clock (every sibling wireX call in
//	daemon_unix_run.go already threads all three through), the real
//	*events.Bus platformDaemonRun constructs, and (DEFECT-scheduler-
//	resume-never-called's fix) the same shared provider.Store
//	buildRPCServer's other handlers read/write, so Resume's journal
//	Reader is bound to the SAME journal every other subsystem here uses,
//	never a second, disconnected one.
//
// Outputs: RegisterScheduler's Manifest entry ("jobs.scheduler") plus its
//
//	background consumer goroutine, started for real on every daemon run,
//	and (as of this fix) a real Resume pass over any `running` job left
//	behind by a prior crash, run once before that consumer starts.
//
// Constraints: nodes.NewFileControllerBindingBackend persists under
//
//	paths.DataDir()/nodes/controller_binding.json — the same real,
//	already-shipped (S-36.T2) file backend cmd/cascade/node_serve.go's
//	enrollment path writes to, so a daemon that has enrolled as a node
//	resolves RoleNode here too, not a second, disconnected notion of role.
//	The jobs.Store Resume needs opens its OWN sqlite connection to
//	paths.DataDir()/cascade.db — daemon_unix_jobs_rpc.go's wireJobRPC
//	already does exactly this for the job.*/lease.* RPC surface, and
//	every schema apply on this file is idempotent by the same contract
//	registerContextEngineHandlers' own doc comment names, so a second
//	connection is the established shape, not a new one.
//
// SPORT: cmd/cascade/daemon (ADD, merge-fix for P1-E29-W6-S59-T5; FIX
// DEFECT-scheduler-resume-never-called).
package main

import (
	"context"
	"database/sql"
	"path/filepath"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// schedulerCursorName is this daemon's fixed bus-subscription cursor name
// for the jobs.lease namespace, distinct from RegisterWorktreeManager's
// own "test-cursor"-shaped names (that one is test-only today; this is
// the first production cursor name on this namespace).
const schedulerCursorName = "jobs-scheduler"

// schedulerResumeHeartbeatInterval is Resume's heartbeatInterval argument.
// DISCLOSED GAP (matches wireResumeScan's unavailableFanOut precedent in
// daemon_resume.go): AF/S-65.T2, the ticket that owns writing
// execution.heartbeat_at, has no production writer anywhere in this tree
// yet (grep across internal/jobs for a heartbeat_at UPDATE outside tests
// returns nothing) — 0 disables reapHeartbeats outright
// (scheduler_resume.go's own "heartbeatInterval <= 0: return nil" guard)
// rather than fabricating an interval for a reaper with nothing to read.
const schedulerResumeHeartbeatInterval = 0

// wireJobScheduler is buildRPCServer's call site for RegisterScheduler. It
// opens its own sqlite connection for the jobs.Store Resume reads/writes
// (mirroring wireJobRPC's identical shape below in
// daemon_unix_jobs_rpc.go) and binds the journal Reader to the SAME
// shared provider.Store buildRPCServer's caller already opened, so a
// stale `running` job from a prior crash is re-entered at `leased` before
// this daemon's scheduler consumer starts serving anything new.
func wireJobScheduler(ctx context.Context, manifest *daemon.Manifest, bus *events.Bus, clock runtime.Clock, paths runtime.PathProvider, store provider.Store) error {
	binding := nodes.NewFileControllerBindingBackend(paths.DataDir())
	jobsStore, closeJobsStore, err := openSchedulerResumeJobsStore(ctx, paths, clock)
	if err != nil {
		return err
	}
	defer closeJobsStore()
	var js journal.Store
	if store != nil {
		js = journal.New(store, clock, journal.DefaultNamespace)
	}
	_, err = manifest.RegisterScheduler(ctx, bus, clock, binding, schedulerCursorName, jobsStore, js, schedulerResumeHeartbeatInterval)
	return err
}

// openSchedulerResumeJobsStore opens the jobs domain's own db handle over
// the SAME cascade.db file wireJobRPC uses and applies its schemas — the
// jobs schema for Resume's own store reads/writes, and the outbox schema
// since Resume's reconcileOutbox step queries jobs_outbox unconditionally.
// The returned closer is intentionally NOT deferred by the daemon's own
// lifetime (this connection lives only for the duration of the startup
// Resume call, unlike wireJobRPC's, which the RPC surface holds open for
// the process's life) — see wireJobRPC's own doc comment for that
// disclosed "not tracked for close" gap, which this function does not
// repeat: it closes its own connection as soon as Resume returns.
func openSchedulerResumeJobsStore(ctx context.Context, paths runtime.PathProvider, clock runtime.Clock) (*jobs.Store, func(), error) {
	dbPath := filepath.Join(paths.DataDir(), "cascade.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		return nil, nil, cascade.Wrapf(cascade.KindUnavailable, err, "scheduler resume: open %s", dbPath)
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
	return jobs.NewStore(db), func() { _ = db.Close() }, nil
}
