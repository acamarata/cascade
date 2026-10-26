//go:build linux && integration

package agents

// Purpose: TestDaemonEnvironHiddenLinux (linux container leg): after
// HideDaemonEnviron a same-uid sibling reading /proc/<daemon>/environ gets
// EACCES, while the same read of an unhidden control process succeeds (so the
// denial is the flag's doing, not the sandbox's), and the hidden "daemon" still
// serves: its own /proc reads work and its AgentPeerGuard still reads a live
// agent child's environ and refuses it, while the test process, outside every
// live tree, keeps the owner view.
// Constraints: the "daemon" is this test binary re-run as TestHelperDaemon (a
// separate process, so the flag never touches the test runner); spawned
// children are testdata/reapchild; the fake provider key is a synthetic
// literal; HOME is a fresh temp dir.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
)

const (
	helperEnv    = "CASCADE_TEST_DAEMON_HELPER" // "<live file>|<socket>|hide" or "...|keep"
	fakeKeyEntry = "FAKE_PROVIDER_KEY=synthetic-environ-canary-0000"
)

// TestHelperDaemon is the daemon process. It is a no-op in a normal run.
func TestHelperDaemon(t *testing.T) {
	spec := os.Getenv(helperEnv)
	if spec == "" {
		t.Skip("helper process only")
	}
	parts := strings.Split(spec, "|")
	liveFile, sock, mode := parts[0], parts[1], parts[2]
	if mode == "hide" {
		if err := HideDaemonEnviron(); err != nil {
			fmt.Println("hide-failed", err)
			os.Exit(10)
		}
	}
	own := ownProcReads()
	rpc.SetOwnerPeerGuard(AgentPeerGuard(func() []LiveSpawn {
		var live []LiveSpawn
		if b, err := os.ReadFile(liveFile); err == nil {
			_ = json.Unmarshal(b, &live)
		}
		return live
	}))
	reg := rpc.NewRegistry()
	reg.SetAccessPolicy(nil)
	reg.Register(ownerVerb, func(context.Context, json.RawMessage) (any, error) { return own, nil })
	ln, err := net.Listen("unix", sock)
	if err != nil {
		fmt.Println("listen-failed", err)
		os.Exit(11)
	}
	go func() {
		_ = (&http.Server{Handler: rpc.NewHandler(reg), ConnContext: rpc.GuardedConnContext(rpc.ConnContext)}).Serve(ln)
	}()
	fmt.Println("ready")
	_, _ = io.Copy(io.Discard, os.Stdin) // run until the parent closes stdin
	os.Exit(0)
}

// ownProcEntries are the /proc entries a hidden process is asked to read about
// itself. The daemon's own production code reads exe (os.Executable, hook packs
// and service install) and the global /proc/stat and /proc/meminfo (governor,
// unaffected by this flag); the rest are recorded so the architect can see
// the full effect of PR_SET_DUMPABLE=0 (escalate_if).
var ownProcEntries = []string{"exe", "cmdline", "stat", "status", "comm", "cgroup", "limits", "mountinfo", "maps", "io", "fd", "cwd", "environ"}

// ownProcReads returns, per entry, "ok" or the error of reading that entry of
// /proc/self.
func ownProcReads() map[string]string {
	out := map[string]string{}
	for _, name := range ownProcEntries {
		path := "/proc/self/" + name
		var err error
		switch name {
		case "exe", "cwd":
			_, err = os.Readlink(path)
		case "fd":
			_, err = os.ReadDir(path)
		default:
			_, err = os.ReadFile(path)
		}
		out[name] = "ok"
		if err != nil {
			out[name] = err.Error()
		}
	}
	return out
}

// startHelper runs the daemon helper and waits for "ready".
func startHelper(t *testing.T, liveFile, sock, mode string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperDaemon$")
	cmd.Env = append(childEnv(t, ""), helperEnv+"="+liveFile+"|"+sock+"|"+mode, fakeKeyEntry)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	ready := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if line := sc.Text(); line == "ready" || strings.HasSuffix(line, "-failed") || strings.Contains(line, "-failed ") {
				ready <- line
				return
			}
		}
		ready <- "stdout closed before ready"
	}()
	select {
	case line := <-ready:
		if line != "ready" {
			t.Fatalf("daemon helper (%s): %s", mode, line)
		}
	case <-time.After(60 * time.Second):
		t.Fatalf("daemon helper (%s) never became ready", mode)
	}
	return cmd
}

