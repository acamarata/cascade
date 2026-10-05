//go:build !windows

// Purpose: prove surviving descendants are killed before their leader is reaped.
// Inputs: the real lifecycleplugin and its readiness pid file.
// Outputs: lifecycle ordering and Close regression verdicts.
// Constraints: isolated homes and bounded waits; no parallel DrainGrace writes.
// SPORT: process lifecycle regression tests.
package process

import (
	"context"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestMonitorKillsGrandchildBeforeRespawn(t *testing.T) {
	bin, dir := buildLifecyclePlugin(t), t.TempDir()
	rt := newRealRuntime()
	rt.Restart = RestartPolicy{MaxAttempts: 2, InitialBackoff: 100 * time.Millisecond}
	beforeRespawn := make(chan bool, 1)
	firstGrandchild := make(chan int, 1)
	starts := 0 // only the monitor calls the factory after Launch
	rt.commandFactory = func(ctx context.Context, m Manifest) Commander {
		starts++
		if starts == 2 {
			pid := <-firstGrandchild
			beforeRespawn <- gone(pid)
		}
		return defaultCommandFactory(ctx, m)
	}
	h := launchReal(context.Background(), t, rt, bin, nil,
		"-grandchild", "-dir", dir, "-crash-first", filepath.Join(dir, "crashed"))
	first := childPid(t, h)
	gpid := readPidFile(t, filepath.Join(dir, "grandchild.pid"))
	firstGrandchild <- gpid
	t.Cleanup(func() {
		if !gone(gpid) {
			_ = syscall.Kill(gpid, syscall.SIGKILL)
		}
	})
	select {
	case dead := <-beforeRespawn:
		if !dead {
			t.Fatal("first grandchild still alive before the respawned child starts")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no respawn after the first leader crashed")
	}
	awaitTrue(t, "respawn installed", func() bool { return childPid(t, h) != first && h.Alive() })
}

func TestHandleCloseKillsGrandchildAfterDrainGrace(t *testing.T) {
	bin, dir := buildLifecyclePlugin(t), t.TempDir()
	oldGrace := DrainGrace
	DrainGrace = 300 * time.Millisecond
	t.Cleanup(func() { DrainGrace = oldGrace })
	rt := newRealRuntime()
	rt.StopGrace = 2 * time.Second
	h := launchReal(context.Background(), t, rt, bin, nil, "-grandchild", "-dir", dir)
	pid := childPid(t, h)
	gpid := readPidFile(t, filepath.Join(dir, "grandchild.pid"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.Close(ctx); err != nil {
		t.Errorf("Close with StopGrace > DrainGrace: %v", err)
	}
	awaitGone(t, "grandchild after its SIGTERM-honouring leader exits", pid, gpid)
	if !closed(h.reaped) {
		t.Fatal("Close did not reap the leader")
	}
}
