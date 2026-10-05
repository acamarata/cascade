// Purpose: proves `cascade daemon logs [-f]` against real files under
// t.TempDir(): it prints the log without a daemon, -f streams appended lines,
// follows across a rotation (the rotated-in file's lines appear) and stops
// when the command's context is cancelled.
// Constraints: follow mode is polled, so assertions wait on the output with a
// deadline rather than sleeping a fixed time.
// SPORT: cmd/cascade/daemon (CHANGE, daemon logs tests).
package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

// logsBuffer is a bytes.Buffer safe to read while the command writes to it.
type logsBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logsBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logsBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// logsFixture is a log file under a fresh PathProvider root and the command
// built over it.
type logsFixture struct {
	path     string
	out, err *logsBuffer
	run      func(ctx context.Context, args ...string) error
}

func newLogsFixture(t *testing.T, initial string) logsFixture {
	t.Helper()
	paths := fakeDaemonPaths{root: t.TempDir()}
	f := logsFixture{path: runtime.LogFilePath(paths), out: &logsBuffer{}, err: &logsBuffer{}}
	if initial != "" {
		if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
			t.Fatalf("mkdir log dir: %v", err)
		}
		f.write(t, f.path, initial)
	}
	f.run = func(ctx context.Context, args ...string) error {
		cmd := newDaemonLogsCmdPolled(daemonDeps{Paths: paths}, 5*time.Millisecond)
		cmd.SetOut(f.out)
		cmd.SetErr(f.err)
		cmd.SetArgs(args)
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		return cmd.ExecuteContext(ctx)
	}
	return f
}

func (logsFixture) write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func (f logsFixture) append(t *testing.T, content string) {
	t.Helper()
	h, err := os.OpenFile(f.path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open log for append: %v", err)
	}
	defer func() { _ = h.Close() }()
	if _, err := h.WriteString(content); err != nil {
		t.Fatalf("append: %v", err)
	}
}

// waitOut waits until the command's stdout contains want.
func (f logsFixture) waitOut(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(f.out.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("stdout never contained %q; got %q (stderr %q)", want, f.out.String(), f.err.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDaemonLogsPrintsFile(t *testing.T) {
	f := newLogsFixture(t, "line one\nline two\n")
	if err := f.run(context.Background()); err != nil {
		t.Fatalf("daemon logs: %v", err)
	}
	if got := f.out.String(); got != "line one\nline two\n" {
		t.Fatalf("stdout = %q, want the log file verbatim", got)
	}
	if f.err.String() != "" {
		t.Fatalf("stderr = %q, want nothing for an existing log", f.err.String())
	}
	none := newLogsFixture(t, "")
	if err := none.run(context.Background()); err != nil {
		t.Fatalf("daemon logs with no log file: %v", err)
	}
	if !strings.Contains(none.err.String(), "no log file yet") || none.out.String() != "" {
		t.Fatalf("no log file: stdout %q stderr %q, want a stderr diagnostic only", none.out.String(), none.err.String())
	}
}

// TestDaemonLogsFollowStopsOnCancel: -f delivers appended lines, then the
// rotation (active file renamed away, a fresh file at the path) is followed:
// the new file's lines appear, including ones written after the switch. A
// cancelled context ends the command with no error.
func TestDaemonLogsFollowStopsOnCancel(t *testing.T) {
	f := newLogsFixture(t, "before\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.run(ctx, "-f") }()

	f.waitOut(t, "before")
	f.append(t, "appended\n")
	f.waitOut(t, "appended")

	if err := os.Rename(f.path, f.path+".1"); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	f.write(t, f.path, "after-rotation\n")
	f.waitOut(t, "after-rotation")
	f.append(t, "later-in-new-file\n")
	f.waitOut(t, "later-in-new-file")

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("daemon logs -f after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon logs -f did not stop when its context was cancelled")
	}
}

// TestDaemonLogsFollowEndsWhenFileDisappears: a deleted log ends -f instead
// of spinning on a path that never comes back.
func TestDaemonLogsFollowEndsWhenFileDisappears(t *testing.T) {
	f := newLogsFixture(t, "only\n")
	done := make(chan error, 1)
	go func() { done <- f.run(context.Background(), "-f") }()
	f.waitOut(t, "only")
	if err := os.Remove(f.path); err != nil {
		t.Fatalf("remove log: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("daemon logs -f after the file was deleted: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon logs -f kept running after the log file was deleted")
	}
}
