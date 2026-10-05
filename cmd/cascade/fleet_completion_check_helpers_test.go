//go:build !windows

// Purpose: shared fixtures for the completion-check proofs: the real cascade
//
//	binary built into a temp dir, the real completion-gate pack rendered
//	against it, a live in-process daemon (the same seam the sessions hook
//	tests use) with an optional capturing handler mounted over the real
//	completion handler, a raw `nc` responder for replies a real daemon would
//	never send, and a runner that executes the rendered command under
//	/bin/sh. No test file here imports net: the raw responder is the system
//	nc over os/exec, as the hookpacks shell-layer tests already do.
//
// SPORT: cmd/cascade/fleet-completion-check (coverage for P1-CI-09).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/hookpacks"
	"github.com/acamarata/cascade/internal/rpc"
)

// completionRPCID is the id the client SDK sends for the completion check.
const completionRPCID = "cascade-client:" + hookpacks.MethodCompletionCheck

// completionBinaryImage is the real cascade binary's bytes, built once per
// test process: linking it takes tens of seconds, and every test wants its own
// copy under its own t.TempDir.
var (
	completionBinaryOnce  sync.Once
	completionBinaryImage []byte
	completionBinaryErr   error
)

// buildCascadeBinary writes the real cascade binary into this test's temp dir
// and returns its path.
func buildCascadeBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("short mode: skips the real-binary build")
	}
	completionBinaryOnce.Do(func() {
		built := filepath.Join(t.TempDir(), "cascade")
		if out, err := exec.Command("go", "build", "-o", built, ".").CombinedOutput(); err != nil {
			completionBinaryErr = fmt.Errorf("go build: %w\n%s", err, out)
			return
		}
		completionBinaryImage, completionBinaryErr = os.ReadFile(built) //nolint:gosec // path built just above.
	})
	if completionBinaryErr != nil {
		t.Fatalf("build cascade binary: %v", completionBinaryErr)
	}
	bin := filepath.Join(t.TempDir(), "cascade")
	if err := os.WriteFile(bin, completionBinaryImage, 0o700); err != nil { //nolint:gosec // a test binary must be executable.
		t.Fatalf("write cascade binary: %v", err)
	}
	return bin
}

// renderedCompletionCommand renders the real completion-gate pack against
// socket and bin and returns evt's command.
func renderedCompletionCommand(t *testing.T, bin, socket string, evt hookpacks.HookEventType) string {
	t.Helper()
	reg := hookpacks.NewHookRegistry()
	if err := hookpacks.RegisterCompletionHookPack(reg, noopCompletionGate{}, noopJobResolver{}); err != nil {
		t.Fatalf("RegisterCompletionHookPack: %v", err)
	}
	for _, pack := range reg.Packs() {
		if pack.Name != "completion-gate" {
			continue
		}
		rendered, err := pack.RenderWithBinary(socket, bin)
		if err != nil {
			t.Fatalf("RenderWithBinary: %v", err)
		}
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
	}
	t.Fatalf("no %s descriptor in the completion-gate pack", evt)
	return ""
}

// shRun is one /bin/sh invocation's observable result.
type shRun struct {
	code           int
	stdout, stderr string
}

// runShell runs command under /bin/sh with a clean environment (HOME and
// USERPROFILE redirected into a temp dir) plus extraEnv, and stdin.
func runShell(t *testing.T, command string, extraEnv []string, stdin string, bound time.Duration) shRun {
	t.Helper()
	home := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), bound)
	defer cancel()
	c := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	c.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "USERPROFILE=" + home}, extraEnv...)
	c.Stdin = strings.NewReader(stdin)
	var out, errBuf bytes.Buffer
	c.Stdout, c.Stderr = &out, &errBuf
	err := c.Run()
	if ctx.Err() != nil {
		t.Fatalf("command did not return within %s", bound)
	}
	res := shRun{stdout: out.String(), stderr: errBuf.String()}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run /bin/sh: %v", err)
	}
	return res
}

