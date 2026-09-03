//go:build !windows

package daemon

// Purpose: UpgradeManager, the daemon upgrade-in-place engine (R-14.12,
// R-14.7): skew-detect (compare the running daemon's embedded build hash
// against an on-disk binary's SHA-256), drain (stop accepting new
// connections, wait bounded for in-flight work, force-close stragglers),
// and exec-relaunch (syscall.Exec, same PID, no stop/start round trip).
// Also carries the W1 allowed-fail resume leg: a WAL-position checkpoint
// written to the db metadata domain before a drain, read back best-effort
// on the next startup. Windows has no daemon at all (tier-2), so this
// whole file is unix-only.
//
// Inputs: an on-disk binary path (CheckSkew, Relaunch); a net.Listener and
// *ConnTracker (Drain); a WAL-position string (WriteResumeCursor); every
// collaborator (Clock, Sleep, Store, Events, Logger) injected, never
// reached for directly (Art.7.1) - Store and Events may be nil, in which
// case their features degrade to documented no-ops, never errors.
//
// Outputs: CheckSkew's bool+typed-error pair; Drain always returns nil (a
// forced close at the grace boundary is its normal outcome, not a
// caller-visible failure); Relaunch's typed error when syscall.Exec itself
// fails (success never returns, since the process image is gone).
//
// Constraints: no bare time.Now/After/Tick/Sleep - Clock.Now() and the
// injected Sleep func are the only time sources. syscall.Exec is called
// through the execFunc var so TestExecRelaunch can stub it.
//
// SPORT: internal/daemon (ADD, DaemonUpgradeManager entity, per T-5
// sport_updates).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// buildHash is overwritten at build time via goreleaser ldflags:
// -X github.com/acamarata/cascade/internal/daemon.buildHash=<git-sha>.
// "dev" is the unreleased-build value; it never matches a real on-disk
// binary's hash, so an unreleased build always reports skew against
// itself, which is expected and harmless since Relaunch only runs when a
// caller acts on CheckSkew's result.
var buildHash = "dev"

// EventKindShutdownRequested is published to eventNamespace the moment
// Drain begins closing its listener, before it waits for in-flight work.
// EventKind is open (unlike pkg/cascade.Kind); this package mints its own.
const EventKindShutdownRequested events.EventKind = "daemon.shutdown_requested"

const (
	eventNamespace = "daemon"
	eventSource    = "daemon.upgrade"

	// upgradeCursorDomain/Key locate the resume-leg's WAL-position
	// checkpoint in the db metadata domain (this ticket's contract).
	upgradeCursorDomain = "runtime"
	upgradeCursorKey    = "upgrade_cursor"

	drainPollInterval = 20 * time.Millisecond
)

// execFunc is the syscall.Exec seam TestExecRelaunch stubs; production
// leaves it as syscall.Exec.
var execFunc = syscall.Exec

// BuildHash returns the build-time embedded hash. It is a package
// function (every UpgradeManager in one process shares the same answer)
// and a method below, matching the contract's "UpgradeManager.BuildHash()"
// call shape for holders of an instance.
func BuildHash() string { return buildHash }

// ErrDraining is the typed error a connection accepted after Drain has
// begun receives, rather than being silently accepted and dropped.
var ErrDraining = cascade.New(cascade.KindUnavailable, "daemon: draining for upgrade, connection refused")

// UpgradeManager implements the skew-detect -> drain -> exec-relaunch
// engine. Every field is a dependency-injection seam (Art.7.1); construct
// with NewUpgradeManager, or set fields directly in tests.
type UpgradeManager struct {
	Clock  runtime.Clock
	Sleep  func(time.Duration)
	Store  provider.Store // optional: nil disables the resume-leg cursor
	Events *events.Bus    // optional: nil disables ShutdownRequested
	Logger *slog.Logger

	draining atomic.Bool
}

// NewUpgradeManager builds a ready UpgradeManager. store and bus may be
// nil; their features then degrade to documented no-ops, never errors.
func NewUpgradeManager(clock runtime.Clock, sleep func(time.Duration), store provider.Store, bus *events.Bus, logger *slog.Logger) *UpgradeManager {
	return &UpgradeManager{Clock: clock, Sleep: sleep, Store: store, Events: bus, Logger: logger}
}

// BuildHash returns the running binary's embedded build hash.
func (m *UpgradeManager) BuildHash() string { return BuildHash() }