// requireControlReadable fails the test unless an unhidden helper of the same
// uid exposes its environ to this sibling, so the denial below is the flag's.
func requireControlReadable(t *testing.T, control *exec.Cmd) {
	t.Helper()
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", control.Process.Pid))
	if err != nil || !strings.Contains(string(b), fakeKeyEntry) {
		t.Fatalf("control: environ read err %v, found key %v; the container hides environ already, so the test proves nothing", err, strings.Contains(string(b), fakeKeyEntry))
	}
}

// requireEnvironDenied fails unless a same-uid reader, and a true sibling
// process, are both denied the hidden daemon's environ.
func requireEnvironDenied(t *testing.T, daemon *exec.Cmd) {
	t.Helper()
	path := fmt.Sprintf("/proc/%d/environ", daemon.Process.Pid)
	b, err := os.ReadFile(path)
	if !errors.Is(err, syscall.EACCES) || strings.Contains(string(b), "FAKE_PROVIDER_KEY") {
		t.Errorf("a same-uid reader got %d bytes, err %v; want EACCES and no key", len(b), err)
	}
	out, cerr := exec.Command("cat", path).CombinedOutput()
	if cerr == nil || !strings.Contains(string(out), "Permission denied") || strings.Contains(string(out), "FAKE_PROVIDER_KEY") {
		t.Errorf("sibling cat: err %v output %q, want Permission denied", cerr, out)
	}
}

// requireHiddenDaemonServes checks that the hidden daemon still answers: its
// guard refuses a live agent child (so it reads that child's environ and
// ancestry), the test process keeps the owner view, and its own /proc reads that
// production code relies on still work.
func requireHiddenDaemonServes(t *testing.T, bin, sock, liveFile string) {
	t.Helper()
	marker, err := NewLeaseMarker()
	if err != nil {
		t.Fatal(err)
	}
	publish := func(sp LiveSpawn) {
		raw, _ := json.Marshal([]LiveSpawn{sp})
		if err := os.WriteFile(liveFile, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	wantRefused(t, "agent child of the hidden daemon", dialChild(t, bin, "dial", sock, "owner", ownerVerb, marker, leaderLive(marker), publish, inOwnGroup))

	owner := selfDial(t, sock, ownerVerb, principal.HeaderOwnerCLI, "owner-cli")
	var env struct {
		Result map[string]string `json:"result"`
	}
	if owner.status != 200 || json.Unmarshal([]byte(owner.body), &env) != nil || len(env.Result) == 0 {
		t.Fatalf("owner dial of the hidden daemon: status %d body %q, want 200 and its own /proc report", owner.status, owner.body)
	}
	for name, got := range env.Result {
		t.Logf("hidden daemon, own /proc/self/%s: %s", name, got)
	}
	// What the daemon's production code reads about itself must keep working.
	for _, name := range []string{"exe", "cmdline", "stat", "status"} {
		if env.Result[name] != "ok" {
			t.Errorf("hidden daemon cannot read its own /proc/self/%s: %s (escalate: the architect decides)", name, env.Result[name])
		}
	}
	// The flag's point: its own environ is no longer readable even by itself.
	if !strings.Contains(env.Result["environ"], "permission denied") {
		t.Errorf("hidden daemon reads its own environ (%q): the flag did not take effect", env.Result["environ"])
	}
}

func TestDaemonEnvironHiddenLinux(t *testing.T) {
	bin := reapChildBin(t)
	dir, err := os.MkdirTemp("", "aghide")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	liveFile := filepath.Join(dir, "live.json")
	requireControlReadable(t, startHelper(t, liveFile, filepath.Join(dir, "c.sock"), "keep"))
	sock := filepath.Join(dir, "d.sock")
	daemon := startHelper(t, liveFile, sock, "hide")
	requireEnvironDenied(t, daemon)
	requireHiddenDaemonServes(t, bin, sock, liveFile)
}
