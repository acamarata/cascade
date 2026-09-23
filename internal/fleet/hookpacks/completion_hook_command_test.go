// Purpose: proves D1/R-16.74's shell-layer fix directly -- the RENDERED
// completion-gate command executed by /bin/sh (never handleCompletionHook
// called in-process) fails CLOSED against a REAL unix-socket responder on
// every outcome but a well-formed 2xx "deny":false response. This is the
// exact gap CR-B's Q1 named: "Zero tests exercise this shell template."
// The responder is the system `nc` binary (os/exec only): this file
// deliberately never imports "net"/"net/http" -- internal/build's own
// NoNetworkUnitTestScanFile gate (Art.7.2) and TestArchEgressImportAllowlist
// (the A-T2 egress firewall) both forbid those two imports outside their
// respective allowlists, and CI's own integration-tag package allowlist
// (ASI hard rule) does not cover this package, so tagging would silently
// drop this proof from every CI run instead of satisfying either gate.
// SPORT: fleet/hookpacks.completionHookCommand/ADD (P1-E32-W6-S66-T1).
package hookpacks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/hookpacks"
)

// renderedCompletionStopCommand builds the real "completion-gate" pack via
// the exported registration path (never re-derives the shell text) and
// returns its Stop descriptor's command rendered against socketPath.
func renderedCompletionStopCommand(t *testing.T, socketPath string) string {
	t.Helper()
	reg := hookpacks.NewHookRegistry()
	if err := hookpacks.RegisterCompletionHookPack(reg, fakeGate{ok: true}, fakeResolver{}); err != nil {
		t.Fatalf("RegisterCompletionHookPack: %v", err)
	}
	var pack hookpacks.HookPack
	for _, p := range reg.Packs() {
		if p.Name == "completion-gate" {
			pack = p
		}
	}
	if pack.Name == "" {
		t.Fatal("completion-gate pack not registered")
	}
	rendered, err := pack.Render(socketPath)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for i, d := range pack.Descriptors {
		if d.EventType != hookpacks.EventStop {
			continue
		}
		var entry struct {
			Hooks []struct{ Command string } `json:"hooks"`
		}
		if err := json.Unmarshal(rendered[i], &entry); err != nil {
			t.Fatalf("decode rendered[%d]: %v", i, err)
		}
		return entry.Hooks[0].Command
	}
	t.Fatal("Stop descriptor not found in completion-gate pack")
	return ""
}

// rawHTTPResponse builds one well-formed HTTP/1.1 response: an exact
// Content-Length and "Connection: close" so curl reads a complete body
// without depending on any further write from the fake responder.
func rawHTTPResponse(status int, body string) string {
	return fmt.Sprintf("HTTP/1.1 %d x\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		status, len(body), body)
}

// startFakeUnixResponder starts a background `nc -lU` process that binds
// sockPath immediately (verified by polling for the socket file, so the
// caller never races a curl connection against an unbound socket) and,
// after delay, writes resp to the first connection. delay=0 writes
// immediately. Returns the bound socket path.
func startFakeUnixResponder(t *testing.T, resp string, delay time.Duration) string {
	t.Helper()
	if _, err := exec.LookPath("nc"); err != nil {
		t.Skip("nc not available in this environment")
	}
	dir, err := os.MkdirTemp("", "s66t1")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "s.sock")
	respFile := filepath.Join(dir, "resp.txt")
	if err := os.WriteFile(respFile, []byte(resp), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	delaySeconds := int(delay / time.Second)
	shellCmd := fmt.Sprintf("(sleep %d; cat %q) | nc -lU %q", delaySeconds, respFile, sockPath)
	cmd := exec.Command("sh", "-c", shellCmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start nc responder: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})

	waitForSocket(t, sockPath)
	return sockPath
}

// waitForSocket blocks (bounded by t's own eventual subtest failure, not
// a raw loop) until sockPath exists or the deadline passes.
func waitForSocket(t *testing.T, sockPath string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(sockPath); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("nc responder never created socket %s", sockPath)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// runShellCommand runs cmd via /bin/sh (the harness's own execution
// model) with no stdin, returning its exit code and stderr.
func runShellCommand(ctx context.Context, t *testing.T, cmd string) (exitCode int, stderr string) {
	t.Helper()
	return runShellCommandWithStdin(ctx, t, cmd, "")
}

// runShellCommandWithStdin is runShellCommand plus an injected stdin --
// the harness's own real delivery channel for the native hook payload
// (completion_hook_command.go's `stdin_json=$(cat)`).
func runShellCommandWithStdin(ctx context.Context, t *testing.T, cmd, stdin string) (exitCode int, stderr string) {
	t.Helper()
	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	c.Stdin = strings.NewReader(stdin)
	var errBuf bytes.Buffer
	c.Stderr = &errBuf
	err := c.Run()
	if err == nil {
		return 0, errBuf.String()
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), errBuf.String()
	}
	t.Fatalf("run %q: %v", cmd, err)
	return -1, ""
}

// shellLayerCase is one TestCompletionHookCommand_ShellLayerFailsClosed
// scenario: the daemon-side response (or its absence) and the exit code
// the rendered command must produce for it.
type shellLayerCase struct {
	name     string
	resp     string
	delay    time.Duration
	noSocket bool
	wantExit int
	bound    time.Duration
}

