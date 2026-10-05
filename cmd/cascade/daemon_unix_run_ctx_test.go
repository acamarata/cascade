//go:build !windows

// Purpose: the daemon run context (daemonWiring.Ctx, withRunContext) and the
//
//	harness the shutdown tests share: test-only daemon registrations, a
//	recorded bridge transport, and a bounded wait for platformDaemonRun.
//
// Constraints: test registrations are appended inside the test and removed
//
//	by t.Cleanup; synchronization is by channels and joins only, a timer
//	exists only to fail a test that would otherwise hang. No network: the
//	bridge transport never dials (withBridgeTransport).
//
// SPORT: cmd/cascade/daemon (ADD, run-context tests).
package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// testDaemonBound fails a test whose daemon never starts or never returns.
const testDaemonBound = 90 * time.Second

// testManifest is the Manifest the starter tests pass: a nil logger
// discards its lines.
func testManifest() *daemon.Manifest { return daemon.NewManifest(nil, nil) }

// addTestRegistration appends r to the daemon registrations for this test
// only.
func addTestRegistration(t *testing.T, r daemonRegistration) {
	t.Helper()
	saved := daemonRegistrations
	daemonRegistrations = append(append([]daemonRegistration(nil), saved...), r)
	t.Cleanup(func() { daemonRegistrations = saved })
}

// observeWiring registers a last-phase test registration that hands the
// finished daemonWiring to the returned channel, once per daemon build.
func observeWiring(t *testing.T) <-chan *daemonWiring {
	t.Helper()
	seen := make(chan *daemonWiring, 1)
	addTestRegistration(t, daemonRegistration{
		Name: "test-observe-wiring", Phase: phaseMCPLast + 90, Order: 1,
		Wire: func(w *daemonWiring) error { seen <- w; return nil },
	})
	return seen
}

// bridgePollRecorder is the bridge's recorded transport: it counts the
// getUpdates calls the poll makes and fails each one without dialling.
type bridgePollRecorder struct {
	mu         sync.Mutex
	getUpdates int
	once       sync.Once
	first      chan struct{}
}

func newBridgePollRecorder() *bridgePollRecorder {
	return &bridgePollRecorder{first: make(chan struct{})}
}

func (r *bridgePollRecorder) roundTrip(_ context.Context, _, path string) error {
	if strings.HasSuffix(path, "/getUpdates") {
		r.mu.Lock()
		r.getUpdates++
		r.mu.Unlock()
		r.once.Do(func() { close(r.first) })
	}
	return cascade.New(cascade.KindUnavailable, "test bridge transport: no network")
}

func (r *bridgePollRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.getUpdates
}

// routeBridgeThrough registers a first-phase test registration that adds
// withBridgeTransport(rec) to the wiring's options, the route the bridge
// client reaches wireCascadePABridge by.
func routeBridgeThrough(t *testing.T, rec *bridgePollRecorder) {
	t.Helper()
	addTestRegistration(t, daemonRegistration{
		Name: "test-bridge-transport", Phase: phaseMiddleware, Order: 900,
		Wire: func(w *daemonWiring) error {
			w.Opts = append(w.Opts, withBridgeTransport(rec.roundTrip))
			return nil
		},
	})
}

// startTestDaemon runs platformDaemonRun until the observed wiring arrives,
// and returns it with the cancel func and Run's result channel.
func startTestDaemon(t *testing.T, deps daemonDeps, seen <-chan *daemonWiring) (*daemonWiring, context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- platformDaemonRun(ctx, deps) }()
	guard := time.NewTimer(testDaemonBound)
	defer guard.Stop()
	select {
	case w := <-seen:
		return w, cancel, done
	case err := <-done:
		cancel()
		t.Fatalf("platformDaemonRun returned before its wiring finished: %v", err)
	case <-guard.C:
		cancel()
		t.Fatal("platformDaemonRun never finished its wiring")
	}
	return nil, cancel, done
}

// awaitDaemonReturn waits for platformDaemonRun's result, bounded.
func awaitDaemonReturn(t *testing.T, done <-chan error) {
	t.Helper()
	guard := time.NewTimer(testDaemonBound)
	defer guard.Stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("platformDaemonRun: %v", err)
		}
	case <-guard.C:
		t.Fatal("platformDaemonRun did not return after its context was cancelled: a supervised goroutine was never stopped")
	}
}

// TestBuildRPCServerNamespacesUseRunContext: a registration added inside the
// test records daemonWiring.Ctx; it is the run context withRunContext
// supplied, so cancelling the run context cancels the recorded one.
func TestBuildRPCServerNamespacesUseRunContext(t *testing.T) {
	seen := observeWiring(t)
	clock := runtime.NewSystemClock()
	paths := fakeDaemonPaths{root: t.TempDir()}
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	bus := events.New(storetest.NewMemStore(), clock)
	t.Cleanup(func() { _ = bus.Close() })
	runCtx, cancelRun := context.WithCancel(context.Background())
	manifest := testManifest()
	t.Cleanup(func() { cancelRun(); manifest.Wait() })
	_, got, _, err := buildRPCServer(bus, clock, nil, daemon.Settings{SocketPath: paths.SocketPath()}, paths, nil,
		storetest.NewMemStore(), withRunContext(runCtx), withManifest(manifest))
	if err != nil {
		t.Fatalf("buildRPCServer: %v", err)
	}
	w := <-seen
	if got != manifest || w.Manifest != manifest {
		t.Fatal("buildRPCServer did not use the Manifest withManifest supplied")
	}
	if w.Ctx.Err() != nil {
		t.Fatalf("recorded ctx already done before cancel: %v", w.Ctx.Err())
	}
	cancelRun()
	select {
	case <-w.Ctx.Done():
	default:
		t.Fatal("cancelling the run context did not cancel daemonWiring.Ctx")
	}
}

