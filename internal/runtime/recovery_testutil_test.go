// Purpose: shared test doubles for every recovery*_test.go file in this
//   package (fakeEventBus, fakeRegistry, fakeConn/dialerStub, and small
//   helpers) — split out under R-14.117 (Art.10.3's 300-line cap; a
//   cap-driven split joins the ticket's authorized write set
//   automatically). Confined to _test.go per Art.1: recovery.go itself
//   only ever sees the EventBus/DomainRegistry/Dialer interfaces.
// Constraints: Art.7.1 — writeSocketPlaceholder only ever writes under a
//   caller-supplied t.TempDir() path. Art.7.2 — no "net"/"net/http"
//   import here; dialerStub is the whole point of not needing one.

package runtime

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// fakeEventBus records every Publish call for assertion.
type fakeEventBus struct {
	published []fakeEvent
}

type fakeEvent struct {
	namespace, kind, source string
	payload                 []byte
}

func (b *fakeEventBus) Publish(_ context.Context, namespace, kind, source string, payload []byte) error {
	b.published = append(b.published, fakeEvent{namespace, kind, source, payload})
	return nil
}

// fakeRegistry is a DomainRegistry test double.
type fakeRegistry struct {
	locks      []OrphanedLock
	listErr    error
	releaseErr map[string]error
	released   []string
}

func (r *fakeRegistry) OrphanedLocks(context.Context) ([]OrphanedLock, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	return r.locks, nil
}

func (r *fakeRegistry) Release(_ context.Context, lockID string) error {
	if err, ok := r.releaseErr[lockID]; ok {
		return err
	}
	r.released = append(r.released, lockID)
	return nil
}

// fakeConn is a trivial io.Closer standing in for net.Conn — the fake
// Dialer's "a listener answered" return value.
type fakeConn struct{ closed bool }

func (c *fakeConn) Close() error { c.closed = true; return nil }

// dialerStub builds a Dialer (recovery.go's injected socket-probe seam)
// that never touches the real network: live simulates a listener
// answering, and any non-nil err (when live is false) simulates the
// dial failure the probe must classify — pass syscall.ECONNREFUSED for
// the confirmed-stale case, anything else for undecidable.
func dialerStub(live bool, err error) Dialer {
	return func(string, string, time.Duration) (io.Closer, error) {
		if live {
			return &fakeConn{}, nil
		}
		return nil, err
	}
}

func testLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// deadPid spawns a real short-lived child process, waits for it to exit
// and be reaped, and returns its pid — a pid the OS unambiguously reports
// ESRCH for immediately afterward. Used by every "stale" test instead of
// a fabricated pid number, matching the real crash scenario the ticket
// requires (Art.2: real counterpart, not a guessed-at pid).
func deadPid(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatalf("spawn+run short-lived child: %v", err)
	}
	return cmd.Process.Pid
}

func intToBytes(n int) string { return strconv.Itoa(n) }

// writeSocketPlaceholder creates a plain file at path standing in for a
// leftover socket inode. Scan/probeSocket only ever os.Stat's the path
// for existence and (via the injected Dialer) attempts a connection —
// neither cares whether the bytes at path are a real bound socket, so a
// plain file is sufficient and keeps this package's unit lane free of
// real net I/O (see recovery_test.go's file-level Art.7.2 note).
func writeSocketPlaceholder(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("seed socket placeholder: %v", err)
	}
}
