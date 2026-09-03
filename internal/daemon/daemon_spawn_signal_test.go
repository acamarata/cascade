//go:build !windows

package daemon

// Purpose: the two production seam implementations Start/Stop's fakeable
//   interfaces stand in for everywhere else in this package: DefaultSpawn
//   (a real, short-lived detached subprocess, released not waited — this
//   ticket confirms it via the child's REAL observable side effect, its
//   log output landing in logPath, per R-14.123: never trust
//   Release/Start's success alone) and DefaultSignal (a real signal-0
//   probe against the current process, plus the genuine delivery-failure
//   path against a PID nothing holds). Also logInfo's nil-logger no-op
//   branch, the one line lifecycle_unix_start.go's own tests leave
//   uncovered. No "net" import needed.
// SPORT: internal/daemon (ADD, per T-2 sport_updates).

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestDefaultSpawn_LaunchesRealDetachedProcess spawns a real (trivial,
// fast-exiting) subprocess via DefaultSpawn and confirms it actually ran —
// via its own logged side effect, not merely a non-error return — then
// waits for its output to land using a ticker + deadline select, never a
// bare time.Sleep as the synchronization primitive (R-14.136).
func TestDefaultSpawn_LaunchesRealDetachedProcess(t *testing.T) {
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH, cannot exercise a real subprocess")
	}
	logPath := filepath.Join(t.TempDir(), "daemon.log")

	spawn := DefaultSpawn(shPath, []string{"-c", "echo spawned-ok"}, logPath)
	pid, err := spawn(context.Background())
	if err != nil {
		t.Fatalf("DefaultSpawn(...)(): %v", err)
	}
	if pid <= 0 {
		t.Fatalf("pid = %d, want a positive pid", pid)
	}

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(5 * time.Second)
	var content []byte
	for {
		select {
		case <-ticker.C:
			content, err = os.ReadFile(logPath)
			if err == nil && strings.Contains(string(content), "spawned-ok") {
				return // the detached child genuinely ran and wrote its output
			}
		case <-deadline:
			t.Fatalf("log file %q never contained the child's output; last read: %q (err=%v)", logPath, content, err)
		}
	}
}

func TestDefaultSpawn_UnopenableLogPathIsError(t *testing.T) {
	badLogPath := filepath.Join(t.TempDir(), "no-such-dir", "daemon.log")
	spawn := DefaultSpawn("sh", []string{"-c", "true"}, badLogPath)
	if _, err := spawn(context.Background()); err == nil {
		t.Fatal("DefaultSpawn with an unopenable log path succeeded, want an error")
	}
}

func TestLogInfo_NilLoggerIsNoop(t *testing.T) {
	// Explicit assertion: the contract is "does nothing, does not panic",
	// and a test whose only assertion is implicit leaves t unused and reads
	// identically to one written to move a coverage number.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("logInfo with a nil logger panicked: %v", r)
		}
	}()
	// Must not panic — the one branch lifecycle_unix_start.go's own
	// Start/Stop tests (which always inject a real or nil *slog.Logger
	// through StartOptions.Logger, never call logInfo directly with a
	// guaranteed-nil logger) leave uncovered.
	logInfo(nil, "daemon: start: already running", "pid", 123)
}

func TestLogInfo_WithLoggerEmitsExactlyOneInfoLine(t *testing.T) {
	log, records := newRecordingLogger()
	logInfo(log, "daemon: start: already running", "pid", 42)
	if len(*records) != 1 {
		t.Fatalf("records = %+v, want exactly one", *records)
	}
	r := (*records)[0]
	if r.Level != slog.LevelInfo || r.Message != "daemon: start: already running" {
		t.Errorf("record = %+v, want an INFO line with the given message", r)
	}
}

// --- DefaultSignal ---

func TestDefaultSignal_Signal0AgainstSelfSucceeds(t *testing.T) {
	// Signal 0 delivers nothing but still performs the kernel's
	// existence/permission check — sending it to the running test
	// process itself must succeed and have no observable side effect.
	if err := DefaultSignal(os.Getpid(), syscall.Signal(0)); err != nil {
		t.Errorf("DefaultSignal(self, 0) = %v, want nil", err)
	}
}

func TestDefaultSignal_DeadPIDIsError(t *testing.T) {
	pid := deadPID(t)
	if err := DefaultSignal(pid, syscall.SIGTERM); err == nil {
		t.Errorf("DefaultSignal(%d, SIGTERM) = nil, want an error (no such process)", pid)
	}
}
