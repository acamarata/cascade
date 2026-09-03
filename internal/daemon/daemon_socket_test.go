//go:build !windows

package daemon

// Purpose: lifecycle_unix.go's socket-level coverage: isAddrInUse's error
//   matching (pure syscall.Errno comparison, no I/O at all), drain's two
//   log lines (a pure function over RunOptions/*int64, no socket needed),
//   and listenSocket's success path AND its stale-socket-file retry path
//   (isAddrInUse+dialable both exercised as PRODUCTION code listenSocket
//   calls internally — this file itself never imports "net", only
//   os/syscall, so Art.7.2's no-network-unit-lane gate does not apply).
//   Run's own full-lifecycle tests live in the sibling daemon_run_test.go,
//   split out purely for Art.10.3's 300-line file cap.
// SPORT: internal/daemon (ADD, per T-2 sport_updates).

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// --- isAddrInUse ---

func TestIsAddrInUse(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"exact EADDRINUSE", syscall.EADDRINUSE, true},
		{"wrapped EADDRINUSE", fmt.Errorf("bind: %w", syscall.EADDRINUSE), true},
		{"unrelated error", errors.New("boom"), false},
		{"different errno", syscall.ENOENT, false},
		{"nil error", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isAddrInUse(c.err); got != c.want {
				t.Errorf("isAddrInUse(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

// --- drain ---

func TestDrain_LogsStartAndEndWithConnectionCount(t *testing.T) {
	log, records := newRecordingLogger()
	var active int64 = 3
	drain(RunOptions{Logger: log, Settings: Settings{ShutdownGrace: 2 * time.Second}}, &active)

	if len(*records) != 2 {
		t.Fatalf("records = %+v, want exactly 2 (drain start, drain end)", *records)
	}
	if (*records)[0].Message != "daemon: drain start" || (*records)[1].Message != "daemon: drain end" {
		t.Errorf("messages = %q, %q", (*records)[0].Message, (*records)[1].Message)
	}
	for _, r := range *records {
		var sawConnections bool
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "connections" && a.Value.Int64() == 3 {
				sawConnections = true
			}
			return true
		})
		if !sawConnections {
			t.Errorf("record %q missing connections=3 attribute", r.Message)
		}
	}
}

func TestDrain_NilLoggerIsNoop(t *testing.T) {
	// Explicit assertion: the contract is "does nothing, does not panic",
	// and a test whose only assertion is implicit leaves t unused and reads
	// identically to one written to move a coverage number.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("drain with a nil logger panicked: %v", r)
		}
	}()
	var active int64
	// Must not panic when opts.Logger is nil — the early-return branch.
	drain(RunOptions{}, &active)
}

// --- listenSocket ---

func TestListenSocket_Success_BindsWith0600Perms(t *testing.T) {
	dir := shortTempDir(t)
	path := filepath.Join(dir, "d.sock")

	ln, err := listenSocket(path)
	if err != nil {
		t.Fatalf("listenSocket: %v", err)
	}
	defer func() { _ = ln.Close() }()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket perms = %v, want 0600", perm)
	}
}

// TestListenSocket_StaleSocketFile_RemovedAndRebound exercises listenSocket's
// EADDRINUSE-but-not-dialable retry branch: a leftover file sitting at the
// socket path (as a crashed prior daemon would leave, since nothing is
// listening on it) must be removed and the bind retried, rather than
// failing outright. This drives isAddrInUse (true, matching the EADDRINUSE
// net.Listen reports for an existing path) and dialable (false, since the
// stale file is not actually accepting connections) as real PRODUCTION
// code paths — this test file itself imports no "net" package.
func TestListenSocket_StaleSocketFile_RemovedAndRebound(t *testing.T) {
	dir := shortTempDir(t)
	path := filepath.Join(dir, "d.sock")
	if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}

	ln, err := listenSocket(path)
	if err != nil {
		t.Fatalf("listenSocket against a stale socket file: %v", err)
	}
	defer func() { _ = ln.Close() }()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket perms after rebind = %v, want 0600", perm)
	}
}

func TestListenSocket_UnusableParentDirIsError(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := listenSocket(filepath.Join(blocker, "d.sock"))
	if err == nil {
		t.Fatal("listenSocket under a file (not a dir) succeeded, want an error")
	}
}
