// Purpose: cobra-wiring tests for `cascade daemon` — mounting, argument
//
//	validation, --json envelope shape, and (on this build platform) an
//	end-to-end status/start/stop/restart round-trip against a real
//	background daemon process built from this same module, so the CLI
//	layer's plumbing (config load → Settings resolution → internal/daemon
//	call → output.Writer rendering) is proven, not just internal/daemon's
//	own unit tests. The Windows refusal PATH itself is proven by internal/
//	daemon's own build-tagged daemon_windows_test.go, run on the Windows
//	CI lane (R-14.131) — this file stays platform-neutral and, on a non-
//	Windows build machine, only asserts today's actual observed behaviour.
//
// Constraints: Art.7.1 — every test roots CASCADE_HOME/CASCADE_SOCKET at
//
//	t.TempDir() via a fake Getenv, never the real home directory.
//
// SPORT: cmd/cascade/daemon (ADD, per T-2 sport_updates).
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// execDaemon runs a fresh `daemon` command tree rooted at home (a
// t.TempDir()) with the given args and returns combined stdout+stderr.
func execDaemon(t *testing.T, home string, args ...string) (string, error) {
	t.Helper()
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	// lazyPaths (root.go) resolves via os.Getenv/os.UserHomeDir directly —
	// this ticket's files_scope does not extend to making that injectable,
	// so t.Setenv is how this test isolates HOME (t.Setenv itself already
	// fails any test that also uses t.Parallel, which none of these do).
	t.Setenv("CASCADE_HOME", home)
	root.SetArgs(args)
	err := root.Execute()
	return buf.String(), err
}

func TestDaemonCmd_MountedUnderHelp(t *testing.T) {
	out, err := execDaemon(t, t.TempDir(), "--help")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "daemon") {
		t.Errorf("root help does not mention daemon: %q", out)
	}
}

func TestDaemonCmd_UnknownSubcommand_InvalidInput(t *testing.T) {
	_, err := execDaemon(t, t.TempDir(), "daemon", "bogus")
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("err = %v, want KindInvalidInput", err)
	}
}

func TestDaemonCmd_ExtraArgsRejected(t *testing.T) {
	for _, verb := range []string{"run", "start", "stop", "restart", "status"} {
		t.Run(verb, func(t *testing.T) {
			_, err := execDaemon(t, t.TempDir(), "daemon", verb, "unexpected-arg")
			if err == nil {
				t.Errorf("daemon %s accepted an unexpected positional arg", verb)
			}
		})
	}
}

