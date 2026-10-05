//go:build !windows

// Purpose: prove Handle.Close against a REAL child built from
//
//	testdata/lifecycleplugin: SIGTERM then SIGKILL after StopGrace, the
//	whole process group killed (grandchild included), reaped, and an
//	idempotent result. Holds the real-child helpers the other files use.
//
// Inputs: the lifecycleplugin binary, built into t.TempDir() per test.
// Outputs: test verdicts only.
// Constraints: HOME, USERPROFILE and CASCADE_HOME point at t.TempDir()
//
//	while a child runs; no test reads the real HOME or a keychain.
//
// SPORT: internal/plugins/process close (TEST) — P1-PLG-09.

package process

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/pkg/cascade"
)

// buildLifecyclePlugin compiles testdata/lifecycleplugin into t.TempDir()
// (before isolateHome, so the build uses the real module and build cache)
// and then isolates HOME for the rest of the test.
func buildLifecyclePlugin(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "lifecycleplugin")
	out, err := exec.Command("go", "build", "-o", bin, "./testdata/lifecycleplugin").CombinedOutput()
	if err != nil {
		t.Fatalf("build lifecycleplugin: %v: %s", err, out)
	}
	for _, key := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(key, t.TempDir())
	}
	return bin
}

// newRealRuntime is a production-factory runtime with one terminal exit
// (no respawn) unless the caller changes Restart.
func newRealRuntime() *ProcessRuntime {
	rt := NewProcessRuntime()
	rt.Stderr = &bytes.Buffer{}
	rt.Registrar = egressRegistryAdapter{reg: egress.NewRegistry()}
	rt.Restart = RestartPolicy{MaxAttempts: 0, InitialBackoff: time.Millisecond}
	return rt
}

// launchReal launches bin with args and registers a bounded Close.
func launchReal(ctx context.Context, t *testing.T, rt *ProcessRuntime, bin string, env []string, args ...string) *Handle {
	t.Helper()
	m := Manifest{Name: "lifecycle", TrustTier: TrustTierTrusted, Command: bin, Args: args, Env: env}
	h, err := rt.Launch(ctx, m)
	if err != nil {
		t.Fatalf("Launch(lifecycleplugin %v): %v", args, err)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = h.Close(cctx)
	})
	return h
}

// childPid is the pid of h's current real child.
func childPid(t *testing.T, h *Handle) int {
	t.Helper()
	h.mu.RLock()
	defer h.mu.RUnlock()
	c, ok := h.cmd.(execCommander)
	if !ok || c.Process == nil {
		t.Fatalf("handle child is %T, want a started execCommander", h.cmd)
	}
	return c.Process.Pid
}

// gone reports whether pid no longer runs: ESRCH, or (linux) a zombie that
// a non-reaping PID 1 (a container's) has not collected yet.
func gone(pid int) bool {
	if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		return true
	}
	if runtime.GOOS != "linux" {
		return false
	}
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	fields := strings.Fields(string(raw[strings.LastIndexByte(string(raw), ')')+1:]))
	return len(fields) > 0 && fields[0] == "Z"
}

