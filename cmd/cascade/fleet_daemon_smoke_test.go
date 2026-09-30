//go:build !windows

// Purpose: TestEpicRDaemonModeSmoke (P1-E12-W6-S121-T1) — the Epic R
//
//	daemon-mode smoke the ticket's own contract names: starts the REAL
//	daemon (platformDaemonRun, the same production entry point
//	`cascade daemon run` uses — not a bare buildRPCServer) over a seeded
//	fixture and asserts `cascade fleet sessions` (one-shot), `cascade
//	fleet top --once` (one frame), and `cascade fleet sessions --watch`
//	(first event) all return the seeded session through the real daemon
//	socket — the exact daemon-mode source P1-E18-W4-S40-T5's own journal
//	recorded as unreachable ("sessions.RegisterHandlers( is called only
//	from ... rpc_test.go — never from cmd/cascade/daemon_unix_run*.go").
//
//	Driving the full platformDaemonRun lifecycle (rather than a bare
//	net.Listen+buildRPCServer pair, as daemon_unix_run_fleet_test.go's
//	two composition-root tests use) is deliberate: an untagged unit test
//	may not import "net"/"net/http" (TestNoNetworkUnitTest_RealTreeGreen),
//	and platformDaemonRun's own real socket/serve loop already lives
//	entirely in production (non-test) files — the identical technique
//	daemon_unix_run_test.go's TestPlatformDaemonRun_ReturnsOnContextCancel
//	already established for this exact constraint. The three CLI
//	assertions below then dial that real socket through the SAME
//	production client path (internal/client.Client via
//	productionFleetSessionsDeps(), fleet_coverage_test.go's own pattern),
//	never importing "net" themselves either.
//
// SPORT: cmd/cascade/fleet (coverage-only addition, closes Epic R's
//
//	daemon-mode residual, register A1-126).
package main

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/runtime"
)

// syncBuffer is a mutex-guarded io.Writer. assertFleetSessionsWatchFirstEvent
// polls output on the test goroutine while runFleetSessionsWatch's own
// goroutine is still writing to it concurrently (Execute() blocks until
// the context is canceled) — a plain *bytes.Buffer's Write/String races,
// caught by -race the first time this test ran.
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

// seedFleetSmokeSession opens the SAME real sqlite store platformDaemonRun
// will open (openRuntimeStore over the same paths), upserts one session
// through it — with a real *events.Bus over that same store, so the
// fleet.sessions.changed event this Upsert emits is durably persisted,
// not merely swallowed by Store.emit's nil-bus best-effort skip; the
// daemon's own later Bus instance recovers it from the same store on
// Subscribe (events.Bus's package doc: "simulated restart" recovery),
// which is what makes --watch's first event assertion below possible —
// and closes the store before the daemon starts: sqlite serializes
// writers, so seeding must complete and close before platformDaemonRun's
// own openRuntimeStore call reopens the same file.
func seedFleetSmokeSession(t *testing.T, paths runtime.PathProvider, clock runtime.Clock, sessionID string) {
	t.Helper()
	store, _, closeStore, err := openRuntimeStore(context.Background(), paths, clock)
	if err != nil {
		t.Fatalf("openRuntimeStore (seed): %v", err)
	}
	defer closeStore()
	seed := sessions.New(store, clock, events.New(store, clock))
	if err := seed.Upsert(context.Background(), sessions.SessionRecord{
		SessionID: sessionID, Harness: "claude", Account: "acct-1", State: sessions.StateActive.String(),
	}); err != nil {
		t.Fatalf("seed Upsert: %v", err)
	}
}

