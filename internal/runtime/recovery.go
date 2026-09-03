// Purpose: the crash-safety recovery scanner's public seam — RecoveryEvent,
//   the DomainRegistry/Dialer/EventBus interfaces, RecoveryOptions, and the
//   Scan entrypoint itself. The scan sequence's step implementations
//   (probeSocket, scanPidfile, scanOrphanedLocks, publishRecoveryEvent)
//   live in recovery_scan.go — split under R-14.117 (Art.10.3's 300-line
//   cap; a cap-driven split joins the ticket's authorized write set
//   automatically, same footing as metrics_emitter.go's split from
//   metrics.go), one package, one ticket, moved code only.
//
// LOCKS-CONSIDERED — three locking mechanisms already exist in this repo;
// this scanner's relationship to each, stated explicitly per the ticket
// brief's requirement:
//
//  1. providers/sqlite's sidecar flock (flock_darwin.go / flock_linux.go /
//     flock_windows.go, probed non-destructively by
//     providers/sqlite.ProbeExclusiveLock). NOT scanned here, deliberately:
//     an OS-level flock is released by the kernel the instant its holding
//     process exits for ANY reason, including SIGKILL — there is no
//     "stale flock" state to clean up; a held flock always means a
//     currently-live holder. providers/** is also outside this ticket's
//     files_scope.
//  2. internal/events/scheduler's lease-based advisory lock (lock.go): a
//     Store-persisted record with an expiry timestamp, not a pid. It
//     self-heals purely by TTL expiry (see scheduler's Acquire: an
//     expired lease is simply re-acquirable) and needs no kill(0)-based
//     cleanup. It is also structurally unreachable from here: R-14.150
//     fixes the import edge as internal/events -> internal/runtime only,
//     never the reverse, so internal/runtime cannot import
//     internal/events/scheduler without an import cycle.
//  3. internal/storage/health.go's flock probe: the same OS-level
//     mechanism as (1), read-only, and outside files_scope.
//
// What IS scanned: the pidfile and socket file (both plain filesystem
// entries the OS does NOT clean up on process death — a dead process
// leaves its pidfile and its unix-socket inode exactly where they were),
// per HOW steps 1-3, plus a best-effort DomainRegistry seam for step 4 —
// see DomainRegistry's doc comment for why that seam is real but
// deliberately unwired to any concrete implementation in this ticket
// (CONTRACT-DEVIATIONS, journal).
//
// Inputs: RecoveryOptions — pidfile/socket paths, an injected Clock, an
//   injected *slog.Logger, an injected EventBus (may be nil: recovery
//   proceeds, simply never publishes), an injected DomainRegistry (may be
//   nil: step 4 is skipped, honestly, not faked), and an injected Dialer
//   (may be nil: defaults to a real net.DialTimeout — see Dialer's doc
//   comment for why this seam exists at all).
// Outputs: a *RecoveryEvent (nil on a clean scan — "no event = clean
//   startup" is the contract, not an omission) or a cascade.KindConflict
//   error when a live daemon answers the socket probe (abort immediately,
//   no cleanup, no event).
// Constraints: no bare time.Now — RecoveredAt is always Clock.Now()
//   (02-TARGET-STRUCTURE §v1.1). Idempotent: a second Scan call against
//   an already-clean state is a no-op returning (nil, nil). Every
//   external seam (Clock, EventBus, DomainRegistry, Dialer) is an
//   injected interface; concrete test doubles are confined to _test.go
//   (Art.1).
// SPORT: runtime/recovery (ADD, per P1-E03-W1-S05-T3 sport_updates
//   placeholder).

package runtime

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	goruntime "runtime"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// RecoveryEvent is the outcome of one Scan call that found (and,
// best-effort, repaired) at least one stale artifact. Per the contract's
// "skip emit if no stale state was found (no-event = clean startup)",
// Scan never constructs a zero-value RecoveryEvent — it returns a nil
// *RecoveryEvent instead.
type RecoveryEvent struct {
	StalePID    bool      `json:"stale_pid"`
	StaleSocket bool      `json:"stale_socket"`
	StaleLocks  []string  `json:"stale_locks"`
	RecoveredAt time.Time `json:"recovered_at"`
}

