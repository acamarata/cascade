//go:build !windows

// Purpose: the job.*/lease.* surface runs over the fleet runtime's one jobs
//
//	store and LeaseManager, and lease authority follows the controller
//	binding on every call (a node-role daemon grants no lease).
//
// SPORT: cmd/cascade/daemon (ADD, P1-CAP-10).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// newTestFleetRuntime builds a fleet runtime over a fresh data dir under
// root, with the production file controller binding.
func newTestFleetRuntime(t *testing.T, paths runtime.PathProvider, clock runtime.Clock) *daemon.FleetRuntime {
	t.Helper()
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatalf("MkdirAll(DataDir): %v", err)
	}
	store := storetest.NewMemStore()
	manifest := daemon.NewManifest(nil, clock)
	ctx, cancel := context.WithCancel(context.Background())
	rt, err := daemon.NewFleetRuntime(ctx, daemon.FleetRuntimeDeps{
		Paths: paths, Clock: clock, Manifest: manifest, Bus: events.New(store, clock), Config: &runtime.Config{},
		Store: store, Binding: nodes.NewFileControllerBindingBackend(paths.DataDir()),
	})
	if err != nil {
		cancel()
		t.Fatalf("NewFleetRuntime: %v", err)
	}
	t.Cleanup(func() { cancel(); manifest.Wait(); _ = rt.Close() })
	return rt
}

// setRoleBinding makes the data dir's controller binding name a controller
// (node role) or removes it (controller role).
func setRoleBinding(t *testing.T, dataDir string, node bool) {
	t.Helper()
	path := filepath.Join(dataDir, "nodes", "controller_binding.json")
	if !node {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove binding: %v", err)
		}
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir binding dir: %v", err)
	}
	raw, _ := json.Marshal(nodes.ControllerBinding{Endpoint: "controller.example.invalid:7000", EnrollmentID: "enroll-fixture"})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write binding: %v", err)
	}
}

// TestFleetRuntimeLeaseManagerUsesRealRole: the runtime's LeaseManager asks
// the binding on every call: a controller grants, a node refuses
// ErrNotController, and switching the file between calls flips the answer.
func TestFleetRuntimeLeaseManagerUsesRealRole(t *testing.T) {
	paths := fakeDaemonPaths{root: t.TempDir()}
	rt := newTestFleetRuntime(t, paths, runtime.NewFixedClock(time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)))
	ctx := context.Background()
	got, err := rt.Leases().Acquire(ctx, "repo-role", "a/**", "job-a")
	if err != nil || !got.Granted {
		t.Fatalf("controller Acquire = %+v, %v; want granted", got, err)
	}
	setRoleBinding(t, paths.DataDir(), true)
	if _, err := rt.Leases().Acquire(ctx, "repo-role", "b/**", "job-b"); !errors.Is(err, jobs.ErrNotController) || err.Error() != jobs.ErrNotController.Error() {
		t.Fatalf("node-role Acquire err = %v, want ErrNotController", err)
	}
	if err := rt.Leases().Release(ctx, "repo-role", "a/**", got.Lease.Epoch); err == nil || err.Error() != jobs.ErrNotController.Error() {
		t.Fatalf("node-role Release err = %v, want ErrNotController", err)
	}
	setRoleBinding(t, paths.DataDir(), false)
	if got, err := rt.Leases().Acquire(ctx, "repo-role", "b/**", "job-b"); err != nil || !got.Granted {
		t.Fatalf("controller again: Acquire = %+v, %v; want granted without a restart", got, err)
	}
	assertNodeGuardRefuses(t, paths, rt, got.Lease)
}

// assertNodeGuardRefuses: with a node binding, job.cancel is refused by the
// RPC guard and changes nothing; lease.release is also refused by the
// LeaseManager itself, so its half checks defence in depth, not the guard.
func assertNodeGuardRefuses(t *testing.T, paths fakeDaemonPaths, rt *daemon.FleetRuntime, held jobs.ResourceLease) {
	t.Helper()
	ctx, clock := context.Background(), runtime.NewSystemClock()
	registry := rpc.NewRegistry()
	if err := wireJobRPC(ctx, registry, rt, clock, rpc.NewNonceLedger(clock), rpc.MapTrustStore{}); err != nil {
		t.Fatalf("wireJobRPC: %v", err)
	}
	seedTestJob(t, rt.Jobs(), "job-guard", jobs.JobStatePending)
	setRoleBinding(t, paths.DataDir(), true)
	for method, p := range map[string]map[string]string{
		"job.cancel":    {"id": "job-guard"},
		"lease.release": {"id": jobs.LeaseID(held), "as_job": held.Holder},
	} {
		params, _ := json.Marshal(p)
		if _, errObj := registry.Dispatch(ctx, &rpc.Request{Method: method, Params: params}); errObj == nil {
			t.Errorf("%s on a node-role daemon succeeded, want the controller guard's refusal", method)
		}
	}
	if j, _, _ := rt.Jobs().GetJob(ctx, "job-guard"); j.State != jobs.JobStatePending {
		t.Errorf("job after a refused cancel = %q, want pending", j.State)
	}
	if l, _, _ := rt.Jobs().GetLease(ctx, held.RepoID, held.ScopeGlob); l.State != jobs.LeaseHeld {
		t.Errorf("lease after a refused release = %q, want held", l.State)
	}
}

