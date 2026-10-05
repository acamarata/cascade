//go:build !windows && integration

// Purpose: the `cascade run` side of the child-daemon harness: the real run
//
//	command in process, dialing the child daemon's socket, with stdout and
//	stderr captured (the request id is printed to stderr before dispatch).
//
// SPORT: cmd/cascade/daemon (CHANGE, P1-CORE-15).
package main

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"
)

// cliResult is one in-process `cascade run` against the child.
type cliResult struct {
	stdout, stderr string
	err            error
}

// requestID is the fan-out id the CLI printed before dispatch.
func (r cliResult) requestID(t *testing.T) string {
	t.Helper()
	_, after, ok := strings.Cut(r.stderr, "cascade: request id ")
	if !ok || len(after) < 26 {
		t.Fatalf("no request id in the run's stderr %q", r.stderr)
	}
	return after[:26]
}

// outputs counts the leg outputs of the recorded reply in stdout.
func (r cliResult) outputs() int { return strings.Count(r.stdout, "How can I help") }

// runFan runs `cascade run --fan-out n` (with --resume when id is set)
// dialing the child's socket.
func (h *resumeHome) runFan(t *testing.T, n, id, prompt string) cliResult {
	t.Helper()
	sock := h.sock()
	deps := runDeps{Paths: fakeDaemonPaths{root: t.TempDir()}, Getenv: func(string) string { return "" },
		Environ: func() []string { return nil },
		DialContext: func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		}}
	args := []string{"--task", "chat", "--fan-out", n, "--sensitivity", "public", "--input", prompt}
	if id != "" {
		args = append(args, "--resume", id)
	}
	cmd, out, errb := newRunCmd(deps), &bytes.Buffer{}, &bytes.Buffer{}
	cmd.SetArgs(args)
	cmd.SetOut(out)
	cmd.SetErr(errb)
	cmd.SetIn(bytes.NewReader(nil))
	err := cmd.ExecuteContext(context.Background())
	return cliResult{stdout: out.String(), stderr: errb.String(), err: err}
}

// asyncFan runs runFan in the background.
func (h *resumeHome) asyncFan(t *testing.T, n, id, prompt string) chan cliResult {
	ch := make(chan cliResult, 1)
	go func() { ch <- h.runFan(t, n, id, prompt) }()
	return ch
}