// TestPlatformDaemonRunPassesRunContext is the source-shape proof: with
// go/parser, platformDaemonRun hands its ctx to composeDaemon, which derives
// runCtx with context.WithCancel from its own ctx argument and passes
// withRunContext(runCtx) to buildRPCServer and runCtx to
// wireBackgroundSubsystems. Cleanups run LIFO, so closeStore, then bus.Close,
// then the background cleanup and the join must be appended in that order.
func TestPlatformDaemonRunPassesRunContext(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(".", "daemon_unix.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse daemon_unix.go: %v", err)
	}
	src := map[string]string{}
	for _, d := range file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil {
			src[fd.Name.Name] = funcShape(fd)
		}
	}
	for _, want := range []struct{ fn, shape string }{
		{"platformDaemonRun", `\ncomposeDaemon\(ctx, `},
		{"composeDaemon", `\nrunCtx, cancelRun := context\.WithCancel\(ctx\)\n`},
		{"composeDaemon", `\nwireBackgroundSubsystems\(runCtx, `},
		{"composeDaemon", `\nbuildRPCServer\([^\n]*, withRunContext\(runCtx\)[,)]`},
		{"composeDaemon", `\nappend\(cleanups, closeStore\)\n(?:.|\n)*\nappend\(cleanups, \(func\(\) literal\)\)\n\nbus\.Close\(\)\n(?:.|\n)*\nappend\(cleanups, cleanupBackground, joinRun\)\n`},
	} {
		if !regexp.MustCompile(want.shape).MatchString(src[want.fn]) {
			t.Errorf("%s does not contain %q; its calls and assignments are:%s", want.fn, want.shape, src[want.fn])
		}
	}
	for _, fn := range []string{"platformDaemonRun", "composeDaemon"} {
		if p := firstParamName(file, fn); p != "ctx" {
			t.Errorf("%s's first parameter is %q, want ctx (the shapes above name it)", fn, p)
		}
	}
}

// funcShape renders every call and every := or = assignment in fd, one per
// line, each line also followed by its call arguments so a nested call
// matches on its own.
func funcShape(fd *ast.FuncDecl) string {
	var b strings.Builder
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			if len(x.Rhs) == 1 && len(x.Lhs) == 2 {
				b.WriteString("\n" + exprText(x.Lhs[0]) + ", " + exprText(x.Lhs[1]) + " " + x.Tok.String() + " " + exprText(x.Rhs[0]) + "\n")
			}
		case *ast.CallExpr:
			b.WriteString("\n" + exprText(x) + "\n")
		}
		return true
	})
	return b.String()
}

// TestJoinRunContextWaitsForSupervised: in a synctest bubble the join cancels
// the run context (else the gated fn never passes ctx.Done and the bubble
// deadlocks) and stays blocked until that fn returns. synctest.Wait makes
// "still blocked" exact: WaitGroup.Wait is durably blocking in the bubble.
func TestJoinRunContextWaitsForSupervised(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runCtx, cancelRun := context.WithCancel(context.Background())
		manifest, gate, joined := testManifest(), make(chan struct{}), make(chan struct{})
		manifest.GoSupervised(runCtx, "test.gated", "gated", func(ctx context.Context) error { <-ctx.Done(); <-gate; return nil })
		go func() { joinRunContext(cancelRun, manifest)(); close(joined) }()
		synctest.Wait()
		select {
		case <-joined:
			t.Error("joinRunContext returned before the gated supervised goroutine did")
		default:
		}
		close(gate)
		<-joined
		manifest.Wait()
	})
}

// firstParamName names the first parameter of the top-level func fn.
func firstParamName(file *ast.File, fn string) string {
	for _, d := range file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == fn && len(fd.Type.Params.List) > 0 && len(fd.Type.Params.List[0].Names) > 0 {
			return fd.Type.Params.List[0].Names[0].Name
		}
	}
	return ""
}

// TestDaemonTestsNeverTouchKeychain: with the hook init() set, a daemon run
// with the bridge and chat enabled routes both custody selections through it
// onto the file vault, the bridge starts from the token stored there, and the
// failing platform Runner is never called.
func TestDaemonTestsNeverTouchKeychain(t *testing.T) {
	deps := newRunTestDeps(t, nil)
	enableTestBridge(t, deps.Paths.DataDir())
	routeBridgeThrough(t, newBridgePollRecorder())
	resetTestCustodyLog()
	w, cancel, done := startTestDaemon(t, deps, observeWiring(t))
	cancel()
	awaitDaemonReturn(t, done)
	sites := map[string]bool{}
	for _, call := range testCustodyCalls() {
		sites[call.site] = true
		custody, err := secrets.SelectCustody(call.cfg)
		if err != nil || custody.Name() != "file-vault" {
			t.Errorf("%s custody = %v (err %v), want the file vault", call.site, custody, err)
		}
	}
	if !sites[custodySiteChat] || !sites[custodySiteBridge] {
		t.Fatalf("custody sites routed through the test hook = %v, want both %q and %q", sites, custodySiteChat, custodySiteBridge)
	}
	for _, row := range w.Manifest.Snapshot() {
		if row.Name == "cascade-pa.bridge" && row.State != daemon.SubsystemRunning {
			t.Errorf("bridge = %+v, want running from the file-vault token", row)
		}
	}
	if n := testCustodyRunnerCalls.Load(); n != 0 {
		t.Fatalf("the platform keychain Runner was called %d time(s)", n)
	}
}
