package runtime

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Purpose: tests for daemon_logs.go's DaemonLogsHandler — a plain read to
//   EOF, the "no log file yet" diagnostic path, -f follow-mode delivery
//   of appended lines, and the "file disappears mid-follow" diagnostic
//   exit. Art.7.1: every log path here is rooted at t.TempDir().
// SPORT: runtime/logger (ADD, per T-2 sport_updates).

// syncBuffer is a mutex-guarded bytes.Buffer so the follow-mode tests
// (one goroutine writing via DaemonLogsHandler, the main goroutine
// reading to poll for delivery) stay -race clean.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestDaemonLogsHandler_ReadsToEOFWithoutFollow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cascade.log")
	if err := writeFile(t, path, "line one\nline two\n"); err != nil {
		t.Fatalf("seed log file: %v", err)
	}

	var out, diag bytes.Buffer
	err := DaemonLogsHandler(context.Background(), DaemonLogsOptions{Path: path, Out: &out, Diag: &diag})
	if err != nil {
		t.Fatalf("DaemonLogsHandler: %v", err)
	}
	if out.String() != "line one\nline two\n" {
		t.Errorf("Out = %q", out.String())
	}
	if diag.Len() != 0 {
		t.Errorf("Diag = %q, want empty (no live daemon required)", diag.String())
	}
}

func TestDaemonLogsHandler_MissingFileEmitsDiagnosticNoError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.log")
	var out, diag bytes.Buffer
	err := DaemonLogsHandler(context.Background(), DaemonLogsOptions{Path: path, Out: &out, Diag: &diag})
	if err != nil {
		t.Fatalf("DaemonLogsHandler: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("Out = %q, want empty", out.String())
	}
	if diag.Len() == 0 {
		t.Error("Diag empty, want a diagnostic naming the missing file")
	}
}

func TestDaemonLogsHandler_FollowDeliversAppendedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cascade.log")
	if err := writeFile(t, path, "initial\n"); err != nil {
		t.Fatalf("seed log file: %v", err)
	}

	out := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- DaemonLogsHandler(ctx, DaemonLogsOptions{
			Path:         path,
			Follow:       true,
			Out:          out,
			Diag:         &syncBuffer{},
			PollInterval: 5 * time.Millisecond,
		})
	}()

	if !waitForContains(t, out, "initial", 2*time.Second) {
		t.Fatalf("initial content not delivered: %q", out.String())
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	if _, err := f.WriteString("appended-line\n"); err != nil {
		t.Fatalf("append: %v", err)
	}
	_ = f.Close()

	if !waitForContains(t, out, "appended-line", 2*time.Second) {
		t.Fatalf("follow mode did not deliver appended line: %q", out.String())
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("DaemonLogsHandler follow: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("DaemonLogsHandler did not return after ctx cancellation")
	}
}

func TestDaemonLogsHandler_FollowFileDisappearsExitsCleanly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cascade.log")
	if err := writeFile(t, path, "line\n"); err != nil {
		t.Fatalf("seed log file: %v", err)
	}

	out := &syncBuffer{}
	diag := &syncBuffer{}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- DaemonLogsHandler(ctx, DaemonLogsOptions{
			Path:         path,
			Follow:       true,
			Out:          out,
			Diag:         diag,
			PollInterval: 5 * time.Millisecond,
		})
	}()

	if !waitForContains(t, out, "line", 2*time.Second) {
		t.Fatalf("initial content not delivered: %q", out.String())
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove log file: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("DaemonLogsHandler after file removal: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("DaemonLogsHandler did not exit after log file disappeared")
	}
	if !strings.Contains(diag.String(), "disappeared") {
		t.Errorf("Diag = %q, want a disappearance diagnostic", diag.String())
	}
}

// TestDaemonLogsHandler_FollowRotationExitsCleanly proves BLOCKING FIX 1
// (CR on P1-E03-W1-S04-T2): the prior followLoop compared
// os.Stat(opts.Path).Size() against the reader's offset while still
// reading from the pre-rotation *os.File. A real rotation (rotation.go's
// rotateLocked) renames the active file away and opens a fresh, SMALLER
// file at the same path, so that comparison was permanently
// size-of-new-file <= offset-into-old-file and follow mode silently
// stopped delivering forever, with no error and no diagnostic. This
// simulates exactly that sequence (rename active-away, create a fresh
// file at the same path) without depending on rotation.go, and asserts
// the documented behaviour (docs/cli-reference/daemon.md: rotated out
// from under the reader -> diagnostic to Diag, clean exit) instead.
func TestDaemonLogsHandler_FollowRotationExitsCleanly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cascade.log")
	if err := writeFile(t, path, "before-rotation\n"); err != nil {
		t.Fatalf("seed log file: %v", err)
	}

	out := &syncBuffer{}
	diag := &syncBuffer{}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- DaemonLogsHandler(ctx, DaemonLogsOptions{
			Path:         path,
			Follow:       true,
			Out:          out,
			Diag:         diag,
			PollInterval: 5 * time.Millisecond,
		})
	}()

	if !waitForContains(t, out, "before-rotation", 2*time.Second) {
		t.Fatalf("initial content not delivered: %q", out.String())
	}

	// Simulate rotateLocked's two file-system effects: the active file
	// moves away (to path.1, as a real rotation renames it) and a fresh,
	// smaller file appears at the original path.
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatalf("simulate rotation rename: %v", err)
	}
	if err := writeFile(t, path, "after-rotation\n"); err != nil {
		t.Fatalf("simulate post-rotation file: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("DaemonLogsHandler after rotation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("DaemonLogsHandler did not exit after rotation (the FIX 1 regression: it would hang here forever, silently, pre-fix)")
	}
	if !strings.Contains(diag.String(), "rotated") {
		t.Errorf("Diag = %q, want a rotation diagnostic", diag.String())
	}
	if strings.Contains(out.String(), "after-rotation") {
		t.Errorf("Out = %q, must not deliver content from the new post-rotation file at the same path", out.String())
	}
}

// waitForContains polls buf for want, up to timeout, to avoid a fixed
// sleep racing the follow-mode goroutine's poll cadence.
func waitForContains(t *testing.T, buf *syncBuffer, want string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), want) {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return strings.Contains(buf.String(), want)
}
