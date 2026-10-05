//go:build !windows

package daemon

// Purpose: the goroutine supervisor's contract (subsystems_goroutines.go):
//   a panicking supervised goroutine is recovered into a failed subsystem
//   while the daemon keeps serving, a clean return is Skipped("stopped"), an
//   error return is Failed, and waitContext bounds the join.
// Constraints: synchronization by channels and joins only (no sleeps); the
//   daemon log is captured through a lock-guarded buffer because supervised
//   goroutines log concurrently.
// SPORT: internal/daemon (CHANGED, supervised goroutines).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
)

// lockedBuffer is an io.Writer safe for the concurrent log writes the
// supervised goroutines make.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// lines returns every JSON log line written so far, decoded.
func (b *lockedBuffer) lines(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(b.buf.String()), "\n") {
		if raw == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			t.Fatalf("decode log line %q: %v", raw, err)
		}
		out = append(out, rec)
	}
	return out
}

// startTestRun serves Run in the background until its injected signal
// channel fires, and returns that channel plus Run's result channel once
// the socket is listening.
func startTestRun(t *testing.T, m *Manifest, logger *slog.Logger) (chan<- os.Signal, <-chan error) {
	t.Helper()
	dir := shortTempDir(t)
	sigs := make(chan os.Signal, 1)
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), RunOptions{
			Settings: Settings{SocketPath: filepath.Join(dir, "d.sock")},
			PIDPath:  filepath.Join(dir, "d.pid"), Logger: logger, Clock: runtime.NewSystemClock(),
			Manifest: m, Signals: sigs, Ready: ready,
		})
	}()
	guard := time.NewTimer(30 * time.Second)
	defer guard.Stop()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("Run returned before it was ready: %v", err)
	case <-guard.C:
		t.Fatal("Run never became ready")
	}
	return sigs, done
}

// statusSubsystems dispatches status.get in process and returns its
// subsystem rows by name.
func statusSubsystems(t *testing.T, registry *rpc.Registry) map[string]SubsystemStatus {
	t.Helper()
	res, errObj := registry.Dispatch(context.Background(), &rpc.Request{Method: StatusMethod})
	if errObj != nil {
		t.Fatalf("status.get: %+v", errObj)
	}
	out := map[string]SubsystemStatus{}
	for _, s := range res.(StatusResponse).Subsystems {
		out[s.Name] = s
	}
	return out
}

// TestSupervisedPanicIsIsolated: a panicking supervised goroutine leaves the
// daemon serving, status.get reports it failed with a "panic:" detail, the
// daemon log holds exactly one ERROR line for it and that line carries the
// stack; a goroutine that returns nil is reported Skipped("stopped").
func TestSupervisedPanicIsIsolated(t *testing.T) {
	logs := &lockedBuffer{}
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	clock := runtime.NewSystemClock()
	m := NewManifest(logger, clock)
	registry := rpc.NewRegistry()
	registry.Register(StatusMethod, NewStatusProvider(clock, clock.Now(), "", nil, m).Handler())
	sigs, runDone := startTestRun(t, m, logger)

	ctx, cancel := context.WithCancel(context.Background())
	m.GoSupervised(ctx, "test.panics", "about to panic", func(context.Context) error {
		panic("supervised boom")
	})
	m.GoSupervised(ctx, "test.clean", "waiting for cancel", func(ctx context.Context) error {
		<-ctx.Done()
		return nil
	})
	cancel()
	m.Wait()

	select {
	case err := <-runDone:
		t.Fatalf("Run returned (%v) after a supervised panic; the daemon must keep serving", err)
	default:
	}
	rows := statusSubsystems(t, registry)
	if got := rows["test.panics"]; got.State != SubsystemError || !strings.HasPrefix(got.Detail, "panic:") {
		t.Fatalf("test.panics = %+v, want state error with a detail starting \"panic:\"", got)
	}
	if got := rows["test.clean"]; got.State != SubsystemSkipped || got.Detail != "stopped" {
		t.Fatalf("test.clean = %+v, want skipped/stopped", got)
	}
	if got := rows[ipcSocketSubsystem]; got.State != SubsystemRunning {
		t.Fatalf("%s = %+v, want still running after the panic", ipcSocketSubsystem, got)
	}
	sigs <- syscall.SIGTERM
	if err := <-runDone; err != nil {
		t.Fatalf("Run: %v", err)
	}
	assertOnePanicLine(t, logs.lines(t))
}

// assertOnePanicLine checks the daemon log holds exactly one ERROR line for
// the panicking subsystem and that it carries the goroutine stack.
func assertOnePanicLine(t *testing.T, lines []map[string]any) {
	t.Helper()
	var errLines []map[string]any
	for _, rec := range lines {
		if rec["level"] == "ERROR" && rec["subsystem"] == "test.panics" {
			errLines = append(errLines, rec)
		}
	}
	if len(errLines) != 1 {
		t.Fatalf("daemon log holds %d ERROR lines for test.panics, want exactly 1: %v", len(errLines), errLines)
	}
	stack, _ := errLines[0]["stack"].(string)
	if !strings.Contains(stack, "goroutine") || !strings.Contains(stack, "TestSupervisedPanicIsIsolated") {
		t.Fatalf("the panic line's stack does not name the panicking frame: %q", stack)
	}
}

// TestGoSupervisedErrorReturnFails: an error return is Failed with the
// error's text, not Skipped.
func TestGoSupervisedErrorReturnFails(t *testing.T) {
	m := NewManifest(nil, nil)
	m.GoSupervised(context.Background(), "test.errs", "running", func(context.Context) error {
		return errors.New("consumer gave up")
	})
	m.Wait()
	snap := m.Snapshot()
	if len(snap) != 1 || snap[0].State != SubsystemError || snap[0].Detail != "consumer gave up" {
		t.Fatalf("snapshot = %+v, want one failed row carrying the error text", snap)
	}
}

// TestWaitContextBoundsTheJoin: waitContext reports false while a supervised
// goroutine is still running and its bound has ended, and true once it has
// returned.
func TestWaitContextBoundsTheJoin(t *testing.T) {
	m := NewManifest(nil, nil)
	release := make(chan struct{})
	m.GoSupervised(context.Background(), "test.stuck", "running", func(context.Context) error {
		<-release
		return nil
	})
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	if m.waitContext(expired) {
		t.Fatal("waitContext reported joined while a supervised goroutine was still blocked")
	}
	close(release)
	if !m.waitContext(context.Background()) {
		t.Fatal("waitContext with a live bound did not report the join")
	}
}
