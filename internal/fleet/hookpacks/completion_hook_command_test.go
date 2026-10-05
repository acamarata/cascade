// Purpose: proves the completion-gate hook command as the harness runs it:
// the RENDERED command executed by /bin/sh (never handleCompletionHook
// called in-process) against the REAL cascade binary, built into the test's
// temp dir, and a raw unix-socket responder. The command is one absolute-path
// invocation of the hidden `fleet completion-check` subcommand; the
// fail-closed table and the reply-decoding proofs live beside that
// subcommand (cmd/cascade/fleet_completion_check_gate_test.go), because they
// need cmd/cascade's own daemon fixtures. What this file pins is the rendered
// text (absolute path, single invocation, no shell-side JSON handling) and
// that stop_hook_active never turns a denial into an allow.
// The responder is the system `nc` binary (os/exec only): this file
// deliberately never imports "net"/"net/http" -- internal/build's own
// NoNetworkUnitTestScanFile gate (Art.7.2) and TestArchEgressImportAllowlist
// (the A-T2 egress firewall) both forbid those two imports outside their
// respective allowlists.
// SPORT: fleet/hookpacks.completionHookCommand/ADD (P1-E32-W6-S66-T1),
// CHANGE (P1-CI-09).
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
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/hookpacks"
)

// completionRPCID is the id the client SDK sends for the completion check.
const completionRPCID = "cascade-client:" + hookpacks.MethodCompletionCheck

// completionPack returns the real "completion-gate" pack, registered through
// the exported registration path (never re-deriving the command text).
func completionPack(t *testing.T) hookpacks.HookPack {
	t.Helper()
	reg := hookpacks.NewHookRegistry()
	if err := hookpacks.RegisterCompletionHookPack(reg, fakeGate{ok: true}, fakeResolver{}); err != nil {
		t.Fatalf("RegisterCompletionHookPack: %v", err)
	}
	for _, p := range reg.Packs() {
		if p.Name == "completion-gate" {
			return p
		}
	}
	t.Fatal("completion-gate pack not registered")
	return hookpacks.HookPack{}
}

// commandOf extracts the command of the descriptor for evt from rendered.
func commandOf(t *testing.T, pack hookpacks.HookPack, rendered []json.RawMessage, evt hookpacks.HookEventType) string {
	t.Helper()
	for i, d := range pack.Descriptors {
		if d.EventType != evt {
			continue
		}
		var entry struct {
			Hooks []struct{ Command string } `json:"hooks"`
		}
		if err := json.Unmarshal(rendered[i], &entry); err != nil || len(entry.Hooks) != 1 {
			t.Fatalf("decode rendered[%d]: %v (%d hooks)", i, err, len(entry.Hooks))
		}
		return entry.Hooks[0].Command
	}
	t.Fatalf("%s descriptor not found in the completion-gate pack", evt)
	return ""
}

// renderedCompletionStopCommand renders the pack's Stop command against
// socketPath and the given binary.
func renderedCompletionStopCommand(t *testing.T, socketPath, binary string) string {
	t.Helper()
	pack := completionPack(t)
	rendered, err := pack.RenderWithBinary(socketPath, binary)
	if err != nil {
		t.Fatalf("RenderWithBinary: %v", err)
	}
	return commandOf(t, pack, rendered, hookpacks.EventStop)
}

// cascadeBinary builds the real cascade binary once per test process and
// writes a copy into this test's temp dir.
var (
	cascadeBinaryOnce  sync.Once
	cascadeBinaryImage []byte
	cascadeBinaryErr   error
)

func cascadeBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("short mode: skips the real-binary build")
	}
	cascadeBinaryOnce.Do(func() {
		built := filepath.Join(t.TempDir(), "cascade")
		if out, err := exec.Command("go", "build", "-o", built, "github.com/acamarata/cascade/cmd/cascade").CombinedOutput(); err != nil {
			cascadeBinaryErr = fmt.Errorf("go build: %w\n%s", err, out)
			return
		}
		cascadeBinaryImage, cascadeBinaryErr = os.ReadFile(built) //nolint:gosec // path built just above.
	})
	if cascadeBinaryErr != nil {
		t.Fatalf("build cascade binary: %v", cascadeBinaryErr)
	}
	bin := filepath.Join(t.TempDir(), "cascade")
	if err := os.WriteFile(bin, cascadeBinaryImage, 0o700); err != nil { //nolint:gosec // a test binary must be executable.
		t.Fatalf("write cascade binary: %v", err)
	}
	return bin
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
// caller never races a connection against an unbound socket) and writes
// resp to the first connection. Returns the bound socket path.
func startFakeUnixResponder(t *testing.T, resp string) string {
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

	shellCmd := fmt.Sprintf("cat %q | nc -lU %q", respFile, sockPath)
	cmd := exec.Command("sh", "-c", shellCmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start nc responder: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
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

// runShellCommandWithStdin runs cmd via /bin/sh (the harness's own execution
// model) with an injected stdin -- the harness's own real delivery channel for
// the native hook payload -- returning its exit code and stderr.
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

// TestCompletionHookCommand_StopHookActiveNeverBypassesFailClosed proves
// stop_hook_active never flips the decision: the REAL captured Stop fixture
// (stop_hook_active:false) and a synthetic stop_hook_active:true payload are
// both piped as the command's real stdin against the SAME denying reply, and
// both must exit 2 carrying the daemon's reason (a vacuous exit 2 from a
// broken command would not carry it). A control with an allowing reply and
// stop_hook_active:true must still exit 0, so the flag moves nothing either way.
func TestCompletionHookCommand_StopHookActiveNeverBypassesFailClosed(t *testing.T) {
	bin := cascadeBinary(t)
	realCapture, err := os.ReadFile(filepath.Join("testdata", "completion", "stop_fixture.json"))
	if err != nil {
		t.Fatalf("read real Stop fixture: %v", err)
	}
	deny := rawHTTPResponse(200, `{"jsonrpc":"2.0","id":"`+completionRPCID+`","result":{"deny":true,"reason":"missing evidence"}}`)
	allow := rawHTTPResponse(200, `{"jsonrpc":"2.0","id":"`+completionRPCID+`","result":{"deny":false}}`)
	for _, tc := range []struct {
		name, stdin, reply string
		wantExit           int
	}{
		{"real_capture_stop_hook_active_false", string(realCapture), deny, 2},
		{"synthetic_stop_hook_active_true", `{"stop_hook_active":true}`, deny, 2},
		{"control_allow_with_stop_hook_active_true", `{"stop_hook_active":true}`, allow, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sockPath := startFakeUnixResponder(t, tc.reply)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			exitCode, stderr := runShellCommandWithStdin(ctx, t, renderedCompletionStopCommand(t, sockPath, bin), tc.stdin)
			if exitCode != tc.wantExit {
				t.Fatalf("exit = %d (stderr=%q), want %d", exitCode, stderr, tc.wantExit)
			}
			if tc.wantExit == 2 && !strings.Contains(stderr, "missing evidence") {
				t.Fatalf("stderr = %q, want the daemon's reason", stderr)
			}
		})
	}
}

// TestCompletionHookCommandUsesAbsolutePath pins that the rendered command
// names the cascade binary by the absolute, symlink-resolved path of the
// running executable, single-quoted, and never resolves it through PATH.
func TestCompletionHookCommandUsesAbsolutePath(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exe = filepath.Clean(exe)
	checkCompletionExecutableSymlink(t, exe)
	pack := completionPack(t)
	rendered, err := pack.Render("/tmp/c.sock")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, evt := range []hookpacks.HookEventType{hookpacks.EventTaskCompleted, hookpacks.EventStop} {
		want := "'" + exe + "' fleet completion-check --event " + string(evt) + " --socket '/tmp/c.sock' || exit 2"
		if got := commandOf(t, pack, rendered, evt); got != want {
			t.Fatalf("%s command = %q, want %q", evt, got, want)
		}
	}
	if !filepath.IsAbs(exe) {
		t.Fatalf("resolved executable %q is not absolute", exe)
	}
	if _, err := pack.RenderWithBinary("/tmp/c.sock", "cascade"); !errors.Is(err, hookpacks.ErrBinaryPathNotAbsolute) {
		t.Fatalf("RenderWithBinary with a bare name: err = %v, want ErrBinaryPathNotAbsolute", err)
	}
	if _, err := pack.RenderWithBinary("/tmp/c.sock", ""); !errors.Is(err, hookpacks.ErrBinaryPathNotAbsolute) {
		t.Fatalf("RenderWithBinary with an empty path: err = %v, want ErrBinaryPathNotAbsolute", err)
	}
}

// TestCompletionHookCommand_RendersExactlyOneCall pins the no-recursion
// half: the command is one straight-line invocation of the subcommand, with
// no retry loop, no second call and no shell-side JSON handling, so a
// Stop -> block -> Stop replay can never turn into more than one request per
// invocation.
func TestCompletionHookCommand_RendersExactlyOneCall(t *testing.T) {
	cmd := renderedCompletionStopCommand(t, "/nonexistent.sock", "/opt/cascade")
	if n := strings.Count(cmd, "completion-check"); n != 1 {
		t.Fatalf("rendered command invokes completion-check %d times, want exactly 1: %s", n, cmd)
	}
	for _, banned := range []string{"curl", "$(", "`", "sed", "|| true", "; ", "&&", "while", "for "} {
		if strings.Contains(cmd, banned) {
			t.Fatalf("rendered command contains %q, want one plain invocation: %s", banned, cmd)
		}
	}
	if n := strings.Count(cmd, "||"); n != 1 || !strings.HasSuffix(cmd, " || exit 2") {
		t.Fatalf("rendered command must end in exactly one `|| exit 2`: %s", cmd)
	}
}

// TestCompletionClientBudgetIsServerTimeoutPlusSlack pins the client deadline
// the completion-check subcommand puts on its daemon round trip: the 10 s
// server completion timeout plus the 5 s client slack. A budget at or below
// the server timeout would let the client give up before the daemon answers
// and turn every slow allow into a fail-closed denial.
func TestCompletionClientBudgetIsServerTimeoutPlusSlack(t *testing.T) {
	if got, want := hookpacks.CompletionClientBudget(), 15*time.Second; got != want {
		t.Fatalf("CompletionClientBudget() = %v, want %v (10 s server timeout + 5 s slack)", got, want)
	}
}
