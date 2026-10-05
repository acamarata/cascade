//go:build !windows

// Purpose: the daemon's shutdown order. Cancelling the run context (or a
//
//	termination signal) joins every supervised goroutine before the
//	background cleanup and the store close run; afterwards no goroutine runs
//	cascade code, and the bridge poll has stopped without any OS signal.
//
// Constraints: the goroutine-dump tests re-run themselves in a child test
//
//	process so no other test's goroutines are present. No sleeps: ordering
//	is proven by channels and joins; a timer only fails a hung test.
//
// SPORT: cmd/cascade/daemon (ADD, shutdown-order tests).
package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
)

// daemonChildEnv names the test a child process runs for real.
const daemonChildEnv = "CASCADE_TEST_DAEMON_CHILD"

// supervisedConsumers are the background subsystems a daemon run starts
// supervised; each must end Skipped("stopped") once the run has joined.
var supervisedConsumers = []string{
	fleetMetricsSubsystem, ciAttentionSubsystem, schedulerLoopSubsystem, memoryProjectionSubsystem,
}

// inDaemonChild reports true in the child process. In the parent it runs
// t's test alone in a child process of this test binary and fails t unless
// the child passed.
func inDaemonChild(t *testing.T) bool {
	t.Helper()
	if os.Getenv(daemonChildEnv) == t.Name() {
		return true
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^"+t.Name()+"$", "-test.count=1", "-test.v", "-test.timeout=300s")
	cmd.Env = append(os.Environ(), daemonChildEnv+"="+t.Name())
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: "+t.Name()) {
		t.Fatalf("child %s failed (%v):\n%s", t.Name(), err, out)
	}
	return false
}

// cascadeGoroutines returns, for every goroutine but the caller's, the first
// frame that runs cascade code (any github.com/acamarata/cascade package or
// this package main, except the test binary's entry frames).
func cascadeGoroutines() []string {
	buf := make([]byte, 1<<20)
	for {
		n := goruntime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	stanzas := strings.Split(string(buf), "\n\n")
	var out []string
	for _, st := range stanzas[1:] {
		lines := strings.Split(st, "\n")
		for _, line := range lines[1:] {
			if strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "created by ") || isTestMainFrame(line) {
				continue
			}
			if strings.HasPrefix(line, "github.com/acamarata/cascade/") || strings.HasPrefix(line, "main.") {
				out = append(out, lines[0]+" "+line)
				break
			}
		}
	}
	return out
}

// isTestMainFrame reports the test binary's own entry frames, which run for
// the whole process and are not a daemon goroutine.
func isTestMainFrame(line string) bool {
	return strings.HasPrefix(line, "main.main(") || strings.HasPrefix(line, "main.TestMain(") ||
		strings.HasPrefix(line, "github.com/acamarata/cascade/cmd/cascade.TestMain(")
}

// assertJoined checks every supervised consumer ended Skipped("stopped") and
// no goroutine still runs cascade code.
func assertJoined(t *testing.T, m *daemon.Manifest) {
	t.Helper()
	rows := map[string]daemon.SubsystemStatus{}
	for _, s := range m.Snapshot() {
		rows[s.Name] = s
	}
	for _, name := range supervisedConsumers {
		if got := rows[name]; got.State != daemon.SubsystemSkipped || got.Detail != "stopped" {
			t.Errorf("%s = %+v after the run, want skipped/stopped (supervised and joined)", name, got)
		}
	}
	if left := cascadeGoroutines(); len(left) != 0 {
		t.Errorf("%d goroutine(s) still run cascade code after the daemon returned:\n%s", len(left), strings.Join(left, "\n"))
	}
}

// awaitBridgePoll waits, bounded, for the bridge poll's first getUpdates.
func awaitBridgePoll(t *testing.T, rec *bridgePollRecorder) {
	t.Helper()
	guard := time.NewTimer(testDaemonBound)
	defer guard.Stop()
	select {
	case <-rec.first:
	case <-guard.C:
		t.Fatal("the bridge poll never issued getUpdates through the recorded transport")
	}
}

// testBridgeVaultKey is the vault key the bridge reads its bot token from
// (internal/plugins' default).
const testBridgeVaultKey = "cascade-pa.telegram.bot_token"

