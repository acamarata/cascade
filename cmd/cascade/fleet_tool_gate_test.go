//go:build !windows

// Purpose: proofs for the tool-gate hook command, run as the harness runs
//
//	it: the real tool-gate pack rendered against the real cascade binary and
//	executed under /bin/sh, with a raw nc unix-socket responder (or a live
//	daemon for the stopped row) on the other end. Every non-allow outcome
//	must exit 2 with a one-line stderr reason; only {"deny":false} exits 0;
//	stdin reaches the wire byte for byte. No file here imports net.
//
// SPORT: cmd/cascade/fleet-tool-gate (coverage).
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/hookpacks"
)

// toolGateStdin is a well-formed hook payload for rows that need one.
const toolGateStdin = `{"hook_event_name":"PreToolUse","tool_name":"Bash"}`

// toolGateReplyOf wraps a result object in a reply envelope with the right id.
func toolGateReplyOf(result string) string {
	return rawHTTP(200, `{"jsonrpc":"2.0","id":"`+toolGateRPCID+`","result":`+result+`}`)
}

// renderedToolGateCommand renders the real tool-gate pack against socket and
// bin, and asserts the binary token was substituted with the quoted path.
func renderedToolGateCommand(t *testing.T, bin, socket string) string {
	t.Helper()
	rendered, err := hookpacks.ToolGatePack().RenderWithBinary(socket, bin)
	if err != nil || len(rendered) != 1 {
		t.Fatalf("RenderWithBinary: %v (%d entries)", err, len(rendered))
	}
	var entry struct {
		Hooks []struct{ Command string } `json:"hooks"`
	}
	if err := json.Unmarshal(rendered[0], &entry); err != nil || len(entry.Hooks) != 1 {
		t.Fatalf("decode rendered entry: %v", err)
	}
	command := entry.Hooks[0].Command
	if !filepath.IsAbs(bin) || !strings.HasPrefix(command, "'"+bin+"' ") {
		t.Fatalf("command %q does not start with the quoted absolute binary %q", command, bin)
	}
	if strings.Contains(command, "{{") {
		t.Fatalf("command %q still carries a placeholder", command)
	}
	return command
}

// runToolGateShell runs command under /bin/sh with a fresh CASCADE_HOME.
func runToolGateShell(t *testing.T, command, stdin string, extraEnv ...string) shRun {
	t.Helper()
	env := append([]string{"CASCADE_HOME=" + t.TempDir()}, extraEnv...)
	return runShell(t, command, env, stdin, 30*time.Second)
}

// assertOneLineRefusal fails unless res is exit 2 with one stderr line
// carrying want.
func assertOneLineRefusal(t *testing.T, res shRun, want string) {
	t.Helper()
	if res.code != 2 {
		t.Fatalf("exit = %d (stdout=%q stderr=%q), want 2", res.code, res.stdout, res.stderr)
	}
	trimmed := strings.TrimRight(res.stderr, "\n")
	if strings.TrimSpace(trimmed) == "" || strings.Contains(trimmed, "\n") {
		t.Fatalf("stderr = %q, want exactly one reason line", res.stderr)
	}
	if !strings.Contains(trimmed, want) {
		t.Fatalf("stderr = %q, want it to mention %q", res.stderr, want)
	}
}

// toolGateRow is one fail-closed scenario.
type toolGateRow struct {
	name, stdin, reason string
	socket              func(t *testing.T) string
	command             func(t *testing.T, bin, sock string) string
}

func allowResponder(t *testing.T) string {
	return startRawResponder(t, toolGateReplyOf(`{"deny":false}`), 0)
}

func toolGateFailRows() []toolGateRow {
	return []toolGateRow{
		{name: "daemon_stopped", stdin: toolGateStdin, reason: "unavailable", socket: func(t *testing.T) string {
			sock, stop := startCompletionDaemon(t, nil)
			stop()
			return sock
		}},
		{name: "socket_missing", stdin: toolGateStdin, reason: "unavailable", socket: func(t *testing.T) string {
			return filepath.Join(t.TempDir(), "none.sock")
		}},
		{name: "rpc_deadline", stdin: toolGateStdin, reason: "timeout", socket: func(t *testing.T) string {
			return startRawResponder(t, toolGateReplyOf(`{"deny":false}`), time.Duration(hookpacks.ToolGateClientDeadline+3)*time.Second)
		}},
		{name: "binary_missing", stdin: toolGateStdin, socket: allowResponder,
			command: func(t *testing.T, _, sock string) string {
				return renderedToolGateCommand(t, filepath.Join(t.TempDir(), "absent", "cascade"), sock)
			}},
		{name: "empty_stdin", stdin: "", reason: "empty stdin", socket: allowResponder},
		{name: "malformed_stdin", stdin: `{"tool_name":`, reason: "not JSON", socket: allowResponder},
		{name: "malformed_body", stdin: toolGateStdin, reason: "malformed", socket: func(t *testing.T) string {
			return startRawResponder(t, rawHTTP(200, `not json at all`), 0)
		}},
		{name: "deny_true", stdin: toolGateStdin, reason: "blocked by taint", socket: func(t *testing.T) string {
			return startRawResponder(t, toolGateReplyOf(`{"deny":true,"reason":"blocked by taint"}`), 0)
		}},
	}
}

