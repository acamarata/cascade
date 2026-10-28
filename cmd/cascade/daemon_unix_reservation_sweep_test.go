//go:build !windows

// Purpose: the composed daemon's start sweep: a dead epoch's held row is
//
//	rolled back with its lease released and its committed row adopted
//	before the socket serves, and a live foreign pidfile refuses the start
//	while this process's own pidfile does not.
//
// SPORT: cmd/cascade/daemon (ADD, P1-CAP-10).
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

const deadEpoch = "daemon-dead-epoch"

// seedForeignReservations writes, under paths' cascade.db, a held row of a
// dead epoch with its granted repo lease and a committed row of the same
// epoch. It returns the held row's lease.
func seedForeignReservations(t *testing.T, paths runtime.PathProvider) jobs.ResourceLease {
	t.Helper()
	ctx, clock := context.Background(), runtime.NewSystemClock()
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(paths.DataDir(), "cascade.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	for _, apply := range []func(context.Context, *sql.DB, migrate.Dialect, migrate.Clock, string, string) error{jobs.ApplyJobsSchema, economics.ApplyReservationSchema} {
		if err := apply(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
			t.Fatalf("apply schema: %v", err)
		}
	}
	got, err := jobs.NewLeaseManager(jobs.NewStore(db), clock, func() bool { return true }, jobs.DefaultLeaseDefaults(), nil).Acquire(ctx, "repo-sweep", "held/**", "job-held")
	if err != nil || !got.Granted {
		t.Fatalf("seed Acquire = %+v, %v", got, err)
	}
	store, err := economics.NewReservationStore(db, clock)
	if err != nil {
		t.Fatalf("NewReservationStore: %v", err)
	}
	handle := jobs.LeaseID(got.Lease) + "@" + strconv.FormatInt(got.Lease.Epoch, 10)
	now := clock.Now().Unix()
	for _, r := range []economics.Reservation{
		{ID: "res-held", ExecutionID: "exec-held", JobID: "job-held", RepoID: "repo-sweep", ScopeGlobs: []string{"held/**"},
			Kind: economics.ReservationInteractive, State: economics.ReservationHeld, OwnerEpoch: deadEpoch, HeartbeatAt: now, LeaseIDs: []string{handle},
			Steps: []economics.Step{{Step: economics.StepLeases, IdempotencyKey: "res-held:leases", Handle: handle, State: economics.StepAcquired}}},
		{ID: "res-commit", ExecutionID: "exec-commit", JobID: "job-commit", Kind: economics.ReservationBatch,
			State: economics.ReservationCommitted, OwnerEpoch: deadEpoch, HeartbeatAt: now},
	} {
		if _, err := store.Insert(ctx, r); err != nil {
			t.Fatalf("Insert(%s): %v", r.ID, err)
		}
	}
	return got.Lease
}

// TestDaemonStartSweepRollsBackForeignHeld: a seeded dead epoch is swept
// during composition, before the socket serves.
func TestDaemonStartSweepRollsBackForeignHeld(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	deps := newRunTestDeps(t, nil)
	lease := seedForeignReservations(t, deps.Paths)
	seenAtCompose := make(chan economics.ReservationState, 1)
	addTestRegistration(t, daemonRegistration{Name: "test-sweep-before-serve", Phase: phaseMCPLast + 91, Order: 1,
		Wire: func(w *daemonWiring) error {
			r, _, err := reservationStore(t, w.Deps.Fleet).Get(context.Background(), "res-held")
			seenAtCompose <- r.State
			return err
		}})
	w, cancel, done := startTestDaemon(t, deps, observeWiring(t))
	t.Cleanup(func() { cancel(); awaitDaemonReturn(t, done) })
	if st := <-seenAtCompose; st != economics.ReservationRolledBack {
		t.Fatalf("held row at composition = %q, want rolled_back before the socket serves", st)
	}
	rt, ctx := w.Deps.Fleet, context.Background()
	if l, _, err := rt.Jobs().GetLease(ctx, lease.RepoID, lease.ScopeGlob); err != nil || l.State != jobs.LeaseReleased {
		t.Fatalf("held row's lease = %+v, %v; want released", l, err)
	}
	if c, _, err := reservationStore(t, rt).Get(ctx, "res-commit"); err != nil || c.State != economics.ReservationCommitted || c.OwnerEpoch != rt.OwnerEpoch() {
		t.Fatalf("committed row = %+v, %v; want committed, adopted by epoch %q", c, err, rt.OwnerEpoch())
	}
	row := subsystem(t, w, "reservation-sweep")
	for _, want := range []string{"rolled_back=1", "adopted=1", "owner_epoch=" + rt.OwnerEpoch()} {
		if row.State != daemon.SubsystemRunning || !strings.Contains(row.Detail, want) {
			t.Fatalf("reservation-sweep row = %+v, want running with %q", row, want)
		}
	}
}