// awaitGone fails t unless every pid is gone within 5s.
func awaitGone(t *testing.T, what string, pids ...int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for _, pid := range pids {
		for !gone(pid) {
			if time.Now().After(deadline) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				t.Fatalf("%s: pid %d is still running", what, pid)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// readPidFile waits up to 5s for path and returns the pid it holds.
func readPidFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, err := os.ReadFile(path)
		if pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && convErr == nil {
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatalf("no pid in %s: %v", path, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHandleCloseEscalatesToSIGKILL(t *testing.T) {
	bin := buildLifecyclePlugin(t)
	rt := newRealRuntime()
	rt.StopGrace = 300 * time.Millisecond
	h := launchReal(context.Background(), t, rt, bin, nil, "-ignore-term", "-ignore-eof")
	pid := childPid(t, h)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if err := h.Close(ctx); err != nil {
		t.Errorf("Close(SIGTERM-ignoring child) = %v, want nil after the SIGKILL escalation", err)
		awaitGone(t, "SIGTERM-ignoring child after a failed Close", pid) // kills the survivor, then fails
		return
	}
	if elapsed := time.Since(start); elapsed < rt.StopGrace {
		t.Fatalf("Close returned after %v, before StopGrace %v: the child was not given its SIGTERM grace", elapsed, rt.StopGrace)
	}
	if !closed(h.reaped) {
		t.Fatal("Close returned but the monitor's Wait on the child never returned (not reaped)")
	}
	if h.Alive() {
		t.Fatal("Alive() = true after Close")
	}
	awaitGone(t, "SIGTERM-ignoring child", pid)
}

func TestHandleCloseIdempotent(t *testing.T) {
	bin := buildLifecyclePlugin(t)
	rt := newRealRuntime()
	rt.StopGrace = 50 * time.Millisecond
	h := launchReal(context.Background(), t, rt, bin, nil, "-ignore-term", "-ignore-eof")
	pid := childPid(t, h)

	// A deadline that has already passed makes the first Close fail, so
	// "the second call returns the first call's result" is observable.
	expired, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancel()
	first := h.Close(expired)
	if kind, ok := cascade.KindOf(first); !ok || kind != cascade.KindUnavailable || !strings.Contains(first.Error(), `"lifecycle"`) {
		t.Fatalf("Close(expired ctx) = %v, want KindUnavailable naming the plugin", first)
	}
	second := h.Close(context.Background())
	if second != first { //nolint:errorlint // identity is the property under test
		t.Fatalf("second Close = %v, want the first call's result %v (same value)", second, first)
	}
	// The expired first Close still escalated to SIGKILL (its grace wait
	// ended at once), so the child dies and the monitor finishes anyway.
	if !waitFor(context.Background(), h.monitorDone, 5*time.Second) {
		t.Fatal("monitor did not finish after the expired Close's SIGKILL")
	}
	awaitGone(t, "child after the expired Close", pid)
}

func TestHandleCloseKillsGrandchild(t *testing.T) {
	bin := buildLifecyclePlugin(t)
	dir := t.TempDir()
	rt := newRealRuntime()
	rt.StopGrace = 200 * time.Millisecond
	h := launchReal(context.Background(), t, rt, bin, nil, "-grandchild", "-dir", dir)
	pid := childPid(t, h)
	gpid := readPidFile(t, filepath.Join(dir, "grandchild.pid"))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	if err := h.Close(ctx); err != nil {
		t.Fatalf("Close(child with a stdout-holding grandchild) = %v", err)
	}
	elapsed := time.Since(start)
	awaitGone(t, "process group after Close", pid, gpid) // first: it kills a survivor before failing
	// The grandchild ignores SIGTERM and holds stdout; only a SIGKILL to
	// the whole group lets the drain finish, so Close must not have sat
	// out DrainGrace waiting for an EOF that never comes.
	if elapsed >= DrainGrace/2 {
		t.Fatalf("Close took %v (DrainGrace %v): the grandchild was not killed with the group", elapsed, DrainGrace)
	}
}

// stubbornCommander ignores stdin EOF: only SIGKILL ends it (its stdout
// closes and Wait returns), so abandonChild's escalation is observable.
type stubbornCommander struct {
	*fakeCommander
	kills chan os.Signal
}

func (s stubbornCommander) Signal(sig os.Signal) error {
	if sig == syscall.SIGKILL {
		s.kills <- sig
		s.exit()
	}
	return nil
}

func TestAbandonChildEscalatesToSIGKILL(t *testing.T) {
	f := newFakeCommander("1.0.0", 0, false, nil)
	cmd := stubbornCommander{fakeCommander: f, kills: make(chan os.Signal, 2)}
	tr := NewTransport(nonCloserWriter{}, f.stdoutR, time.Second) // stdin "close" never reaches the fake
	abandonChild(cmd, tr, 20*time.Millisecond)
	select {
	case <-cmd.kills:
	default:
		t.Fatal("abandonChild returned without SIGKILLing a child that ignored stdin EOF")
	}
}

func TestExecCommanderSignalEdges(t *testing.T) {
	c := newExecCommander(exec.Command("unused"))
	if err := c.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("Signal(not started) = %v, want nil (nothing to signal)", err)
	}
	err := c.Signal(fakeSignal{})
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("Signal(non-syscall signal) = %v, want KindInvalidInput", err)
	}
	if c.SysProcAttr == nil || !c.SysProcAttr.Setpgid || c.Cancel == nil {
		t.Fatalf("execCommander must start a new group with a group-kill Cancel: attr=%+v cancel=%v", c.SysProcAttr, c.Cancel != nil)
	}
}

// fakeSignal is an os.Signal that is not a syscall.Signal.
type fakeSignal struct{}

func (fakeSignal) String() string { return "fake" }
func (fakeSignal) Signal()        {}

func TestHandleLiteralCloseAndAlive(t *testing.T) {
	h := &Handle{state: &stateBox{}}
	if h.Alive() {
		t.Fatal("Alive() = true for a Handle that never had a child")
	}
	if err := h.Close(context.Background()); err != nil {
		t.Fatalf("Close on a Handle with no child = %v, want nil", err)
	}
}