// CheckSkew reports whether the binary on disk at binaryPath differs from
// the running process's embedded BuildHash, streaming the file through
// SHA-256 rather than loading it whole. Any I/O failure (missing file,
// permission denied, a directory instead of a file) is a typed
// cascade.KindUnavailable error, never swallowed into a false "no skew".
func (m *UpgradeManager) CheckSkew(binaryPath string) (bool, error) {
	sum, err := hashFile(binaryPath)
	if err != nil {
		return false, cascade.Wrapf(cascade.KindUnavailable, err, "daemon: upgrade: hash %s", binaryPath)
	}
	return sum != m.BuildHash(), nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Draining reports whether Drain has been called. The accept loop
// consults this to refuse, with ErrDraining, a connection that arrives
// once a drain is under way rather than accepting and silently dropping.
func (m *UpgradeManager) Draining() bool { return m.draining.Load() }

// Drain stops accepting new connections and waits (bounded by grace) for
// tracker's in-flight work to finish, force-closing whatever remains once
// grace elapses. It always returns nil: a forced close at the grace
// boundary is Drain's normal, logged outcome, not a failure the caller
// branches on. ln is closed unconditionally, even when tracker is nil (a
// caller with no in-flight work to track can still stop accepting).
func (m *UpgradeManager) Drain(ctx context.Context, ln io.Closer, tracker *ConnTracker, grace time.Duration) error {
	m.draining.Store(true)
	if ln != nil {
		_ = ln.Close()
	}
	m.publishShutdownRequested(ctx)
	if tracker == nil {
		return nil
	}
	if m.waitGrace(tracker.Done(), grace) {
		return nil
	}
	n := tracker.CloseAll()
	m.logWarn("daemon: upgrade: drain grace elapsed, force-closed connections", "count", n)
	return nil
}

func (m *UpgradeManager) publishShutdownRequested(ctx context.Context) {
	if m.Events == nil {
		return
	}
	if _, err := m.Events.Publish(ctx, eventNamespace, EventKindShutdownRequested, eventSource, nil); err != nil {
		m.logWarn("daemon: upgrade: publish ShutdownRequested failed", "error", err.Error())
	}
}

// waitGrace polls done against a Clock-derived deadline, sleeping
// drainPollInterval between checks via the injected Sleep func - the same
// attempt-counted-wait idiom lifecycle_unix_stop.go's pollGone already
// uses, deterministic under test since Sleep is faked, never a bare
// time.Sleep.
func (m *UpgradeManager) waitGrace(done <-chan struct{}, grace time.Duration) bool {
	deadline := m.now().Add(grace)
	for {
		select {
		case <-done:
			return true
		default:
		}
		if !m.now().Before(deadline) {
			select {
			case <-done:
				return true
			default:
				return false
			}
		}
		m.sleep(drainPollInterval)
	}
}

func (m *UpgradeManager) now() time.Time {
	if m.Clock != nil {
		return m.Clock.Now()
	}
	return runtime.NewSystemClock().Now()
}

func (m *UpgradeManager) sleep(d time.Duration) {
	if m.Sleep != nil {
		m.Sleep(d)
	}
}

func (m *UpgradeManager) logWarn(msg string, kv ...any) {
	if m.Logger != nil {
		m.Logger.Warn(msg, kv...)
	}
}

func (m *UpgradeManager) logInfo(msg string, kv ...any) {
	if m.Logger != nil {
		m.Logger.Info(msg, kv...)
	}
}

// Relaunch replaces the current process image in place with binaryPath,
// args and env unchanged. On success this never returns to its caller: the
// process IS the new binary from this call onward, same PID, no deferred
// cleanup runs. On failure it returns a typed cascade.KindUnavailable
// error and the CALLER is still running - Relaunch closes and removes
// nothing itself, so the caller decides what "still alive but failed to
// relaunch" means. AttemptUpgrade's caller (lifecycle_unix.go's Run) falls
// through to its normal drain-and-exit path on this error, which is what
// makes the failure non-bricking: Run's own deferred cleanup still removes
// the socket and pidfile, leaving a state `cascade daemon start` can pick
// up cleanly.
func (m *UpgradeManager) Relaunch(binaryPath string, args, env []string) error {
	if err := execFunc(binaryPath, args, env); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "daemon: upgrade: exec %s", binaryPath)
	}
	return nil
}

// AttemptUpgrade is the single entry point a termination handler calls: it
// resolves binaryPath's skew against BuildHash, and when skewed, drains ln
// (tracked by tracker, bounded by grace) and exec-relaunches in place with
// args/env unchanged. relaunched is true only when Relaunch was attempted
// and returned (a stubbed execFunc in tests, since a real successful exec
// never returns at all). err is CheckSkew's or Relaunch's error, whichever
// fired; a nil err with relaunched=false means "no skew, logged no-op" -
// the caller proceeds with its normal shutdown.
func (m *UpgradeManager) AttemptUpgrade(ctx context.Context, binaryPath string, ln io.Closer, tracker *ConnTracker, grace time.Duration, args, env []string) (relaunched bool, err error) {
	skew, err := m.CheckSkew(binaryPath)
	if err != nil {
		return false, err
	}
	if !skew {
		m.logInfo("daemon: upgrade: binary hashes match, no-op")
		return false, nil
	}
	m.logInfo("daemon: upgrade: skew detected, draining", "binary", binaryPath)
	_ = m.Drain(ctx, ln, tracker, grace)
	if relErr := m.Relaunch(binaryPath, args, env); relErr != nil {
		m.logWarn("daemon: upgrade: relaunch failed, falling back to normal shutdown", "error", relErr.Error())
		return false, relErr
	}
	return true, nil
}

// WriteResumeCursor persists pos (the current WAL position, an opaque
// caller-supplied string) to the db metadata domain ahead of a drain, so
// the next startup can log it. A write failure is logged, never fatal
// (R-14.12's allowed-fail resume leg) and never returned: an upgrade must
// never abort because a best-effort checkpoint could not be written.
func (m *UpgradeManager) WriteResumeCursor(ctx context.Context, pos string) {
	if m.Store == nil {
		return
	}
	if err := m.Store.Put(ctx, upgradeCursorDomain, upgradeCursorKey, []byte(pos)); err != nil {
		m.logWarn("daemon: upgrade: write resume cursor failed (non-fatal)", "error", err.Error())
	}
}

// ReadResumeCursor reads back WriteResumeCursor's checkpoint, if any.
// found is false (err nil) both when Store is nil and when no cursor was
// ever written - the ordinary "starting clean" case, never an error. A
// present-but-unreadable cursor still degrades to found=false
// (allowed-fail); err is purely for the caller to log.
func (m *UpgradeManager) ReadResumeCursor(ctx context.Context) (pos string, found bool, err error) {
	if m.Store == nil {
		return "", false, nil
	}
	val, getErr := m.Store.Get(ctx, upgradeCursorDomain, upgradeCursorKey)
	if getErr != nil {
		if cascade.HasKind(getErr, cascade.KindNotFound) {
			return "", false, nil
		}
		return "", false, getErr
	}
	return string(val), true, nil
}