// OrphanedLock is one advisory-lock record DomainRegistry.OrphanedLocks
// reports: a lock whose stored owner may no longer be a live process.
// Scan itself performs the kill(0) liveness check (via ProcessAlive) —
// DomainRegistry only reports candidates, it does not decide staleness,
// keeping the "never delete on ambiguity" policy in exactly one place.
type OrphanedLock struct {
	// LockID identifies the lock for Release.
	LockID string
	// OwnerPID is the pid the lock record was stamped with at
	// acquisition time.
	OwnerPID int
}

// DomainRegistry is the narrow, structurally-typed seam step 4
// ("stale advisory-lock release") is written against. It deliberately
// does NOT import providers/sqlite's actual goroutine-keyed domain
// registry (from B/S-02.T2) — that registry is in-process state
// (sync.Mutex-guarded map, keyed by goroutine, holding no pid at all)
// that dies with the process itself; there is nothing for a post-crash
// scanner to find there, because a crashed process's in-memory map is
// simply gone, not "stale". It is also outside this ticket's
// files_scope (providers/** is denied). See CONTRACT-DEVIATIONS in the
// ticket journal for the full reasoning: this interface is the honest
// seam a future PID-keyed, Store-persisted registry would satisfy,
// following the same decoupled-interface pattern R-14.150 ratified for
// EventBus above (metrics_emitter.go) — but bootstrap.go wires it to nil
// in production today, which Scan treats as "step 4 skipped", never as
// a silent fake pass.
type DomainRegistry interface {
	// OrphanedLocks returns every advisory-lock record currently on
	// file, for Scan to liveness-check. It is NOT expected to have
	// already filtered by liveness — Scan does that.
	OrphanedLocks(ctx context.Context) ([]OrphanedLock, error)
	// Release releases the lock identified by lockID. Called only after
	// Scan has independently confirmed (via ProcessAlive) that the
	// lock's owner pid is ProcessLivenessDead.
	Release(ctx context.Context, lockID string) error
}

// Dialer abstracts the socket-probe connection attempt. Production always
// uses defaultDialer (a thin wrapper over the real net.DialTimeout); this
// exists as an injectable seam so recovery_test.go — the DEFAULT unit
// lane every CI job runs without `-tags integration` — never has to
// import "net" itself to exercise Scan's live/stale/undecidable branches.
// internal/build's Art.7.2 no-network-unit-lane gate statically forbids a
// "net" import in any untagged _test.go file (real socket I/O is
// confined to the `integration`-tagged lane); a fake Dialer returning
// io.Closer (net.Conn already satisfies it) lets the unit tests drive
// probeSocket's real branching logic without ever touching the real
// network stack. recovery_integration_test.go separately proves the
// production defaultDialer against a REAL unix socket end-to-end
// (Art.2's real-counterpart requirement), behind the integration tag.
type Dialer func(network, address string, timeout time.Duration) (io.Closer, error)

// defaultDialer is the production Dialer: a real net.DialTimeout call.
// net.Conn already implements io.Closer, so no adapter is needed beyond
// this signature cast.
func defaultDialer(network, address string, timeout time.Duration) (io.Closer, error) {
	return net.DialTimeout(network, address, timeout)
}