func TestToolGateCommandFailsClosed(t *testing.T) {
	bin := buildCascadeBinary(t)
	for _, row := range toolGateFailRows() {
		t.Run(row.name, func(t *testing.T) {
			sock := row.socket(t)
			command := renderedToolGateCommand(t, bin, sock)
			if row.command != nil {
				command = row.command(t, bin, sock)
			}
			assertOneLineRefusal(t, runToolGateShell(t, command, row.stdin), row.reason)
		})
	}
}

func TestToolGateCommandAllowsOnlyExplicitFalse(t *testing.T) {
	bin := buildCascadeBinary(t)
	t.Run("explicit_false", func(t *testing.T) {
		res := runToolGateShell(t, renderedToolGateCommand(t, bin, allowResponder(t)), toolGateStdin)
		if res.code != 0 || res.stderr != "" {
			t.Fatalf("exit=%d stderr=%q, want a silent 0", res.code, res.stderr)
		}
	})
	refused := map[string]string{
		"empty_result": toolGateReplyOf(`{}`),
		"null_deny":    toolGateReplyOf(`{"deny":null}`),
		"string_false": toolGateReplyOf(`{"deny":"false"}`),
		"zero_deny":    toolGateReplyOf(`{"deny":0}`),
		"rpc_error":    rawHTTP(200, `{"jsonrpc":"2.0","id":"`+toolGateRPCID+`","error":{"code":-32603,"message":"boom"}}`),
	}
	for name, reply := range refused {
		t.Run(name, func(t *testing.T) {
			sock := startRawResponder(t, reply, 0)
			assertOneLineRefusal(t, runToolGateShell(t, renderedToolGateCommand(t, bin, sock), toolGateStdin), "tool_gate")
		})
	}
}

// startRecordingResponder is startRawResponder that also copies every request
// byte into a file; it returns the socket and that file's path.
func startRecordingResponder(t *testing.T, resp string) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("nc"); err != nil {
		t.Skip("nc not available in this environment")
	}
	// A short dir: sun_path holds 104 bytes, and a test-named temp dir can exceed it.
	dir, err := os.MkdirTemp("", "tg")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock, respFile, reqFile := filepath.Join(dir, "s.sock"), filepath.Join(dir, "resp.txt"), filepath.Join(dir, "req.bin")
	if err := os.WriteFile(respFile, []byte(resp), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	fifo := filepath.Join(dir, "reply.fifo")
	cmd := exec.Command("sh", "-c", fmt.Sprintf(
		"mkfifo %q && nc %s-lU %q <%q | tee %q | { head -c 1 >/dev/null; cat %q; exec >&-; cat >/dev/null; } >%q",
		fifo, ncCloseFlag(), sock, fifo, reqFile, respFile, fifo))
	if err := cmd.Start(); err != nil {
		t.Fatalf("start nc recorder: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(sock); err == nil {
			return sock, reqFile
		}
		if time.Now().After(deadline) {
			t.Fatalf("nc recorder never created %s", sock)
		}
	}
}

// recordedRPCBody waits until reqFile holds a whole HTTP request and returns
// its body.
func recordedRPCBody(t *testing.T, reqFile string) []byte {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		raw, _ := os.ReadFile(reqFile) //nolint:gosec // a path under this test's temp dir.
		if head, body, ok := bytes.Cut(raw, []byte("\r\n\r\n")); ok {
			for _, line := range strings.Split(string(head), "\r\n") {
				name, value, _ := strings.Cut(line, ":")
				if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && strings.EqualFold(name, "Content-Length") && len(body) >= n {
					return body[:n]
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no complete request recorded: %q", raw)
		}
	}
}

func TestToolGateCommandForwardsStdinVerbatim(t *testing.T) {
	bin := buildCascadeBinary(t)
	const sentinel = "sentinel-session-7f3a"
	stdin := "{\n  \"hook_event_name\": \"PreToolUse\",\n  \"tool_name\": \"Bash\",\n" +
		"  \"tool_input\": {\n    \"command\": \"a < b && c > d\",\n    \"note\": \"café ✓\"\n  }\n}\n"
	sock, reqFile := startRecordingResponder(t, toolGateReplyOf(`{"deny":false}`))
	res := runToolGateShell(t, renderedToolGateCommand(t, bin, sock), stdin, "CASCADE_SESSION_ID="+sentinel)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%q)", res.code, res.stderr)
	}
	body := recordedRPCBody(t, reqFile)
	var req struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("request body is not JSON: %v (%q)", err, body)
	}
	if req.Method != "fleet.sessions.tool_gate" {
		t.Fatalf("method = %q, want fleet.sessions.tool_gate", req.Method)
	}
	_, params, ok := bytes.Cut(body, []byte(`"params":`))
	if !ok || !bytes.Equal(bytes.TrimSuffix(params, []byte("}")), []byte(stdin)) {
		t.Fatalf("params bytes differ from stdin:\n got %q\nwant %q", params, stdin)
	}
	if raw, _ := os.ReadFile(reqFile); bytes.Contains(raw, []byte(sentinel)) { //nolint:gosec // a path under this test's temp dir.
		t.Fatal("CASCADE_SESSION_ID reached the wire")
	}
}