// TestJobRPCUsesFleetRuntimeLeaseManager: a composed daemon's job.*/lease.*
// handlers act on the runtime's store and LeaseManager (a lease acquired
// through rt.Leases() is listed and released over RPC, and the release is
// journaled by the runtime's sink), wireJobRPC opens no database, the one
// production NewLeaseManager call is the runtime's, and a terminal job
// transition on the runtime store releases the job's leases.
func TestJobRPCUsesFleetRuntimeLeaseManager(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	w, cancel, done := startTestDaemon(t, newRunTestDeps(t, nil), observeWiring(t))
	t.Cleanup(func() { cancel(); awaitDaemonReturn(t, done) })
	rt, ctx := w.Deps.Fleet, principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	got, err := rt.Leases().Acquire(ctx, "repo-rpc", "src/**", "job-rpc")
	if err != nil || !got.Granted {
		t.Fatalf("Acquire = %+v, %v", got, err)
	}
	listed, errObj := w.Registry.Dispatch(ctx, &rpc.Request{Method: "lease.list", Params: json.RawMessage(`{}`)})
	if page, ok := listed.(rpc.LeasePage); errObj != nil || !ok || len(page.Leases) != 1 || page.Leases[0].Holder != "job-rpc" {
		t.Fatalf("lease.list = %+v, %+v; want the runtime's one lease", listed, errObj)
	}
	params, _ := json.Marshal(map[string]string{"id": jobs.LeaseID(got.Lease), "as_job": "job-rpc"})
	if _, errObj := w.Registry.Dispatch(ctx, &rpc.Request{Method: "lease.release", Params: params}); errObj != nil {
		t.Fatalf("lease.release: %+v", errObj)
	}
	if l, ok, err := rt.Jobs().GetLeaseByID(ctx, jobs.LeaseID(got.Lease)); err != nil || !ok || l.State != jobs.LeaseReleased {
		t.Fatalf("lease after RPC release = %+v ok=%v err=%v, want released in the runtime store", l, ok, err)
	}
	entries, err := journal.New(w.Store, runtime.NewSystemClock(), journal.DefaultNamespace).Replay(ctx, "lease:repo-rpc:src/**", journal.Cursor{}, nil)
	if err != nil || len(entries) != 2 || entries[0].Kind != journal.KindIntent || entries[1].Kind != journal.KindAck {
		t.Fatalf("lease journal = %+v, %v; want acquired then released from the runtime's sink", entries, err)
	}
	assertTerminalReleases(t, rt)
	assertGrepLines(t, []string{"sql.Open", "--", "cmd/cascade/daemon_unix_jobs_rpc.go"}, nil)
	// The one production LeaseManager is built by jobs.NewLeaseManagerWithEvents,
	// so the prefix (not the literal "jobs.NewLeaseManager(") is what is unique.
	assertGrepLines(t, []string{"jobs.NewLeaseManager", "--", "*.go", ":!*_test.go"}, []string{"internal/daemon/subsystems_fleet_reservation.go"})
}

// assertTerminalReleases: a job reaching a terminal state on the runtime
// store releases its leases (the release-on-terminal hook is installed).
func assertTerminalReleases(t *testing.T, rt *daemon.FleetRuntime) {
	t.Helper()
	ctx := context.Background()
	seedTestJob(t, rt.Jobs(), "job-term", jobs.JobStateRunning)
	got, err := rt.Leases().Acquire(ctx, "repo-term", "t/**", "job-term")
	if err != nil || !got.Granted {
		t.Fatalf("Acquire = %+v, %v", got, err)
	}
	if err := rt.Jobs().PutTransition(ctx, "job-term", jobs.JobStateFailed, 5); err != nil {
		t.Fatalf("PutTransition ->failed: %v", err)
	}
	if l, _, err := rt.Jobs().GetLease(ctx, "repo-term", "t/**"); err != nil || l.State != jobs.LeaseReleased {
		t.Fatalf("lease after terminal transition = %+v, %v; want released", l, err)
	}
}

// assertGrepLines runs git grep -n in the module and compares the matched
// files with want (nil: no match).
func assertGrepLines(t *testing.T, args []string, want []string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"grep", "-n", "-F"}, args...)...)
	cmd.Dir = moduleRootForTest(t)
	out, err := cmd.Output()
	var exitErr *exec.ExitError
	if err != nil && (!errors.As(err, &exitErr) || exitErr.ExitCode() != 1) {
		t.Fatalf("git grep %v: %v", args, err)
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			files = append(files, strings.SplitN(line, ":", 2)[0])
		}
	}
	if strings.Join(files, ",") != strings.Join(want, ",") {
		t.Fatalf("git grep %v matched %q, want %q", args, files, want)
	}
}

// moduleRootForTest is the repository top level.
func moduleRootForTest(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	return strings.TrimSpace(string(out))
}