// RecoveryOptions carries every external input Scan needs. All fields
// except PidfilePath/SocketPath are optional; a nil EventBus or
// DomainRegistry means "skip that step", not "use a default".
type RecoveryOptions struct {
	// PidfilePath is the daemon pidfile location (Root()/daemon.pid by
	// convention — see docs/daemon.md).
	PidfilePath string
	// SocketPath is the daemon IPC socket location (normally
	// PathProvider.SocketPath()).
	SocketPath string
	// Clock supplies RecoveredAt. A nil value falls back to
	// NewSystemClock(), matching bootstrap.go's own convention for this
	// field.
	Clock Clock
	// Log receives every WARN this scanner emits. A nil Log falls back
	// to slog.Default() rather than silently dropping the WARNs
	// Art.1/the contract require.
	Log *slog.Logger
	// Bus, if non-nil, receives the RecoveryEvent (only when one is
	// produced). A nil Bus is a valid, honest configuration — no
	// composition-root caller is required to wire one for Scan to do
	// its cleanup work.
	Bus EventBus
	// Registry, if non-nil, is consulted for step 4. See DomainRegistry's
	// doc comment for why production wiring (bootstrap.go) leaves this
	// nil today.
	Registry DomainRegistry
	// Dial, if non-nil, replaces defaultDialer for the socket probe. Only
	// tests should ever set this (see Dialer's doc comment); production
	// callers leave it nil.
	Dial Dialer
	// DialTimeout bounds the socket probe's connect attempt so a wedged
	// (accepting-but-not-responding) listener cannot hang Scan
	// indefinitely. Defaults to 2s when zero.
	DialTimeout time.Duration
}

// ErrDaemonAlreadyRunning is returned (wrapped with cascade.KindConflict)
// when Scan's socket probe finds a live daemon already listening. This is
// the taxonomy mapping for the contract's "DAEMON_ALREADY_RUNNING" — the
// frozen 14-member cascade.Kind enumeration (R-14.3) has no literal
// DAEMON_ALREADY_RUNNING member, so this is carried as a KindConflict
// (the same taxonomy member scheduler/lock.go's Acquire uses for its
// analogous "someone else already holds this" case) with a message text
// that names the condition precisely.
var ErrDaemonAlreadyRunning = errors.New("runtime: daemon already running")

// Scan runs the crash-safety recovery sequence. Called at the top of
// daemon startup, before any domain lock acquisition (bootstrap.go). See
// the package-level doc above for exactly which lock mechanisms this
// does and does not reason about.
func Scan(ctx context.Context, opts RecoveryOptions) (*RecoveryEvent, error) {
	log, clock, dial := resolveScanDefaults(opts)

	if goruntime.GOOS == "windows" {
		log.Warn("platform-unsupported: unix socket probe skipped", "goos", goruntime.GOOS)
		return nil, nil
	}

	live, staleSocket, err := probeSocket(opts.SocketPath, opts.DialTimeout, dial)
	if err != nil {
		return nil, err
	}
	if live {
		return nil, cascade.Wrapf(cascade.KindConflict, ErrDaemonAlreadyRunning,
			"runtime: recovery scan aborted: a live daemon answered %s", opts.SocketPath)
	}

	ev := &RecoveryEvent{RecoveredAt: clock.Now()}

	if staleSocket {
		if err := RemoveSocketFile(opts.SocketPath); err != nil {
			return nil, cascade.Wrapf(cascade.KindUnavailable, err,
				"runtime: recovery: remove stale socket %s", opts.SocketPath)
		}
		log.Warn("runtime: removed stale socket file", "path", opts.SocketPath)
		ev.StaleSocket = true
	}

	if scanPidfile(opts.PidfilePath, log) {
		ev.StalePID = true
	}

	ev.StaleLocks = scanOrphanedLocks(ctx, opts.Registry, log)

	if !ev.StalePID && !ev.StaleSocket && len(ev.StaleLocks) == 0 {
		return nil, nil
	}
	if opts.Bus != nil {
		if err := publishRecoveryEvent(ctx, opts.Bus, ev); err != nil {
			return ev, cascade.Wrapf(cascade.KindUnavailable, err, "runtime: recovery: publish RecoveryEvent")
		}
	}
	return ev, nil
}

// resolveScanDefaults applies Scan's nil-fallback conventions (a nil Log
// falls back to slog.Default(), a nil Clock to NewSystemClock(), a nil
// Dial to defaultDialer) — split out of Scan to stay under Art.10.3's
// 50-line function cap (funlen).
func resolveScanDefaults(opts RecoveryOptions) (*slog.Logger, Clock, Dialer) {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	clock := opts.Clock
	if clock == nil {
		clock = NewSystemClock()
	}
	dial := opts.Dial
	if dial == nil {
		dial = defaultDialer
	}
	return log, clock, dial
}
