//go:build !windows && integration

// Purpose: the parent half of the child-daemon harness (T0 P1-BF-R120,
//
//	R124): a throwaway CASCADE_HOME seeded through the store APIs (provider
//	registry row for the recorded-provider server, file-vault secret and
//	standing grant), a child daemon that is this test binary re-exec'd into
//	TestDaemonResumeChildProcess (platformDaemonRun under its own TMPDIR,
//	so its TestMain gives it its own throwaway HOME), the `cascade run`
//	CLI dialing its socket in process, status.get over the socket, and a
//	read of the store once the child is gone.
//
// Constraints: the child never sees host custody (daemon_resume_child_test.go
//
//	proves the keychain unreachable before it starts); every path is under
//	one MkdirTemp root removed at cleanup.
//
// SPORT: cmd/cascade/daemon (CHANGE, P1-CORE-15).
package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/fleet/resume"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/providers/sqlite"
)

// Child environment keys (read by TestDaemonResumeChildProcess).
const (
	resumeChildRoot   = "CASCADE_TEST_RESUME_CHILD_ROOT"
	resumeChildSite   = "CASCADE_TEST_RESUME_CHILD_SITE"   // store event that SIGKILLs or notifies
	resumeChildNotify = "CASCADE_TEST_RESUME_CHILD_NOTIFY" // file written at the site instead of a kill
	resumeChildPolicy = "CASCADE_TEST_RESUME_CHILD_POLICY" // flag-file deny policy path
)

// resumeHome is one throwaway CASCADE_HOME and its recorded provider.
type resumeHome struct {
	root string
	prov *recordedProvider
}

// childOpts selects the child's swapped conductor registration (zero: the
// unmodified production registration).
type childOpts struct{ site, notify, policy string }

// resumeChild is one running child daemon.
type resumeChild struct {
	cmd  *exec.Cmd
	out  *bytes.Buffer
	done chan error
}

// newResumeHome seeds a home whose config sets the fan-out ttl and sweep
// interval; block makes the provider hold calls after its free ones.
func newResumeHome(t *testing.T, block bool, ttl, interval string) *resumeHome {
	t.Helper()
	root, err := os.MkdirTemp("", "crs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	h := &resumeHome{root: root, prov: newRecordedProvider(t, block)}
	paths := fakeDaemonPaths{root: root}
	for _, dir := range []string{paths.DataDir(), filepath.Join(root, "tmp")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cfg := "[daemon]\nshutdown_grace = \"2s\"\nfanout_record_ttl = \"" + ttl + "\"\nfanout_sweep_interval = \"" + interval + "\"\n"
	if err := os.WriteFile(paths.ConfigPath(), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	seedRecordedRegistry(ctx, t, paths, runtime.NewSystemClock(), h.prov.srv.URL)
	custody, err := secrets.SelectCustody(secrets.Config{Service: vaultService, Dir: paths.DataDir(), ForceFileVault: true, Runner: testFailingCustodyRunner})
	if err != nil {
		t.Fatal(err)
	}
	if err := custody.Set(ctx, "recorded-provider-bearer", []byte("local-"+"placeholder")); err != nil {
		t.Fatal(err)
	}
	grantStore, err := secrets.NewFileGrantStore(paths.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	grants, err := secrets.NewGrants(grantStore, runtime.NewSystemClock())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := grants.Issue(ctx, "recorded-provider-bearer", time.Hour); err != nil {
		t.Fatal(err)
	}
	return h
}

// sock is the child daemon's socket path.
func (h *resumeHome) sock() string { return fakeDaemonPaths{root: h.root}.SocketPath() }

// start runs a child daemon and waits for its socket.
func (h *resumeHome) start(t *testing.T, o childOpts) *resumeChild {
	t.Helper()
	h.clearDeadSchedulerLease(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestDaemonResumeChildProcess$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), resumeChildRoot+"="+h.root, resumeChildSite+"="+o.site, resumeChildNotify+"="+o.notify,
		resumeChildPolicy+"="+o.policy, "TMPDIR="+filepath.Join(h.root, "tmp"),
		"DBUS_SESSION_BUS_ADDRESS=unix:path="+filepath.Join(h.root, "no-bus"))
	c := &resumeChild{cmd: cmd, out: &bytes.Buffer{}, done: make(chan error, 1)}
	cmd.Stdout, cmd.Stderr = c.out, c.out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { c.done <- cmd.Wait() }()
	t.Cleanup(c.reap)
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		select {
		case err := <-c.done:
			c.done <- err
			t.Fatalf("child daemon exited before serving: %v\n%s", err, c.out.String())
		default:
		}
		if socketDialable(h.sock()) {
			return c
		}
	}
	_ = cmd.Process.Kill()
	c.done <- <-c.done
	t.Fatalf("child daemon never served\n%s", c.out.String())
	return nil
}

// clearDeadSchedulerLease removes the scheduler's advisory-lock row a
// SIGKILLed daemon left behind (its 5-minute lease would refuse every
// restart until it lapses; a pre-existing kill-9 finding documented in
// internal/jobs/acceptance_resume_integration_rig_test.go). This is the
// operator recovery that rig uses; no fan-out state is touched.
func (h *resumeHome) clearDeadSchedulerLease(t *testing.T) {
	t.Helper()
	path := filepath.Join(fakeDaemonPaths{root: h.root}.DataDir(), "cascade.db")
	if _, err := os.Stat(path); err != nil {
		return
	}
	drv, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open the child's store: %v", err)
	}
	defer func() { _ = drv.Close() }()
	if err := drv.Delete(context.Background(), schedulerNamespace, "sched:lock"); err != nil {
		t.Fatalf("clear the dead scheduler lease: %v", err)
	}
}

// reap kills a child a failed test left running and waits for it.
func (c *resumeChild) reap() {
	select {
	case err := <-c.done:
		c.done <- err
	default:
		_ = c.cmd.Process.Kill()
		c.done <- <-c.done
	}
}

// crashRun runs a fan-out on c that must end with the daemon's death, and
// fails with the run's own error when the daemon answered instead.
func (h *resumeHome) crashRun(t *testing.T, c *resumeChild, n, prompt string) cliResult {
	t.Helper()
	res := h.runFan(t, n, "", prompt)
	if res.err == nil || !strings.Contains(res.err.Error(), "EOF") {
		t.Fatalf("crash run ended with %v; want the daemon's death mid-call (EOF)", res.err)
	}
	c.waitKilled(t)
	return res
}

// wait waits for the child to exit and returns how it ended; the result
// stays readable.
func (c *resumeChild) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-c.done:
		c.done <- err // kept for reap and any later wait
		return err
	case <-time.After(60 * time.Second):
		_ = c.cmd.Process.Kill()
		c.done <- <-c.done
		t.Fatalf("child daemon did not exit\n%s", c.out.String())
		return nil
	}
}