// reservationStore wraps the runtime's one handle for read-backs.
func reservationStore(t *testing.T, rt *daemon.FleetRuntime) *economics.ReservationStore {
	t.Helper()
	s, err := economics.NewReservationStore(rt.JobsDB(), runtime.NewSystemClock())
	if err != nil {
		t.Fatalf("NewReservationStore: %v", err)
	}
	return s
}

// writePidfile records pid with its real start time as the daemon pidfile.
func writePidfile(t *testing.T, paths runtime.PathProvider, pid int) {
	t.Helper()
	started, ok := daemon.NewProber().StartTime(pid)
	if !ok {
		started = time.Now()
	}
	raw, _ := json.Marshal(map[string]any{"pid": pid, "started_at": started})
	if err := os.MkdirAll(paths.Root(), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(daemon.PIDFilePath(paths), raw, 0o600); err != nil {
		t.Fatalf("write pidfile: %v", err)
	}
}

// TestFleetRuntimeRefusesConcurrentDaemon: a live foreign pidfile (the
// test runner, this process's parent) refuses the start with
// ErrConcurrentDaemon and sweeps nothing.
func TestFleetRuntimeRefusesConcurrentDaemon(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	deps := newRunTestDeps(t, nil)
	seedForeignReservations(t, deps.Paths)
	writePidfile(t, deps.Paths, os.Getppid())
	_, cleanups, err := composeDaemon(context.Background(), deps, nil)
	runCleanupsLIFO(cleanups)
	if !hasErrIdentity(err, economics.ErrConcurrentDaemon) {
		t.Fatalf("composeDaemon = %v, want ErrConcurrentDaemon", err)
	}
	if st := heldRowState(t, deps.Paths); st != string(economics.ReservationHeld) {
		t.Fatalf("held row after a refused start = %q, want held (nothing swept)", st)
	}
}

// TestFleetRuntimeRefusesCorruptPidfile: a pidfile that cannot be read is
// never taken as "no other daemon": the start is refused, nothing swept.
func TestFleetRuntimeRefusesCorruptPidfile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	deps := newRunTestDeps(t, nil)
	seedForeignReservations(t, deps.Paths)
	if err := os.WriteFile(daemon.PIDFilePath(deps.Paths), []byte("{"), 0o600); err != nil {
		t.Fatalf("write pidfile: %v", err)
	}
	_, cleanups, err := composeDaemon(context.Background(), deps, nil)
	runCleanupsLIFO(cleanups)
	if !cascade.HasKind(err, cascade.KindIntegrity) || !strings.Contains(err.Error(), "corrupt pidfile") || heldRowState(t, deps.Paths) != string(economics.ReservationHeld) {
		t.Fatalf("composeDaemon with a corrupt pidfile = %v, want a refusal that sweeps nothing", err)
	}
}

// heldRowState reads res-held's state through a fresh handle.
func heldRowState(t *testing.T, paths runtime.PathProvider) string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(paths.DataDir(), "cascade.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	var state string
	if err := db.QueryRow(`SELECT state FROM jobs_reservation WHERE id = 'res-held'`).Scan(&state); err != nil {
		t.Fatalf("read res-held: %v", err)
	}
	return state
}

// TestFleetRuntimeOwnPidfileIsNotConcurrent: a pidfile naming only this
// process (an exec relaunch keeps its pid) does not block the sweep.
func TestFleetRuntimeOwnPidfileIsNotConcurrent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	deps := newRunTestDeps(t, nil)
	seedForeignReservations(t, deps.Paths)
	writePidfile(t, deps.Paths, os.Getpid())
	_, cleanups, err := composeDaemon(context.Background(), deps, nil)
	defer runCleanupsLIFO(cleanups)
	if err != nil {
		t.Fatalf("composeDaemon with only its own pidfile = %v, want the sweep to run", err)
	}
	if state := heldRowState(t, deps.Paths); state != string(economics.ReservationRolledBack) {
		t.Fatalf("held row = %q; want rolled_back by the start sweep", state)
	}
}

// hasErrIdentity walks err's chain for target itself (cascade errors of one
// Kind compare equal under errors.Is).
func hasErrIdentity(err, target error) bool {
	for ; err != nil; err = errors.Unwrap(err) {
		if err == target {
			return true
		}
	}
	return false
}