// enableTestBridge enables the telegram module in dataDir's cascade-pa
// manifest and stores a synthetic token, with a standing grant, in the file
// vault the custody hook selects for the bridge. The token is never sent
// anywhere: the bridge's transport is recorded, not dialled.
func enableTestBridge(t *testing.T, dataDir string) {
	t.Helper()
	manifest := filepath.Join(dataDir, "plugins", "cascade-pa", "manifest.toml")
	if err := os.MkdirAll(filepath.Dir(manifest), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(manifest, []byte("[modules.telegram]\nenabled = true\n"), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	custody, err := secrets.SelectCustody(daemonCustodyConfig(custodySiteBridge, plugins.BridgeVaultConfig(dataDir)))
	if err != nil {
		t.Fatalf("SelectCustody: %v", err)
	}
	broker, err := secrets.NewBroker(custody, nil)
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}
	token := "111111" + ":" + "SYNTHETIC-DAEMON-TEST"
	if _, err := broker.Set(context.Background(), testBridgeVaultKey, []byte(token), secrets.SetUpdate); err != nil {
		t.Fatalf("Set: %v", err)
	}
	grantStore, err := secrets.NewFileGrantStore(dataDir)
	if err != nil {
		t.Fatalf("NewFileGrantStore: %v", err)
	}
	grants, err := secrets.NewGrants(grantStore, runtime.NewSystemClock())
	if err != nil {
		t.Fatalf("NewGrants: %v", err)
	}
	if _, err := grants.Issue(context.Background(), testBridgeVaultKey, 10*time.Minute); err != nil {
		t.Fatalf("Issue: %v", err)
	}
}

// startBridgedDaemon enables the bridge over a recorded transport, observes
// the wiring and starts platformDaemonRun.
func startBridgedDaemon(t *testing.T) (*daemonWiring, *bridgePollRecorder, context.CancelFunc, <-chan error) {
	t.Helper()
	deps := newRunTestDeps(t, nil)
	enableTestBridge(t, deps.Paths.DataDir())
	rec := newBridgePollRecorder()
	routeBridgeThrough(t, rec)
	w, cancel, done := startTestDaemon(t, deps, observeWiring(t))
	awaitBridgePoll(t, rec)
	return w, rec, cancel, done
}

// TestDaemonRunJoinsEveryGoroutine: with metrics, CI attention, the
// scheduler loop, memory projection and the bridge poll running, cancelling
// the context makes platformDaemonRun join all of them before it returns.
func TestDaemonRunJoinsEveryGoroutine(t *testing.T) {
	if !inDaemonChild(t) {
		return
	}
	w, _, cancel, done := startBridgedDaemon(t)
	cancel()
	awaitDaemonReturn(t, done)
	assertJoined(t, w.Manifest)
}

// serveUntilSignal runs platformDaemonRun's own three steps (composeDaemon,
// daemon.Run, the cleanups) with an injected SIGTERM on RunOptions.Signals,
// the only place the signal seam exists. beforeRun runs once the wiring is
// done. It checks the run context outlived Run, so only the cleanups'
// join can have ended it.
func serveUntilSignal(t *testing.T, deps daemonDeps, beforeRun func()) *daemonWiring {
	t.Helper()
	seen := observeWiring(t)
	opts, cleanups, err := composeDaemon(context.Background(), deps, nil)
	if err != nil {
		runCleanupsLIFO(cleanups)
		t.Fatalf("composeDaemon: %v", err)
	}
	w := <-seen
	beforeRun()
	sigs := make(chan os.Signal, 1)
	sigs <- syscall.SIGTERM
	opts.Signals = sigs
	runErr := daemon.Run(context.Background(), opts)
	ctxEndedEarly := w.Ctx.Err() != nil
	runCleanupsLIFO(cleanups)
	if runErr != nil || ctxEndedEarly {
		t.Fatalf("daemon.Run = %v, run context ended before Run returned = %v", runErr, ctxEndedEarly)
	}
	if w.Ctx.Err() == nil {
		t.Fatal("the cleanups did not cancel the run context")
	}
	return w
}

// TestShutdownOnSignalJoins: the same join holds when Run returns through an
// injected SIGTERM on RunOptions.Signals rather than a cancelled context.
func TestShutdownOnSignalJoins(t *testing.T) {
	if !inDaemonChild(t) {
		return
	}
	deps := newRunTestDeps(t, nil)
	enableTestBridge(t, deps.Paths.DataDir())
	rec := newBridgePollRecorder()
	routeBridgeThrough(t, rec)
	w := serveUntilSignal(t, deps, func() { awaitBridgePoll(t, rec) })
	assertJoined(t, w.Manifest)
}

// TestShutdownJoinsBeforeStoreClose: a test registration's supervised
// goroutine waits for the run context to end and then reads the store. Run
// returns through a signal, so only the cleanups' join ends that context.
// The read succeeds on an open store before the cleanups return. This is the
// end-to-end check only: with no Wait the reader can still win the race to
// closeStore, so TestJoinRunContextWaitsForSupervised proves the Wait itself.
func TestShutdownJoinsBeforeStoreClose(t *testing.T) {
	result := make(chan error, 1)
	addTestRegistration(t, daemonRegistration{
		Name: "test-store-reader", Phase: phaseSupervised, Order: 990,
		Wire: func(w *daemonWiring) error {
			if err := w.Store.Put(w.Ctx, "test-shutdown-order", "k", []byte("open")); err != nil {
				return err
			}
			w.Manifest.GoSupervised(w.Ctx, "test.store-reader", "waiting for cancel", func(ctx context.Context) error {
				<-ctx.Done()
				got, err := w.Store.Get(context.WithoutCancel(ctx), "test-shutdown-order", "k")
				if err == nil && string(got) != "open" {
					err = os.ErrInvalid
				}
				result <- err
				return nil
			})
			return nil
		},
	})
	serveUntilSignal(t, newRunTestDeps(t, nil), func() {})
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("the supervised read after cancel failed (%v): the store closed before the join", err)
		}
	default:
		t.Fatal("the cleanups returned before the supervised reader finished: no join before the store close")
	}
}

// TestBridgePollStopsOnRunContextCancel: cancelling the run context, with no
// OS signal, stops the bridge poll. After platformDaemonRun returns no poll
// goroutine is left and the recorded getUpdates count no longer changes.
func TestBridgePollStopsOnRunContextCancel(t *testing.T) {
	_, rec, cancel, done := startBridgedDaemon(t)
	cancel()
	awaitDaemonReturn(t, done)
	after := rec.count()
	for _, g := range cascadeGoroutines() {
		if strings.Contains(g, "plugins/cascade-pa/telegram.") {
			t.Fatalf("a bridge goroutine outlived the run: %s", g)
		}
	}
	if after < 1 || rec.count() != after {
		t.Fatalf("getUpdates count = %d after the join, then %d; want a non-zero count that no longer changes", after, rec.count())
	}
}
