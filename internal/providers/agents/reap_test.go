package agents

// Purpose: the lease sweep, against fake process tables (survivor, recycled
// pid, error paths) and against real processes: the test-only reapchild is
// spawned with a marker, including a setsid orphan, and the sweep must kill
// it, leave other markers alone and report a survivor.
//
// Constraints: only testdata/reapchild is ever spawned; HOME and USERPROFILE
// point at a fresh temp dir; sentinel errors are asserted by identity and
// message.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// --- real processes ---

var (
	childOnce sync.Once
	childBin  string
	childErr  error
)

// reapChildBin builds testdata/reapchild once and returns its path. It runs
// before a test changes HOME so the build keeps the real Go cache.
func reapChildBin(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("reapchild is unix only")
	}
	childOnce.Do(func() {
		dir, err := os.MkdirTemp("", "reapchild")
		if err != nil {
			childErr = err
			return
		}
		childBin = filepath.Join(dir, "reapchild")
		out, err := exec.Command("go", "build", "-o", childBin, "./testdata/reapchild").CombinedOutput()
		if err != nil {
			childErr = errors.New("go build reapchild: " + err.Error() + "\n" + string(out))
		}
	})
	if childErr != nil {
		t.Fatal(childErr)
	}
	return childBin
}

func TestMain(m *testing.M) {
	code := m.Run()
	if childBin != "" {
		_ = os.RemoveAll(filepath.Dir(childBin))
	}
	os.Exit(code)
}

// child is one running reapchild.
type child struct {
	cmd  *exec.Cmd
	done chan struct{}
}

// childEnv is the test environment with HOME isolated and marker, when not
// empty, as the only CASCADE_LEASE_ID entry.
func childEnv(t *testing.T, marker string) []string {
	home := t.TempDir()
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, LeaseMarkerEnv+"=") && !strings.HasPrefix(e, "HOME=") && !strings.HasPrefix(e, "USERPROFILE=") {
			env = append(env, e)
		}
	}
	env = append(env, "HOME="+home, "USERPROFILE="+home)
	if marker != "" {
		env = append(env, leaseEntry(marker))
	}
	return env
}

// startChild runs reapchild in the background; it is killed at cleanup.
func startChild(t *testing.T, bin, marker string, args ...string) *child {
	t.Helper()
	c := &child{cmd: exec.Command(bin, args...), done: make(chan struct{})}
	c.cmd.Env = childEnv(t, marker)
	if err := c.cmd.Start(); err != nil {
		t.Fatalf("start reapchild: %v", err)
	}
	go func() { _ = c.cmd.Wait(); close(c.done) }()
	t.Cleanup(func() { _ = c.cmd.Process.Kill(); <-c.done })
	return c
}

func (c *child) exited() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

// readPid polls for the pid file reapchild writes and registers the process
// for cleanup.
func readPid(t *testing.T, path string) int {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if b, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				t.Cleanup(func() { killPid(pid) })
				return pid
			}
		}
	}
	t.Fatalf("reapchild never wrote %s", path)
	return 0
}

func killPid(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}

// alive reports whether pid is a running process; a zombie is not running.
func alive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil || p.Signal(syscall.Signal(0)) != nil {
		return false
	}
	if b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil {
		if i := strings.LastIndexByte(string(b), ')'); i >= 0 && i+2 < len(b) && b[i+2] == 'Z' {
			return false
		}
	}
	return true
}

// dead waits briefly for pid to stop running.
func dead(pid int) bool {
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if !alive(pid) {
			return true
		}
	}
	return false
}

func TestSpawnSetsLeaseMarker(t *testing.T) {
	bin := reapChildBin(t)
	marker, err := NewLeaseMarker()
	if err != nil {
		t.Fatalf("NewLeaseMarker: %v", err)
	}
	cmd := exec.Command(bin, "print-env", LeaseMarkerEnv)
	cmd.Env = childEnv(t, marker)
	out, err := cmd.Output()
	if got := strings.TrimSpace(string(out)); err != nil || got != marker {
		t.Fatalf("child saw %s=%q (%v), want %q", LeaseMarkerEnv, got, err, marker)
	}
	if !validMarker(marker) {
		t.Fatalf("marker %q is not a lease marker", marker)
	}
}

func TestLeaseSweepReapsSetsidDescendant(t *testing.T) {
	bin := reapChildBin(t)
	marker, _ := NewLeaseMarker()
	pidfile := filepath.Join(t.TempDir(), "leaf.pid")
	fork := startChild(t, bin, marker, "fork", pidfile)
	leaf := readPid(t, pidfile)
	<-fork.done // the forker is gone: the leaf is an orphan in its own session
	if !alive(leaf) {
		t.Fatalf("the setsid descendant %d is not running before the sweep", leaf)
	}
	if err := SweepLease(context.Background(), marker); err != nil {
		t.Fatalf("SweepLease = %v, want nil", err)
	}
	if !dead(leaf) {
		t.Fatalf("setsid descendant %d survived a successful sweep", leaf)
	}
	src, _ := platformSource()
	if left, _, err := fastSweeper(src).marked(context.Background(), leaseEntry(marker)); err != nil || len(left) != 0 {
		t.Fatalf("re-list after the sweep = %v, %v; want empty", left, err)
	}
}

func TestLeaseSweepIgnoresOtherMarkers(t *testing.T) {
	bin := reapChildBin(t)
	mine, _ := NewLeaseMarker()
	other, _ := NewLeaseMarker()
	dir := t.TempDir()
	target := startChild(t, bin, mine, "sleep", filepath.Join(dir, "mine.pid"))
	bystander := startChild(t, bin, other, "sleep", filepath.Join(dir, "other.pid"))
	unmarked := startChild(t, bin, "", "sleep", filepath.Join(dir, "none.pid"))
	for _, n := range []string{"mine", "other", "none"} {
		readPid(t, filepath.Join(dir, n+".pid"))
	}
	if err := SweepLease(context.Background(), mine); err != nil {
		t.Fatalf("SweepLease = %v, want nil", err)
	}
	select {
	case <-target.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the marked child survived the sweep")
	}
	// A widened match would kill the others after the target; give their
	// reaper goroutines time to report it before calling them untouched.
	for deadline := time.Now().Add(750 * time.Millisecond); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
		if bystander.exited() || unmarked.exited() {
			t.Fatalf("a process with another or no marker was killed (other=%v none=%v)", bystander.exited(), unmarked.exited())
		}
	}
}

// A child that execs with an empty environment leaves the sweep's reach. The
// residual is documented (DEF-P2-driver-tool-isolation); this test pins it so
// a change to it is deliberate.
func TestLeaseSweepScrubbedChildIsAKnownResidual(t *testing.T) {
	bin := reapChildBin(t)
	marker, _ := NewLeaseMarker()
	pidfile := filepath.Join(t.TempDir(), "scrubbed.pid")
	startChild(t, bin, marker, "scrub", pidfile)
	pid := readPid(t, pidfile)
	if err := SweepLease(context.Background(), marker); err != nil {
		t.Fatalf("SweepLease = %v, want nil (nothing carries the marker)", err)
	}
	if !alive(pid) {
		t.Fatal("the scrubbed child was killed: the documented residual changed")
	}
}