// completionCapture records every raw params object the daemon receives for
// the completion check and answers with reply.
type completionCapture struct {
	mu    sync.Mutex
	raws  []json.RawMessage
	reply hookpacks.CompletionHookResponse
}

func (c *completionCapture) register(registry *rpc.Registry) error {
	registry.Register(hookpacks.MethodCompletionCheck, func(_ context.Context, raw json.RawMessage) (any, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.raws = append(c.raws, append(json.RawMessage(nil), raw...))
		return c.reply, nil
	})
	return nil
}

func (c *completionCapture) received() []json.RawMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]json.RawMessage(nil), c.raws...)
}

// startCompletionDaemon serves a live daemon over a throwaway cascade home.
// A non-nil mount registers over the real completion handler the daemon
// wires itself (after the daemon's own registrations, so it wins); nil leaves the real handler and gate in place. It returns the
// socket path and a stop func (idempotent).
func startCompletionDaemon(t *testing.T, mount func(*rpc.Registry) error) (string, func()) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	runDeps := newRunTestDeps(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	_, paths, settings, err := loadDaemonConfig(ctx, runDeps)
	if err != nil {
		t.Fatalf("loadDaemonConfig: %v", err)
	}
	store, _, closeStore, err := openRuntimeStore(ctx, paths, runDeps.Clock)
	if err != nil {
		t.Fatalf("openRuntimeStore: %v", err)
	}
	bus := events.New(store, runDeps.Clock)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	opts := []rpcServerOption{}
	if mount != nil {
		// register runs BEFORE the fleet-phase wiring, which would overwrite
		// the handler; observe runs after the whole registry is built.
		opts = append(opts, rpcServerOption{observe: func(registry *rpc.Registry, _ *rpc.SSEMux) {
			if err := mount(registry); err != nil {
				t.Errorf("mount over the completion handler: %v", err)
			}
		}})
	}
	server, manifest, connections, err := buildRPCServer(bus, runDeps.Clock, logger, settings, paths, nil, store, opts...)
	if err != nil {
		t.Fatalf("buildRPCServer: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- daemon.Run(ctx, daemon.RunOptions{
			Settings: settings, PIDPath: daemon.PIDFilePath(paths), Logger: logger, Clock: runDeps.Clock,
			Server: server, Environ: runDeps.Environ, Manifest: manifest, Connections: connections,
		})
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			<-done
			closeStore()
		})
	}
	t.Cleanup(stop)
	waitForSocket(t, paths.SocketPath())
	return paths.SocketPath(), stop
}

// rawHTTP builds one well-formed HTTP/1.1 response with an exact
// Content-Length, so the client reads a complete body.
func rawHTTP(status int, body string) string {
	return fmt.Sprintf("HTTP/1.1 %d x\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		status, len(body), body)
}

// startRawResponder binds a unix socket with the system nc and, after delay,
// writes resp to the first connection. It returns once the socket exists.
func startRawResponder(t *testing.T, resp string, delay time.Duration) string {
	t.Helper()
	if _, err := exec.LookPath("nc"); err != nil {
		t.Skip("nc not available in this environment")
	}
	dir, err := os.MkdirTemp("", "cc09")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock, respFile := filepath.Join(dir, "s.sock"), filepath.Join(dir, "resp.txt")
	if err := os.WriteFile(respFile, []byte(resp), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cmd := exec.Command("sh", "-c", fmt.Sprintf("(sleep %d; cat %q) | nc -lU %q", int(delay/time.Second), respFile, sock))
	if err := cmd.Start(); err != nil {
		t.Fatalf("start nc responder: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(sock); err == nil {
			return sock
		}
		if time.Now().After(deadline) {
			t.Fatalf("nc responder never created %s", sock)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// envelope wraps a result object in the JSON-RPC envelope the client expects.
func envelope(result string) string {
	return `{"jsonrpc":"2.0","id":"` + completionRPCID + `","result":` + result + `}`
}
