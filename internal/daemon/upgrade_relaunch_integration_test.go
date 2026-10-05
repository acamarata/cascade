//go:build !windows && integration

package daemon

// Purpose: the upgrade hand-off on real binaries. Two cascade builds that
//   differ only in their stamped version; a daemon started from path P with
//   a fresh HOME and CASCADE_HOME; P replaced by the second build. One
//   UpgradeSignal relaunches it in place (same PID, new version); a plain
//   `cascade daemon stop` after the same replacement stops it, without a
//   relaunch and without escalating to SIGKILL.
// Constraints: runs only in the golang:1.26-bookworm container leg; it
//   builds and runs real daemons, which never run on the build host.
// SPORT: internal/daemon (CHANGED, explicit upgrade hand-off).

import (
	"context"
	"encoding/json"
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
)

const (
	e2eVersionOld = "2.0.0-e2e.1"
	e2eVersionNew = "2.0.1-e2e.2"
	e2eWait       = 30 * time.Second
)

func init() { realBinaryStopLeg = testStopAfterReplaceRealBinary }

// realDaemon is one `cascade daemon run` child and the environment it runs in.
type realDaemon struct {
	cmd    *exec.Cmd
	env    []string
	sock   string
	exited chan struct{}
}

// buildCascadeVersion builds ./cmd/cascade with buildinfo.Version stamped.
func buildCascadeVersion(t *testing.T, version string) string {
	t.Helper()
	root, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	out := filepath.Join(t.TempDir(), "cascade-"+version)
	build := exec.Command("go", "build", "-buildvcs=false", "-o", out,
		"-ldflags", "-X github.com/acamarata/cascade/internal/buildinfo.Version="+version, "./cmd/cascade")
	build.Dir = filepath.Dir(strings.TrimSpace(string(root)))
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", version, err, b)
	}
	return out
}

// installAt copies src next to dst and renames it over dst, the way an
// installer replaces a binary.
func installAt(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	if err := os.WriteFile(dst+".new", b, 0o755); err != nil {
		t.Fatalf("write %s.new: %v", dst, err)
	}
	if err := os.Rename(dst+".new", dst); err != nil {
		t.Fatalf("rename over %s: %v", dst, err)
	}
}

// startRealDaemon runs `P daemon run` with a fresh HOME and CASCADE_HOME and
// reaps it as soon as it exits, so a stop sees the PID gone, not a zombie.
func startRealDaemon(t *testing.T, p string) *realDaemon {
	t.Helper()
	home := shortTempDir(t)
	d := &realDaemon{sock: filepath.Join(home, "daemon.sock"), exited: make(chan struct{})}
	d.env = append(os.Environ(), "HOME="+t.TempDir(), "USERPROFILE="+t.TempDir(), "CASCADE_HOME="+home)
	d.cmd = exec.Command(p, "daemon", "run")
	d.cmd.Env, d.cmd.Stdout, d.cmd.Stderr = d.env, io.Discard, io.Discard
	if err := d.cmd.Start(); err != nil {
		t.Fatalf("start %s daemon run: %v", p, err)
	}
	go func() { _ = d.cmd.Wait(); close(d.exited) }()
	t.Cleanup(func() {
		_ = d.cmd.Process.Signal(syscall.SIGKILL)
		<-d.exited
	})
	return d
}

// status calls status.get over the daemon's real socket.
func (d *realDaemon) status() (StatusResponse, error) {
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", d.sock)
		},
	}}
	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"` + StatusMethod + `"}`)
	resp, err := client.Post("http://unix/rpc", "application/json", body)
	if err != nil {
		return StatusResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	var env struct {
		Result StatusResponse `json:"result"`
	}
	err = json.NewDecoder(resp.Body).Decode(&env)
	return env.Result, err
}

// waitVersion polls status.get until it reports want, failing after e2eWait.
func (d *realDaemon) waitVersion(t *testing.T, want string) StatusResponse {
	t.Helper()
	deadline := time.NewTimer(e2eWait)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	var last string
	for {
		if st, err := d.status(); err == nil {
			if st.Version == want {
				return st
			}
			last = st.Version
		}
		select {
		case <-d.exited:
			t.Fatalf("daemon exited while waiting for version %s", want)
		case <-deadline.C:
			t.Fatalf("status.get version never reached %q within %s (last %q)", want, e2eWait, last)
			return StatusResponse{}
		case <-tick.C:
		}
	}
}

// startOldAtP builds both versions, installs the old one at P, starts the
// daemon from P and waits until it answers with the old version.
func startOldAtP(t *testing.T) (d *realDaemon, p, newBin string) {
	t.Helper()
	oldBin, newBin := buildCascadeVersion(t, e2eVersionOld), buildCascadeVersion(t, e2eVersionNew)
	p = filepath.Join(t.TempDir(), "cascade")
	installAt(t, oldBin, p)
	d = startRealDaemon(t, p)
	d.waitVersion(t, e2eVersionOld)
	return d, p, newBin
}

// TestRealBinaryRelaunchOnReplace: replace P, send one UpgradeSignal, and
// status.get reports the new version from the same PID within 30 s.
func TestRealBinaryRelaunchOnReplace(t *testing.T) {
	d, p, newBin := startOldAtP(t)
	installAt(t, newBin, p)
	if err := d.cmd.Process.Signal(UpgradeSignal); err != nil {
		t.Fatalf("send UpgradeSignal: %v", err)
	}
	st := d.waitVersion(t, e2eVersionNew)
	if st.Daemon.PID != d.cmd.Process.Pid {
		t.Fatalf("relaunched daemon PID = %d; want the same PID %d (exec in place)", st.Daemon.PID, d.cmd.Process.Pid)
	}
}

// testStopAfterReplaceRealBinary: replace P, run `P daemon stop`; the daemon
// exits without a relaunch or a SIGKILL and nothing listens afterwards.
func testStopAfterReplaceRealBinary(t *testing.T) {
	d, p, newBin := startOldAtP(t)
	installAt(t, newBin, p)
	stop := exec.Command(p, "daemon", "stop")
	stop.Env = d.env
	out, err := stop.CombinedOutput()
	if err != nil {
		t.Fatalf("cascade daemon stop: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "stopped") || strings.Contains(string(out), "escalated") {
		t.Fatalf("cascade daemon stop output %q; want a plain stop (StopResult.Escalated false)", out)
	}
	select {
	case <-d.exited:
	case <-time.After(e2eWait):
		t.Fatal("daemon still running after cascade daemon stop: a stop turned into a relaunch")
	}
	if code := d.cmd.ProcessState.ExitCode(); code != 0 {
		t.Fatalf("daemon exit code = %d; want 0 (a clean drain)", code)
	}
	if c, err := net.Dial("unix", d.sock); err == nil {
		_ = c.Close()
		t.Fatal("something still listens on the daemon socket after stop")
	}
}