// shellLayerCases is CR-B Q1's own prescribed list: no socket, and a real
// unix-socket responder returning deny/allow/empty/garbage/http-500/a
// timeout-inducing delay. Every outcome but the explicit allow must exit
// 2 with a one-line stderr reason; the previous template exited 0
// (allow) on every one of these but the literal "deny":true case.
func shellLayerCases() []shellLayerCase {
	return []shellLayerCase{
		{name: "no_socket", noSocket: true, wantExit: 2, bound: 5 * time.Second},
		{name: "deny", resp: rawHTTPResponse(200, `{"deny":true,"reason":"missing evidence"}`), wantExit: 2, bound: 5 * time.Second},
		{name: "allow", resp: rawHTTPResponse(200, `{"deny":false}`), wantExit: 0, bound: 5 * time.Second},
		{name: "empty_body", resp: rawHTTPResponse(200, ``), wantExit: 2, bound: 5 * time.Second},
		{name: "garbage_body", resp: rawHTTPResponse(200, `not json at all`), wantExit: 2, bound: 5 * time.Second},
		{name: "http_500", resp: rawHTTPResponse(500, `{"deny":false}`), wantExit: 2, bound: 5 * time.Second},
		{name: "sleeps_past_client_timeout", resp: rawHTTPResponse(200, `{"deny":false}`), delay: 16 * time.Second, wantExit: 2, bound: 20 * time.Second},
	}
}

// runShellLayerCase executes one shellLayerCase's rendered Stop command
// via /bin/sh and asserts its exit code and stderr shape.
func runShellLayerCase(t *testing.T, tc shellLayerCase) {
	t.Helper()
	var sockPath string
	if tc.noSocket {
		dir, err := os.MkdirTemp("", "s66t1")
		if err != nil {
			t.Fatalf("MkdirTemp: %v", err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		sockPath = filepath.Join(dir, "no-such.sock")
	} else {
		sockPath = startFakeUnixResponder(t, tc.resp, tc.delay)
	}
	cmd := renderedCompletionStopCommand(t, sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), tc.bound)
	defer cancel()
	exitCode, stderr := runShellCommand(ctx, t, cmd)
	if ctx.Err() != nil {
		t.Fatalf("command did not return within %s: %v", tc.bound, ctx.Err())
	}
	if exitCode != tc.wantExit {
		t.Fatalf("exit = %d (stderr=%q), want %d", exitCode, stderr, tc.wantExit)
	}
	if tc.wantExit == 2 && stderr == "" {
		t.Fatalf("a fail-closed exit carried no stderr reason")
	}
	if tc.wantExit == 0 && stderr != "" {
		t.Fatalf("an allow exit carried a stderr reason %q, want none", stderr)
	}
}

// TestCompletionHookCommand_ShellLayerFailsClosed is CR-B Q1's own
// prescribed proof, run over shellLayerCases: "Zero tests exercise this
// shell template."
func TestCompletionHookCommand_ShellLayerFailsClosed(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not available in this environment")
	}
	for _, tc := range shellLayerCases() {
		t.Run(tc.name, func(t *testing.T) { runShellLayerCase(t, tc) })
	}
}

// TestCompletionHookCommand_StopHookActiveNeverBypassesFailClosed proves
// stop_hook_active never flips the fail-closed decision: the REAL
// captured Stop fixture (stop_hook_active:false) and a synthetic
// stop_hook_active:true payload are both piped as the command's real
// stdin (the harness's own delivery channel) against the SAME denying
// responder, and both must still exit 2.
func TestCompletionHookCommand_StopHookActiveNeverBypassesFailClosed(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not available in this environment")
	}
	realCapture, err := os.ReadFile(filepath.Join("testdata", "completion", "stop_fixture.json"))
	if err != nil {
		t.Fatalf("read real Stop fixture: %v", err)
	}
	for _, tc := range []struct{ name, stdin string }{
		{"real_capture_stop_hook_active_false", string(realCapture)},
		{"synthetic_stop_hook_active_true", `{"stop_hook_active":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sockPath := startFakeUnixResponder(t, rawHTTPResponse(200, `{"deny":true,"reason":"missing evidence"}`), 0)
			cmd := renderedCompletionStopCommand(t, sockPath)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			exitCode, stderr := runShellCommandWithStdin(ctx, t, cmd, tc.stdin)
			if exitCode != 2 {
				t.Fatalf("exit = %d (stderr=%q), want 2 (fail-closed regardless of stop_hook_active)", exitCode, stderr)
			}
		})
	}
}

// TestCompletionHookCommand_RendersExactlyOneCurlCall pins the
// no-recursion half: the template is straight-line with no retry loop
// around stop_hook_active, so a Stop -> block -> Stop replay can never
// turn into more than one HTTP request per invocation.
func TestCompletionHookCommand_RendersExactlyOneCurlCall(t *testing.T) {
	cmd := renderedCompletionStopCommand(t, "/nonexistent.sock")
	// "$(curl " is the invocation form (a subshell call); "curl exit" in
	// the stderr message below does not match this narrower substring.
	if n := strings.Count(cmd, "$(curl "); n != 1 {
		t.Fatalf("rendered command invokes curl %d times, want exactly 1 (no recursive retry): %s", n, cmd)
	}
}
