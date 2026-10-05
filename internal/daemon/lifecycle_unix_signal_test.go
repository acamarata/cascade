//go:build !windows

package daemon

// Purpose: Run's signal contract. UpgradeSignal is the only trigger that
//   consults the UpgradeManager; with an unchanged binary the daemon logs
//   and keeps answering. SIGTERM, SIGINT and a cancelled context always
//   drain and return, even when the installed binary has been replaced.
// Constraints: no "net" import in the unit lane (Art.7.2): the probe
//   request goes over raw AF_UNIX syscalls. Signals are delivered on an
//   injected unbuffered channel, so a send returns only once Run took it.
// SPORT: internal/daemon (CHANGED, explicit upgrade hand-off).

import (
	"context"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// realBinaryStopLeg is set by the integration-tagged real-binary file, so
// TestStopAfterReplaceExits repeats its check on real binaries there.
var realBinaryStopLeg func(t *testing.T)

// TestRunCapturesStartupDigestBeforeReady guards Run's eager startup hash.
func TestRunCapturesStartupDigestBeforeReady(t *testing.T) {
	bin := writeTempBinary(t, "startup bytes")
	var resolves atomic.Int32
	pinResolver(t, func() (string, error) { resolves.Add(1); return bin, nil })
	h := newRunHarness(t)
	sigs := make(chan os.Signal)
	go func() { h.done <- Run(context.Background(), upgradeRunOptions(h, sigs, nil)) }()
	<-h.ready
	if got := resolves.Load(); got != 1 {
		t.Errorf("resolver calls before any signal = %d; want 1", got)
	}
	sendSignal(t, sigs, syscall.SIGTERM, h.done)
	waitRunReturn(t, h.done, "startup capture check")
}

// TestStopDuringHandOffExits covers every stop source while the join runs.
func TestStopDuringHandOffExits(t *testing.T) {
	for _, stop := range []string{"SIGTERM", "SIGINT", "SIGHUP", "closed", "parent"} {
		t.Run(stop, func(t *testing.T) {
			calls, _ := stubExec(t, nil)
			bin := writeTempBinary(t, "release-1 bytes")
			pinStartup(t, bin)
			_ = BuildHash()
			replaceFileAt(t, bin, "release-2 bytes")
			h := newRunHarness(t)
			sigs := make(chan os.Signal)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m, _ := newTestManager(t, nil, nil)
			m.BeforeRelaunch = func(hctx context.Context) {
				switch stop {
				case "SIGTERM":
					sigs <- syscall.SIGTERM
				case "SIGINT":
					sigs <- syscall.SIGINT
				case "SIGHUP":
					sigs <- syscall.SIGHUP
				case "closed":
					close(sigs)
				default:
					cancel()
				}
				select {
				case <-hctx.Done():
				case <-time.After(time.Second):
					t.Error("hand-off context was not cancelled")
				}
			}
			go func() { h.done <- Run(ctx, upgradeRunOptions(h, sigs, m)) }()
			<-h.ready
			sendSignal(t, sigs, UpgradeSignal, h.done)
			waitRunReturn(t, h.done, stop+" during hand-off")
			if calls.Load() != 0 || !m.Draining() {
				t.Fatalf("exec calls=%d draining=%v; want 0, true", calls.Load(), m.Draining())
			}
			if _, err := os.Stat(h.pidPath); !os.IsNotExist(err) {
				t.Fatalf("pidfile still present after hand-off stop: %v", err)
			}
		})
	}
}

// rawStatusLine sends one HTTP/1.0 request to the unix socket at path and
// returns the response's status line.
func rawStatusLine(t *testing.T, path string) string {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("socket: %v", err)
	}
	defer func() { _ = syscall.Close(fd) }()
	tv := syscall.NsecToTimeval(int64(5 * time.Second))
	if err := syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv); err != nil {
		t.Fatalf("set receive timeout: %v", err)
	}
	if err := syscall.Connect(fd, &syscall.SockaddrUnix{Name: path}); err != nil {
		t.Fatalf("connect %s: %v", path, err)
	}
	if _, err := syscall.Write(fd, []byte("GET /probe HTTP/1.0\r\n\r\n")); err != nil {
		t.Fatalf("write request: %v", err)
	}
	buf := make([]byte, 128)
	n, err := syscall.Read(fd, buf)
	if err != nil || n == 0 {
		t.Fatalf("read response: n=%d err=%v; the daemon did not answer", n, err)
	}
	line, _, _ := strings.Cut(string(buf[:n]), "\r\n")
	return line
}

// sendSignal delivers sig on the unbuffered sigs, failing if Run exited or
// never took it.
func sendSignal(t *testing.T, sigs chan<- os.Signal, sig os.Signal, done <-chan error) {
	t.Helper()
	select {
	case sigs <- sig:
	case err := <-done:
		t.Fatalf("Run returned (%v) before taking %v", err, sig)
	case <-time.After(5 * time.Second):
		t.Fatalf("Run never took %v", sig)
	}
}

