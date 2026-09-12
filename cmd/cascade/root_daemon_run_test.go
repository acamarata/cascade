// Purpose: proves DEFECT-daemon-run-prints-daemonless-warning.md's fix —
//
//	`cascade daemon run`, driven through the real cobra entry point
//	(newRootCmd/ExecuteContext, exactly as daemon_test.go's execDaemon
//	does for the other daemon verbs), must never print
//	probeDaemonlessAndAttach's "daemon not running; running in embedded
//	(daemonless) mode" warning about its own startup. Every OTHER command
//	(daemon status here) still gets that warning when no daemon is up, so
//	this file also proves the suppression is scoped to `daemon run` and
//	does not silence the probe globally.
//
// Constraints: Art.7.1 — CASCADE_HOME is a short os.MkdirTemp() dir, not
//
//	t.TempDir() (whose path embeds the full test name and can exceed
//	sockaddr_un.sun_path on darwin — same fix daemon_unix_run_test.go
//	documents). The run is cancelled as soon as its own socket answers,
//	never on a fixed delay (same race daemon_unix_run_test.go's comment
//	explains). probeDaemonlessAndAttach's Warn call goes through
//	output.NewDefault, which intentionally hardcodes the real os.Stderr
//	(see its doc comment) rather than the cobra command's SetErr buffer,
//	so this file captures the real fd via os.Pipe instead of reading
//	cmd output — no test in this package runs t.Parallel(), so swapping
//	the process-global os.Stderr for the duration of one test is safe.
//
// SPORT: cmd/cascade — cobra-root, global-flags, version, completions
//
//	(DEFECT fix, no new sport_updates: no new exported surface).
//
// Build constraint: this file drives socketDialable, which daemon_unix.go
// defines behind //go:build !windows. `go build` never compiles test files,
// so an untagged test referencing a unix-only symbol is invisible until
// `GOOS=windows go vet ./...` runs — the same build-tag parity trap this
// phase has hit before. The tag keeps the Windows vet lane green without
// weakening what this test proves on the platforms that have a unix socket.
//go:build !windows

package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

// shortCascadeHome lives in testhelper_home_test.go (untagged): it is shared
// with platform-neutral tests that must keep compiling on Windows, so it
// cannot inherit this file's !windows constraint.

// captureRealStderr redirects the process's real os.Stderr fd for the
// duration of fn and returns everything written to it. probeDaemonlessAndAttach
// warns through output.NewDefault, which writes the real os.Stderr directly
// (never a cobra command's own SetErr stream), so this is the only way to
// observe that warning from a test in this process.
func captureRealStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	fn()
	os.Stderr = orig
	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stderr: %v", err)
	}
	return string(out)
}

// runDaemonUntilServing drives `cascade daemon <args...>` through the real
// root command tree and cancels the run as soon as its own socket answers
// (or after the outer bound elapses, so a daemon that never comes up fails
// the test instead of hanging forever). CASCADE_HOME/HOME are set by the
// caller (via t.Setenv) before this runs, so the pipe capturing
// probeDaemonlessAndAttach's warning is already installed by then.
func runDaemonUntilServing(t *testing.T, args ...string) {
	t.Helper()
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	root.SetArgs(append([]string{"daemon"}, args...))

	paths, err := runtime.NewDefaultPathProvider()
	if err != nil {
		t.Fatalf("NewDefaultPathProvider: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if socketDialable(paths.SocketPath()) {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()

	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("daemon %v: %v", args, err)
	}
}

// TestDaemonRunCmd_NoSpuriousDaemonlessWarning is the mutation-proof target:
// reverting isDaemonRunCmd's check in root.go's probeDaemonlessAndAttach
// makes this fail with the warning present in the captured output.
func TestDaemonRunCmd_NoSpuriousDaemonlessWarning(t *testing.T) {
	home := shortCascadeHome(t)
	t.Setenv("CASCADE_HOME", home)
	t.Setenv("HOME", home)

	out := captureRealStderr(t, func() {
		runDaemonUntilServing(t, "run")
	})
	if strings.Contains(out, "daemon not running") {
		t.Fatalf("`daemon run` printed the daemonless warning about itself: %q", out)
	}
	if strings.Contains(out, "embedded (daemonless) mode") {
		t.Fatalf("`daemon run` printed the embedded-mode warning about itself: %q", out)
	}
}

// TestDaemonStatusCmd_StillWarnsWhenEmbedded proves the suppression is
// scoped to `daemon run` alone: `daemon status`, run against a CASCADE_HOME
// with no daemon listening, must still get probeDaemonlessAndAttach's
// warning exactly as before this fix.
func TestDaemonStatusCmd_StillWarnsWhenEmbedded(t *testing.T) {
	home := shortCascadeHome(t)
	t.Setenv("CASCADE_HOME", home)
	t.Setenv("HOME", home)

	out := captureRealStderr(t, func() {
		globalFlags = GlobalFlags{}
		root := newRootCmd()
		root.SetArgs([]string{"daemon", "status"})
		if err := root.Execute(); err != nil {
			t.Fatalf("daemon status: %v", err)
		}
	})
	if !strings.Contains(out, "daemon not running") {
		t.Fatalf("daemon status lost the daemonless warning: %q", out)
	}
}
