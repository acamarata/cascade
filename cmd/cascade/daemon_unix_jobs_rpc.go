//go:build !windows

// Purpose: the composition-root call site for P1-E29-W6-S60-T1's
//
//	job.*/lease.* RPC surface. Opens a second sqlite connection to the
//	daemon's own cascade.db (registerContextEngineHandlers' already-
//	blessed pattern for a subsystem that needs its own schema applied
//	there), applies jobs.ApplyJobsSchema + jobs.ApplyOutboxSchema,
//	constructs a real *jobs.Store/*jobs.LeaseManager, resolves this
//	daemon's nodes.Role once, and adapts all four into internal/rpc's
//	duck-typed JobStore/LeaseStore/ControllerGuard/OutboxRecorder seams
//	(see internal/rpc/jobs.go's CONTRACT NOTE for why those seams exist:
//	internal/rpc cannot import internal/jobs or internal/nodes directly —
//	both edges are real import cycles).
//
// CONTRACT NOTE (files_scope, quoted in the journal): this file, and the
// two-line call this file's wireJobRPC needs from buildRPCServer
// (daemon_unix_run.go), are not named in this ticket's files_scope. Per
// the AGENT-BRIEF's own instruction ("files_scope is INTENT, not proof
// the paths exist... find the REAL composition root... a registered RPC
// method must be MOUNTED at the composition root"), this file exists
// because job.list/show/cancel/retry and lease.list/release would
// otherwise ship built, tested and unreachable from any real daemon —
// exactly the pattern R-14.166/R-14.223 forbid. Named and exempted in
// .golangci.yml's cmd-rpc-server-boundary list and
// internal/client/boundary_test.go's cmdRPCBoundaryExempt map (edited
// together, per the AGENT-BRIEF): this file REGISTERS job.*/lease.* on
// the daemon's own registry and never dials the daemon, the same
// daemon-SIDE reasoning daemon_unix_conductor.go and
// daemon_unix_evidence.go already carry their own exemptions under.
//
// SPORT: cmd/cascade/daemon (ADD, P1-E29-W6-S60-T1).
package main

import (
	"context"
	"database/sql"
	"path/filepath"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// wireJobRPC opens the jobs domain's own db handle, applies its schemas,
// builds the real Store/LeaseManager, resolves this daemon's Role, and
// registers job.*/lease.* against registry. Returns the opened *sql.DB so
// the caller can close it on shutdown alongside its other db handles.
func wireJobRPC(ctx context.Context, registry *rpc.Registry, paths runtime.PathProvider, clock runtime.Clock, ledger *rpc.NonceLedger, trust rpc.TrustStore) (*sql.DB, error) {
	dbPath := filepath.Join(paths.DataDir(), "cascade.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "jobs rpc: open %s", dbPath)
	}
	db.SetMaxOpenConns(1)
	// runtime.Clock and migrate.Clock are structurally identical (both
	// declare exactly Now() time.Time — migrate/clock.go's own doc
	// comment names this as deliberate), so clock satisfies migrate.Clock
	// with no adapter.
	if err := jobs.ApplyJobsSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := jobs.ApplyOutboxSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		_ = db.Close()
		return nil, err
	}

	store := jobs.NewStore(db)
	leaseMgr := jobs.NewLeaseManager(store, clock, func() bool { return true }, jobs.DefaultLeaseDefaults(), nil)
	store.SetTerminalHook(nil)

	binding := nodes.NewFileControllerBindingBackend(paths.DataDir())
	role, err := nodes.ResolveRole(binding)
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	rpc.RegisterJobHandlers(registry, rpc.JobHandlerDeps{
		Jobs:   jobStoreAdapter{store: store},
		Leases: leaseStoreAdapter{store: store, lease: leaseMgr},
		Guard:  controllerGuardAdapter{role: role},
		Outbox: outboxAdapter{db: db, clock: clock},
		Ledger: ledger,
		Trust:  trust,
		Clock:  clock,
	})
	return db, nil
}

// jobRPCGuardedMethods names the two job.*/lease.* verbs this daemon
// guards with nodes.RequireController: job.cancel maps to the
// "job.advance" guarded name (its only production entry into that
// authority) and lease.release maps to itself. Both MUST be members of
// the real, production nodes.GuardedMethods slice — assertGuardedMethods
// panics at startup, not at request time, if that ever drifts, so a
// typo here fails loudly instead of silently un-guarding an authority
// verb.
var jobRPCGuardedMethods = []string{"job.advance", "lease.release"}

func init() {
	assertGuardedMethods(jobRPCGuardedMethods, nodes.GuardedMethods)
}

// assertGuardedMethods panics unless every entry in want is present in
// realMethods (internal/nodes.GuardedMethods) — the production reference
// this ticket's testonly-allow retirement names as the missing caller.
func assertGuardedMethods(want, realMethods []string) {
	for _, w := range want {
		found := false
		for _, r := range realMethods {
			if r == w {
				found = true
				break
			}
		}
		if !found {
			panic("cmd/cascade: jobRPCGuardedMethods names " + w + ", which is not in nodes.GuardedMethods")
		}
	}
}
