package client

// Purpose: the transport half of Client, asserted in the default
//   no-network unit lane by driving the REAL production dialer
//   (UnixDialer) at unix socket paths nothing serves — a connect(2) that
//   fails before any byte moves, so this file needs no "net"/"net/http"
//   import and no listening peer. What it proves is the classification
//   contract: every way the exchange can fail before a response exists
//   maps to the frozen taxonomy, never to a raw net error. The success
//   path over a real daemon-shaped socket is
//   client_integration_test.go's.
// SPORT: internal/client (ADD).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// missingSocket returns a path inside a fresh temp dir where no socket
// exists at all: the "daemon was never started" case.
func missingSocket(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "nothing-here.sock")
}

// notASocket returns a path holding a regular file, so connect(2) reaches
// an existing path that cannot serve: the "stale or wrong socket path"
// case, distinct from the missing one above.
func notASocket(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "regular-file.sock")
	if err := os.WriteFile(path, []byte("not a socket"), 0o600); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
	return path
}

func TestNew_NilDialPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("New(nil dial): expected a panic")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "nil DialFunc") {
			t.Errorf("panic value = %v, want it to name the nil DialFunc", r)
		}
	}()
	New("/tmp/unused.sock", nil, time.Second)
}

// TestDo_MissingSocket proves the "daemon not running" case surfaces
// KindUnavailable naming the socket, not a raw dial error.
func TestDo_MissingSocket(t *testing.T) {
	sock := missingSocket(t)
	c := New(sock, UnixDialer, 2*time.Second)

	err := c.Do(context.Background(), "status.get", nil, nil)
	if err == nil {
		t.Fatal("Do: expected an error dialing a socket that does not exist")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("err = %v, want KindUnavailable", err)
	}
	if !strings.Contains(err.Error(), sock) {
		t.Errorf("err = %v, want it to name the socket path %s", err, sock)
	}
}

// TestDo_PathIsNotASocket proves a path that exists but is not a socket is
// also KindUnavailable, not KindInternal: nothing was ever decoded.
func TestDo_PathIsNotASocket(t *testing.T) {
	c := New(notASocket(t), UnixDialer, 2*time.Second)

	err := c.Do(context.Background(), "status.get", nil, nil)
	if err == nil {
		t.Fatal("Do: expected an error dialing a regular file")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("err = %v, want KindUnavailable", err)
	}
}

// TestDo_CanceledContext proves a caller cancellation is reported as
// KindCanceled, distinct from the unreachable-daemon case, so a CLI can
// tell "you pressed ctrl-c" from "the daemon is down".
func TestDo_CanceledContext(t *testing.T) {
	c := New(missingSocket(t), UnixDialer, 2*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := c.Do(ctx, "status.get", nil, nil)
	if !cascade.HasKind(err, cascade.KindCanceled) {
		t.Fatalf("err = %v, want KindCanceled", err)
	}
}

// TestDo_ExpiredDeadline proves an already-expired context deadline is
// reported as KindTimeout, again distinct from unreachable. The zero
// time.Time is used as the deadline so the test needs no clock read.
func TestDo_ExpiredDeadline(t *testing.T) {
	c := New(missingSocket(t), UnixDialer, 2*time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), time.Time{})
	defer cancel()

	err := c.Do(ctx, "status.get", nil, nil)
	if !cascade.HasKind(err, cascade.KindTimeout) {
		t.Fatalf("err = %v, want KindTimeout", err)
	}
}

// TestDo_EncodeFailureNeverDials proves a params value that cannot be
// marshaled fails before the transport is touched, surfacing the encoder's
// own KindInternal rather than a misleading "daemon unreachable".
func TestDo_EncodeFailureNeverDials(t *testing.T) {
	c := New(missingSocket(t), UnixDialer, 2*time.Second)

	err := c.Do(context.Background(), "status.get", make(chan int), nil)
	if !cascade.HasKind(err, cascade.KindInternal) {
		t.Fatalf("err = %v, want KindInternal from the encoder", err)
	}
	if strings.Contains(err.Error(), "not running") {
		t.Errorf("err = %v, want an encode failure, not a transport one", err)
	}
}

// TestStatus_TransportFailurePropagates proves the typed wrapper does not
// swallow or re-tag the transport classification, and returns the zero
// response rather than a half-populated one.
func TestStatus_TransportFailurePropagates(t *testing.T) {
	c := New(missingSocket(t), UnixDialer, 2*time.Second)

	res, err := c.Status(context.Background())
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	if res.Version != "" || res.Health != "" || res.Daemon.PID != 0 {
		t.Errorf("Status returned %+v on failure, want the zero response", res)
	}
}

// timeoutError implements net.Error's shape (Error/Timeout/Temporary)
// without this file naming the "net" package, so the timeout branch of
// classifyTransportError that keys off net.Error is exercised in the
// no-network lane.
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// nonTimeoutError is the same shape reporting Timeout()=false, proving the
// branch keys off the Timeout() answer and not merely off the interface.
type nonTimeoutError struct{}

func (nonTimeoutError) Error() string   { return "connection refused" }
func (nonTimeoutError) Timeout() bool   { return false }
func (nonTimeoutError) Temporary() bool { return false }

func TestClassifyTransportError_NetTimeout(t *testing.T) {
	err := classifyTransportError(context.Background(), "status.get", "/s.sock", timeoutError{})
	if !cascade.HasKind(err, cascade.KindTimeout) {
		t.Errorf("err = %v, want KindTimeout for a net.Error reporting Timeout()", err)
	}
}

func TestClassifyTransportError_NetNonTimeout(t *testing.T) {
	err := classifyTransportError(context.Background(), "status.get", "/s.sock", nonTimeoutError{})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("err = %v, want KindUnavailable for a non-timeout net.Error", err)
	}
}

// TestClassifyTransportError_DeadlineFromErrorNotContext proves the
// deadline branch fires on the ERROR chain too, not only on a context
// that has already expired.
func TestClassifyTransportError_DeadlineFromErrorNotContext(t *testing.T) {
	err := classifyTransportError(context.Background(), "status.get", "/s.sock", context.DeadlineExceeded)
	if !cascade.HasKind(err, cascade.KindTimeout) {
		t.Errorf("err = %v, want KindTimeout", err)
	}
}