func TestDaemonStatusCmd_NotRunning_JSONEnvelope(t *testing.T) {
	home := t.TempDir()
	out, err := execDaemon(t, home, "daemon", "status", "--json")
	if err != nil {
		t.Fatalf("daemon status: %v (%s)", err, out)
	}

	var envelope struct {
		OK   bool `json:"ok"`
		Data struct {
			Running     bool    `json:"running"`
			PID         int     `json:"pid"`
			UptimeS     float64 `json:"uptime_s"`
			Connections int     `json:"connections"`
			Detail      string  `json:"detail"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &envelope); err != nil {
		t.Fatalf("unmarshal envelope: %v\noutput: %s", err, out)
	}
	if envelope.Data.Running {
		t.Errorf("fresh CASCADE_HOME reports a running daemon: %+v", envelope.Data)
	}
}

func TestDaemonStopCmd_NothingRunning_IsIdempotent(t *testing.T) {
	home := t.TempDir()
	out, err := execDaemon(t, home, "daemon", "stop")
	if err != nil {
		t.Fatalf("daemon stop against nothing running: %v (%s)", err, out)
	}
}

// TestDaemonStartStopRestartStatus_RealBinary is the end-to-end round-trip:
// it builds the real cascade binary once, then drives start → status →
// restart → status → stop → status against it exactly as a user would from
// a shell, proving the whole CLI-to-process wire, not just faked units.
// Skipped when go build is unavailable (no network/toolchain assumptions
// beyond what every other check in this ticket already requires) or on
// Windows, where `cascade daemon` refuses unconditionally by design.
func TestDaemonStartStopRestartStatus_RealBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("daemon is unsupported on windows by design; see internal/daemon/daemon_windows_test.go")
	}
	if testing.Short() {
		t.Skip("short mode: skips the real-binary end-to-end round-trip")
	}

	repoRoot := findModuleRoot(t)
	binPath := filepath.Join(t.TempDir(), "cascade-e2e")
	build := exec.Command("go", "build", "-o", binPath, "./cmd/cascade")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build cascade binary: %v\n%s", err, out)
	}

	// A unix socket path must fit in sockaddr_un.sun_path (~104 bytes on
	// Darwin); t.TempDir() embeds this test's own long name in its path,
	// which alone can overflow that limit before "daemon.sock" is even
	// appended — so CASCADE_HOME here is a short dedicated temp dir
	// instead (see internal/daemon's shortTempDir for the same fix there).
	home := shortHomeDir(t)
	run := realBinaryRunner(t, binPath, home)
	e2eRoundTrip(t, run)
}

// realBinaryRunner returns a closure that invokes the built binary with
// CASCADE_HOME=home, returning combined output and the real exit code.
func realBinaryRunner(t *testing.T, binPath, home string) func(...string) (string, int) {
	t.Helper()
	return func(args ...string) (string, int) {
		cmd := exec.Command(binPath, args...)
		cmd.Env = append(os.Environ(), "CASCADE_HOME="+home)
		out, _ := cmd.CombinedOutput()
		return string(out), cmd.ProcessState.ExitCode()
	}
}

// e2eRoundTrip drives status → start → start (idempotent) → status →
// restart → status → stop → status against run, exactly as a user would
// from a shell.
func e2eRoundTrip(t *testing.T, run func(...string) (string, int)) {
	t.Helper()
	if out, code := run("daemon", "status"); code != 0 || !strings.Contains(out, "no pidfile") {
		t.Fatalf("initial status = %q (exit %d), want 'no pidfile' exit 0", out, code)
	}
	if out, code := run("daemon", "start"); code != 0 {
		t.Fatalf("start = %q (exit %d)", out, code)
	}
	if out, code := run("daemon", "start"); code != 0 || !strings.Contains(out, "already running") {
		t.Fatalf("second start = %q (exit %d), want idempotent 'already running'", out, code)
	}
	if out, code := run("daemon", "status"); code != 0 || !strings.Contains(out, "running") {
		t.Fatalf("status after start = %q (exit %d)", out, code)
	}
	if out, code := run("daemon", "restart"); code != 0 {
		t.Fatalf("restart = %q (exit %d)", out, code)
	}
	if out, code := run("daemon", "status"); code != 0 || !strings.Contains(out, "running") {
		t.Fatalf("status after restart = %q (exit %d)", out, code)
	}
	if out, code := run("daemon", "stop"); code != 0 {
		t.Fatalf("stop = %q (exit %d)", out, code)
	}
	awaitFinalStatus(t, run)
}

// awaitFinalStatus bounded-polls status until the stopped daemon's process
// has fully exited (stop returns once the socket is gone, which precedes
// process reap on some schedulers by a few ms).
func awaitFinalStatus(t *testing.T, run func(...string) (string, int)) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out, code := run("daemon", "status")
		if code == 0 && strings.Contains(out, "stale") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, code := run("daemon", "status"); code != 0 {
		t.Fatalf("final status exit = %d, want 0", code)
	}
}

// shortHomeDir returns a temp directory NOT rooted under t.TempDir() (see
// this function's callers' doc comments for why a unix socket needs one).
func shortHomeDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cascd")
	if err != nil {
		t.Fatalf("shortHomeDir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// findModuleRoot walks up from the current working directory to the
// nearest go.mod, for the real-binary build above.
func findModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod found walking up from cmd/cascade")
		}
		dir = parent
	}
}