// startFleetSmokeDaemon seeds one session then starts the real
// platformDaemonRun lifecycle in the background, waiting for its socket
// to answer before returning. deps.Paths' SocketPath is where callers
// dial. Cleanup cancels the daemon's context and waits for Run to return.
func startFleetSmokeDaemon(t *testing.T, sessionID string) daemonDeps {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	deps := newRunTestDeps(t, nil)
	seedFleetSmokeSession(t, deps.Paths, deps.Clock, sessionID)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- platformDaemonRun(ctx, deps) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if socketDialable(deps.Paths.SocketPath()) {
			return deps
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("daemon socket never became dialable")
	return deps
}

func TestEpicRDaemonModeSmoke(t *testing.T) {
	const sessionID = "smoke-1"
	runDeps := startFleetSmokeDaemon(t, sessionID)

	deps := productionFleetSessionsDeps()
	deps.Paths = runDeps.Paths
	daemonCtx := runtime.WithDaemonlessState(context.Background(), runtime.DaemonlessState{Embedded: false})

	t.Run("fleet sessions (one-shot)", func(t *testing.T) {
		assertFleetSessionsOnce(t, daemonCtx, deps, sessionID)
	})
	t.Run("fleet top --once (one frame)", func(t *testing.T) {
		assertFleetTopOnce(t, daemonCtx, deps, sessionID)
	})
	t.Run("fleet sessions --watch (first event)", func(t *testing.T) {
		assertFleetSessionsWatchFirstEvent(t, daemonCtx, deps, sessionID)
	})
}

// assertFleetSessionsOnce drives `cascade fleet sessions` (one-shot)
// through the real cobra Execute() entry point and asserts the seeded
// session appears in its output.
//
//nolint:revive // t-first is the idiomatic test-helper signature, not a context-ordering bug
func assertFleetSessionsOnce(t *testing.T, ctx context.Context, deps fleetSessionsDeps, sessionID string) {
	t.Helper()
	cmd, buf := newTestFleetCmd(deps)
	cmd.SetContext(ctx)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("cascade fleet sessions: %v", err)
	}
	if !strings.Contains(buf.String(), sessionID) {
		t.Fatalf("fleet sessions output = %q, want it to contain the seeded session %q", buf.String(), sessionID)
	}
}

// assertFleetTopOnce drives `cascade fleet top --once --json` (one frame)
// through the real cobra Execute() entry point and asserts the seeded
// session appears in its output.
//
//nolint:revive // t-first is the idiomatic test-helper signature, not a context-ordering bug
func assertFleetTopOnce(t *testing.T, ctx context.Context, deps fleetSessionsDeps, sessionID string) {
	t.Helper()
	cmd := newFleetTopCmd(deps)
	cmd.Flags().Bool("json", false, "")
	cmd.Flags().Bool("quiet", false, "")
	cmd.Flags().Bool("verbose", false, "")
	cmd.Flags().Bool("no-color", false, "")
	buf := &strings.Builder{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{"--once", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("cascade fleet top --once: %v", err)
	}
	if !strings.Contains(buf.String(), sessionID) {
		t.Fatalf("fleet top --once output = %q, want it to contain the seeded session %q", buf.String(), sessionID)
	}
}

// assertFleetSessionsWatchFirstEvent drives `cascade fleet sessions
// --watch` through the real cobra Execute() entry point in a goroutine
// (Execute blocks until its context is canceled), polls its output for
// the seeded session's first event, then cancels and waits for a clean
// return.
//
//nolint:revive // t-first is the idiomatic test-helper signature, not a context-ordering bug
func assertFleetSessionsWatchFirstEvent(t *testing.T, parentCtx context.Context, deps fleetSessionsDeps, sessionID string) {
	t.Helper()
	watchCtx, cancelWatch := context.WithCancel(parentCtx)
	defer cancelWatch()

	cmd := newFleetSessionsCmd(deps)
	cmd.Flags().Bool("json", false, "")
	cmd.Flags().Bool("quiet", false, "")
	cmd.Flags().Bool("verbose", false, "")
	cmd.Flags().Bool("no-color", false, "")
	buf := &syncBuffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetContext(watchCtx)
	cmd.SetArgs([]string{"--watch"})
	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(buf.String(), sessionID) {
		time.Sleep(10 * time.Millisecond)
	}
	got := buf.String()
	cancelWatch()
	<-done

	if !strings.Contains(got, sessionID) {
		t.Fatalf("fleet sessions --watch first event = %q, want it to contain the seeded session %q", got, sessionID)
	}
}