// TestSIGUSR2WithoutReplaceKeepsServing: UpgradeSignal over an unchanged
// startup binary relaunches nothing and the daemon still answers a request
// sent after it; SIGTERM then drains and returns, again with no relaunch.
func TestSIGUSR2WithoutReplaceKeepsServing(t *testing.T) {
	calls, _ := stubExec(t, nil)
	pinStartup(t, writeTempBinary(t, "unchanged-binary"))
	h := newRunHarness(t)
	sigs := make(chan os.Signal)
	m, _ := newTestManager(t, nil, nil)
	go func() { h.done <- Run(context.Background(), upgradeRunOptions(h, sigs, m)) }()
	<-h.ready

	sendSignal(t, sigs, UpgradeSignal, h.done)
	// The second send completes only once the first was handled in full.
	sendSignal(t, sigs, UpgradeSignal, h.done)
	if line := rawStatusLine(t, h.socketPath); !strings.HasPrefix(line, "HTTP/1.") || !strings.Contains(line, "404") {
		t.Fatalf("request after UpgradeSignal answered %q; want the fallback server's 404", line)
	}
	if m.Draining() || calls.Load() != 0 {
		t.Fatalf("after UpgradeSignal: draining=%v exec calls=%d; want false, 0", m.Draining(), calls.Load())
	}

	sendSignal(t, sigs, syscall.SIGTERM, h.done)
	waitRunReturn(t, h.done, "SIGTERM")
	if calls.Load() != 0 {
		t.Fatalf("exec calls = %d after SIGTERM; want 0", calls.Load())
	}
}

// TestSIGUSR2SkewCheckErrorKeepsServing: when the installed binary cannot
// be read, UpgradeSignal neither drains nor relaunches (the failure is
// logged, never read as "unchanged") and the daemon keeps answering.
func TestSIGUSR2SkewCheckErrorKeepsServing(t *testing.T) {
	calls, _ := stubExec(t, nil)
	bin := writeTempBinary(t, "release-1 bytes")
	pinStartup(t, bin)
	_ = BuildHash()
	if err := os.Remove(bin); err != nil {
		t.Fatalf("remove: %v", err)
	}
	h := newRunHarness(t)
	sigs := make(chan os.Signal)
	m, _ := newTestManager(t, nil, nil)
	go func() { h.done <- Run(context.Background(), upgradeRunOptions(h, sigs, m)) }()
	<-h.ready

	sendSignal(t, sigs, UpgradeSignal, h.done)
	sendSignal(t, sigs, UpgradeSignal, h.done)
	if line := rawStatusLine(t, h.socketPath); !strings.Contains(line, "404") {
		t.Fatalf("request after a failed skew check answered %q; want 404", line)
	}
	if m.Draining() || calls.Load() != 0 {
		t.Fatalf("draining=%v exec calls=%d; want false, 0", m.Draining(), calls.Load())
	}
	sendSignal(t, sigs, syscall.SIGTERM, h.done)
	waitRunReturn(t, h.done, "SIGTERM")
}

// TestStopAfterReplaceExits: with skew present, SIGTERM, SIGINT and a
// cancelled context each drain and return without consulting the
// UpgradeManager (0 exec calls, no drain), and clean up pidfile and socket.
func TestStopAfterReplaceExits(t *testing.T) {
	for _, stop := range []string{"SIGTERM", "SIGINT", "ctx"} {
		t.Run(stop, func(t *testing.T) {
			calls, _ := stubExec(t, nil)
			bin := writeTempBinary(t, "release-1 bytes")
			pinStartup(t, bin)
			m, _ := newTestManager(t, nil, nil)
			_ = BuildHash()
			replaceFileAt(t, bin, "release-2 bytes")
			wantSkew(t, m, true)

			h := newRunHarness(t)
			sigs := make(chan os.Signal)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { h.done <- Run(ctx, upgradeRunOptions(h, sigs, m)) }()
			<-h.ready
			switch stop {
			case "SIGTERM":
				sendSignal(t, sigs, syscall.SIGTERM, h.done)
			case "SIGINT":
				sendSignal(t, sigs, syscall.SIGINT, h.done)
			default:
				cancel()
			}
			waitRunReturn(t, h.done, stop)
			if calls.Load() != 0 || m.Draining() {
				t.Fatalf("%s with skew: exec calls=%d draining=%v; want 0, false", stop, calls.Load(), m.Draining())
			}
			if _, err := os.Stat(h.pidPath); !os.IsNotExist(err) {
				t.Fatalf("pidfile still present after %s: %v", stop, err)
			}
		})
	}
	if realBinaryStopLeg != nil {
		t.Run("real-binary", realBinaryStopLeg)
	}
}
