//go:build !windows

// Purpose: the composition-root call site for the job.*/lease.* RPC
//
//	surface, over the daemon's one fleet runtime: its one cascade.db
//	handle, *jobs.Store and *jobs.LeaseManager (P1-CAP-10). wireJobRPC
//	opens no database of its own and builds no LeaseManager; it adapts the
//	runtime's into internal/rpc's duck-typed JobStore/LeaseStore/
//	ControllerGuard/OutboxRecorder seams (see internal/rpc/jobs.go's
//	CONTRACT NOTE: internal/rpc cannot import internal/jobs or
//	internal/nodes, both edges are import cycles).
//
// Constraints: this file REGISTERS job.*/lease.* on the daemon's own
//
//	registry and never dials the daemon, the daemon-side reasoning behind
//	its exemption in .golangci.yml's cmd-rpc-server-boundary list and
//	internal/client/boundary_test.go's cmdRPCBoundaryExempt map. The role
//	guard resolves the persisted designation and binding on every call, so
//	a re-designation needs no restart and a role conflict refuses the
//	controller-only verbs by name instead of failing daemon start.
//
// SPORT: cmd/cascade/daemon (ADD, P1-E29-W6-S60-T1; CHANGED, P1-CAP-10).
package main

import (
	"context"
	"database/sql"
	"path/filepath"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// wireJobRPC registers job.*/lease.* against registry over rt's one jobs
// store, LeaseManager and handle. The runtime owns and closes the handle.
// The controller guard resolves the persisted role on every call; a role
// conflict refuses the controller-only verbs and never aborts start.
func wireJobRPC(ctx context.Context, registry *rpc.Registry, rt *daemon.FleetRuntime, clock runtime.Clock, ledger *rpc.NonceLedger, trust rpc.TrustStore) error {
	if rt == nil || rt.Jobs() == nil || rt.Leases() == nil {
		return cascade.New(cascade.KindInvalidInput, "jobs rpc: requires the fleet runtime's jobs store and lease manager")
	}
	rpc.RegisterJobHandlers(registry, rpc.JobHandlerDeps{
		Jobs:   jobStoreAdapter{store: rt.Jobs()},
		Leases: leaseStoreAdapter{store: rt.Jobs(), lease: rt.Leases()},
		Guard:  newRoleGuard(ctx, rt.JobsDB()),
		Outbox: outboxAdapter{db: rt.JobsDB(), clock: clock},
		Ledger: ledger,
		Trust:  trust,
		Clock:  clock,
	})
	return nil
}

// roleGuard implements rpc.ControllerGuard over the file binding backend of
// the runtime's data dir, resolved with nodes.ResolveRole on every call. A
// nil binding resolves to a no-source conflict, so it refuses.
type roleGuard struct {
	binding nodes.ControllerBindingBackend
}

// newRoleGuard binds the guard to the data dir holding the runtime's
// cascade.db. When that path cannot be read the guard keeps a nil binding
// and refuses every controller-only verb instead of failing daemon start.
func newRoleGuard(ctx context.Context, db *sql.DB) roleGuard {
	dbPath, err := jobsDBFilePath(ctx, db)
	if err != nil || dbPath == "" {
		return roleGuard{}
	}
	return roleGuard{binding: nodes.NewFileControllerBindingBackend(filepath.Dir(dbPath))}
}

// jobsDBFilePath reads the main database file of db via PRAGMA
// database_list, the only way to recover a path from a bare *sql.DB.
func jobsDBFilePath(ctx context.Context, db *sql.DB) (string, error) {
	if db == nil {
		return "", cascade.New(cascade.KindInvalidInput, "jobs rpc: no jobs database handle")
	}
	var path string
	err := db.QueryRowContext(ctx, "SELECT file FROM pragma_database_list WHERE name = 'main'").Scan(&path)
	return path, err
}

// RequireController refuses method unless this daemon is the controller
// now: a role conflict is ErrRoleConflict (KindIntegrity) naming it, and a
// node role is nodes.ErrNotController.
func (g roleGuard) RequireController(method string) error {
	role, err := nodes.ResolveRole(g.binding)
	if err != nil {
		return err
	}
	return controllerGuardAdapter{role: role}.RequireController(method)
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