// waitKilled waits for the child to die by SIGKILL (its own store seam or
// the parent's kill9).
func (c *resumeChild) waitKilled(t *testing.T) {
	t.Helper()
	var exitErr *exec.ExitError
	if err := c.wait(t); !errors.As(err, &exitErr) || exitErr.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("child daemon: %v, want death by SIGKILL\n%s", err, c.out.String())
	}
}

// kill9 SIGKILLs the child daemon.
func (c *resumeChild) kill9(t *testing.T) {
	t.Helper()
	if err := c.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	c.waitKilled(t)
}

// term SIGTERMs the child daemon and requires a clean exit.
func (c *resumeChild) term(t *testing.T) {
	t.Helper()
	if err := c.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := c.wait(t); err != nil {
		t.Fatalf("child daemon after SIGTERM: %v, want a clean exit\n%s", err, c.out.String())
	}
}

// waitFile polls for path (the child's notify file).
func waitFile(t *testing.T, path string) {
	t.Helper()
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
	}
	t.Fatalf("notify file %s never written", path)
}

// resumeDetail reads status.get's fanout-resume detail over the socket.
func (h *resumeHome) resumeDetail(t *testing.T) string {
	t.Helper()
	var st daemon.StatusResponse
	if err := client.New(h.sock(), client.UnixDialer, 10*time.Second).Do(context.Background(), daemon.StatusMethod, nil, &st); err != nil {
		t.Fatalf("status.get: %v", err)
	}
	for _, s := range st.Subsystems {
		if s.Name == fanOutResumeSubsystem {
			return s.Detail
		}
	}
	return ""
}

// inspect opens the store of a child that has exited, reads it and closes
// it again (the store's exclusive lock allows one opener at a time).
func (h *resumeHome) inspect(t *testing.T, id string) (resume.FanOutState, map[string]int) {
	t.Helper()
	drv, err := sqlite.Open(context.Background(), filepath.Join(fakeDaemonPaths{root: h.root}.DataDir(), "cascade.db"))
	if err != nil {
		t.Fatalf("open the child's store: %v", err)
	}
	defer func() { _ = drv.Close() }()
	fs, _ := resume.NewFanOutStore(journal.New(drv, runtime.NewSystemClock(), journal.DefaultNamespace), drv)
	st, err := fs.State(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, ns := range []string{"conductor.fanout.legs", "conductor.fanout.requests", "conductor.fanout.attempts"} {
		counts[ns] = countKeys(t, drv, ns)
	}
	return st, counts
}

// records is the leg-result plus request-record count of a view.
func records(c map[string]int) int {
	return c["conductor.fanout.legs"] + c["conductor.fanout.requests"]
}
