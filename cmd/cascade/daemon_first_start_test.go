//go:build !windows

// Purpose: the first start of a fresh home. composeDaemon bootstraps the
// controller designation before any role consumer reads it, so the job
// scheduler runs and the controller-only job verbs work on the very first
// start, without any fixture seeding the designation.
// Constraints: the paths provider here seeds nothing (fakeDaemonPaths seeds a
// fixture designation); the daemon is the real composeDaemon and daemon.Run.
// SPORT: cmd/cascade/daemon (test).

package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
)

// bareDaemonPaths is fakeDaemonPaths over a data dir nothing pre-seeds.
type bareDaemonPaths struct{ fakeDaemonPaths }

func (p bareDaemonPaths) DataDir() string { return filepath.Join(p.root, "data") }

// freshHomeDeps are daemon deps over a fresh home with no designation, and
// the real node keystore constructor over a short secrets dir.
func freshHomeDeps(t *testing.T) daemonDeps {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	deps := newRunTestDeps(t, nil)
	deps.Paths = bareDaemonPaths{fakeDaemonPaths{root: deps.Paths.Root()}}
	deps.SecretsDir = shortTempDir(t)
	deps.NewKeystore = nodes.NewNodeKeystore
	return deps
}

func TestFirstDaemonStartBootstraps(t *testing.T) {
	deps := freshHomeDeps(t)
	dataDir := deps.Paths.DataDir()
	if _, found, err := nodes.NewFileDesignationBackend(dataDir).Load(); err != nil || found {
		t.Fatalf("fresh home before the first start: designation found=%v err=%v; want none", found, err)
	}
	w, cancel, done := startTestDaemon(t, deps, observeWiring(t))
	t.Cleanup(func() { cancel(); awaitDaemonReturn(t, done) })

	d, found, err := nodes.NewFileDesignationBackend(dataDir).Load()
	if err != nil || !found || d.Epoch != 1 || d.Reason != "bootstrap" {
		t.Fatalf("designation after the first start = %+v found=%v err=%v; want epoch 1, reason bootstrap", d, found, err)
	}
	if s := subsystem(t, w, "jobs.scheduler"); s.State != daemon.SubsystemRunning {
		t.Fatalf("jobs.scheduler = %v %q on the first start, want Running", s.State, s.Detail)
	}
	seedTestJob(t, w.Deps.Fleet.Jobs(), "job-first-start", jobs.JobStatePending)
	params, _ := json.Marshal(map[string]string{"id": "job-first-start"})
	ctx := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	if _, errObj := w.Registry.Dispatch(ctx, &rpc.Request{Method: "job.cancel", Params: params}); errObj != nil {
		t.Fatalf("job.cancel (the job.advance guard) on the first start: %+v", errObj)
	}
}
