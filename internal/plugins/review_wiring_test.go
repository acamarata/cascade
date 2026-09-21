// Purpose: unit coverage for review_wiring.go's two adapters, isolated
//
//	from the real home directory and the real daemon socket -- the same
//	fakePathProvider/missingSocketPath doubles cascadepa_wiring_test.go
//	already declares in this package.
//
// SPORT: internal/plugins:review-wiring (TEST) -- P1-E25-W5-S52-T4.
package plugins

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	reviewengine "github.com/acamarata/cascade/internal/review"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestReviewModelExecutor_PathResolutionFailure proves a path-resolution
// failure surfaces as a real, wrapped error -- never a silent no-op.
func TestReviewModelExecutor_PathResolutionFailure(t *testing.T) {
	wantErr := errors.New("boom: no home directory")
	e := newReviewModelExecutor(nil, time.Second, func() (runtime.PathProvider, error) {
		return nil, wantErr
	})
	_, err := e.Execute(context.Background(), provider.ModelRequest{TaskID: "t", Inputs: []provider.ChatMessage{{Role: "user", Content: "x"}}})
	if err == nil {
		t.Fatal("Execute: err = nil, want a path-resolution error")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("Execute: err = %v, want it to wrap %v", err, wantErr)
	}
}

// TestReviewModelExecutor_TransportUnreachable proves a real dial to a
// socket nothing listens on fails through internal/client's own classified
// transport error, reached through pkg/provider.Client.ModelExecute -- the
// exact production path, not a substituted double.
func TestReviewModelExecutor_TransportUnreachable(t *testing.T) {
	socket := missingSocketPath(t)
	e := newReviewModelExecutor(client.UnixDialer, time.Second, func() (runtime.PathProvider, error) {
		return fakePathProvider{socket: socket}, nil
	})
	_, err := e.Execute(context.Background(), provider.ModelRequest{TaskID: "t", Inputs: []provider.ChatMessage{{Role: "user", Content: "x"}}})
	if err == nil {
		t.Fatal("Execute: err = nil, want a transport error")
	}
	if got := err.Error(); !containsAll(got, "conductor.execute") {
		t.Errorf("Execute: err = %q, want it to name conductor.execute", got)
	}
}

// reviewFakePaths is this file's own runtime.PathProvider double, with a
// configurable DataDir -- fakePathProvider (cascadepa_wiring_test.go)
// hardcodes DataDir() to a nonexistent "/fake/data", which cannot host a
// real SQLite file this file's registry tests need to open and migrate.
type reviewFakePaths struct{ dataDir, socket string }

func (p reviewFakePaths) Root() string                       { return p.dataDir }
func (p reviewFakePaths) ConfigPath() string                 { return p.dataDir + "/config.toml" }
func (p reviewFakePaths) SocketPath() string                 { return p.socket }
func (p reviewFakePaths) DataDir() string                    { return p.dataDir }
func (p reviewFakePaths) LogDir() string                     { return p.dataDir + "/logs" }
func (p reviewFakePaths) StorageRoot(runtime.Profile) string { return p.dataDir + "/storage" }

// TestReviewRegistryReader_PathResolutionFailure proves the registry
// adapter's path-resolution failure is real and propagated, not swallowed
// into an empty, false-confident family list.
func TestReviewRegistryReader_PathResolutionFailure(t *testing.T) {
	wantErr := errors.New("boom: no data dir")
	r := reviewRegistryReader{resolvePaths: func() (runtime.PathProvider, error) { return nil, wantErr }, clock: runtime.NewSystemClock()}
	if _, err := r.ListProviders(context.Background()); !errors.Is(err, wantErr) {
		t.Errorf("ListProviders: err = %v, want it to wrap %v", err, wantErr)
	}
}

// TestReviewRegistryReader_RealMigratedDB proves the adapter opens and
// migrates a real, fresh SQLite file under a temp data dir, then queries
// it successfully through the real registry.Reader -- an empty registry
// reports zero providers and NO error, never a fabricated family.
func TestReviewRegistryReader_RealMigratedDB(t *testing.T) {
	dataDir := t.TempDir()
	r := reviewRegistryReader{
		resolvePaths: func() (runtime.PathProvider, error) { return reviewFakePaths{dataDir: dataDir, socket: "/unused"}, nil },
		clock:        runtime.NewSystemClock(),
	}
	infos, err := r.ListProviders(context.Background())
	if err != nil {
		t.Fatalf("ListProviders against a freshly migrated empty registry: %v", err)
	}
	if len(infos) != 0 {
		t.Errorf("ListProviders = %v, want an empty (not fabricated) slice", infos)
	}
	// A second call re-opens and re-migrates the SAME file idempotently
	// (per-call lifecycle, not a stale handle).
	if _, err := r.GetByModel(context.Background(), "any-model"); err != nil {
		t.Errorf("GetByModel on a re-opened registry: %v", err)
	}
	if _, err := r.ListLanes(context.Background()); err != nil {
		t.Errorf("ListLanes on a re-opened registry: %v", err)
	}
	if _, err := r.ListPool(context.Background(), "any-pool"); err != nil {
		t.Errorf("ListPool on a re-opened registry: %v", err)
	}
	if _, err := r.GetProvider(context.Background(), "does-not-exist"); err == nil {
		t.Error("GetProvider(unknown name) returned nil error, want the registry's not-found error")
	}
}

// TestReviewInitConstructionSucceeds proves the exact collaborator
// construction init() performs succeeds with the real production
// constructors (client.UnixDialer, runtime.NewDefaultPathProvider,
// runtime.NewSystemClock) -- the same construction that, inside init(),
// would otherwise panic per this file's own documented reasoning.
func TestReviewInitConstructionSucceeds(t *testing.T) {
	_, err := newRealReviewProvider()
	if err != nil {
		t.Fatalf("the exact construction init() performs failed: %v", err)
	}
}

// TestReviewSlogEventsPublishesTheFallback proves the PRODUCTION event
// publisher actually writes a line, and that the line names every field an
// operator needs: the level, the consequence class, the families and the
// reason (CR fix D5 -- the AC is "used and LOGGED, never silent", and before
// this there was no destination at all in internal/review or in this wiring).
func TestReviewSlogEventsPublishesTheFallback(t *testing.T) {
	var buf bytes.Buffer
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(restore) })

	reviewSlogEvents{}.Publish(context.Background(), reviewengine.Event{
		Fallback: &reviewengine.FallbackEvent{
			Level:            provider.ReviewCRLevelB,
			ConsequenceClass: "normal",
			Families:         []string{"anthropic"},
			Reason:           "only one family registered",
		},
	})

	line := buf.String()
	for _, want := range []string{"same-family reviewer fallback", "CR-B", "normal", "anthropic", "only one family registered"} {
		if !strings.Contains(line, want) {
			t.Errorf("the published log line does not carry %q:\n%s", want, line)
		}
	}
}

// TestReviewSlogEventsIgnoresOtherEvents proves the publisher is not a
// constant: an event with no Fallback writes nothing at all.
func TestReviewSlogEventsIgnoresOtherEvents(t *testing.T) {
	var buf bytes.Buffer
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(restore) })

	reviewSlogEvents{}.Publish(context.Background(), reviewengine.Event{})
	if buf.Len() != 0 {
		t.Errorf("an event with no Fallback wrote %q, want nothing", buf.String())
	}
}

// TestInstallReviewProvider_PanicsOnConstructionFailure proves init()'s
// extracted body (installReviewProvider) actually panics when the
// constructor it is handed fails -- the branch init() itself can never
// exercise, since newRealReviewProvider's two literal constructions can
// never fail in production (this file's own doc comment on init()).
func TestInstallReviewProvider_PanicsOnConstructionFailure(t *testing.T) {
	wantErr := errors.New("boom: nil collaborator")
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("installReviewProvider: no panic, want one on a failing constructor")
		}
		if !strings.Contains(r.(string), wantErr.Error()) {
			t.Errorf("panic value = %q, want it to contain %q", r, wantErr.Error())
		}
	}()
	installReviewProvider(func() (*reviewengine.Provider, error) { return nil, wantErr })
}

// TestInstallReviewProvider_SucceedsAndInstalls proves the non-panic path:
// a real construction installs into reviewplugin without panicking.
func TestInstallReviewProvider_SucceedsAndInstalls(_ *testing.T) {
	installReviewProvider(newRealReviewProvider)
}

// TestReviewModelExecutor_UsesInjectedDoer proves rpcClient's doer-injection
// branch: when e.doer is already set, Execute dials it directly and never
// calls resolvePaths at all -- the same seam
// cascadepa_rpc_success_test.go's fakeRPCDoer already proves for the other
// composition-root adapters in this package.
func TestReviewModelExecutor_UsesInjectedDoer(t *testing.T) {
	doer := &fakeRPCDoer{fill: func(out any) {
		resp := out.(*provider.ModelResponse)
		resp.Output = "ok"
	}}
	e := newReviewModelExecutor(nil, time.Second, func() (runtime.PathProvider, error) {
		t.Fatal("resolvePaths must not be called when doer is already injected")
		return nil, nil
	})
	e.doer = doer

	resp, err := e.Execute(context.Background(), provider.ModelRequest{TaskID: "t", Inputs: []provider.ChatMessage{{Role: "user", Content: "x"}}})
	if err != nil {
		t.Fatalf("Execute with an injected doer: %v", err)
	}
	if resp.Output != "ok" {
		t.Errorf("Execute output = %q, want %q", resp.Output, "ok")
	}
	if doer.calledMethod != "conductor.execute" {
		t.Errorf("calledMethod = %q, want conductor.execute", doer.calledMethod)
	}
}

// TestReviewRegistryReader_MigrationFailure proves open()'s migration-
// failure branch: a DataDir whose parent does not exist lets sql.Open
// succeed lazily (modernc.org/sqlite's Driver implements only the plain
// driver.Open, so database/sql.Open never touches the filesystem eagerly --
// verified directly against the vendored driver before writing this test),
// then fails for real the moment ApplyMigrationSchema issues its first
// statement. The db handle this failure opened must be closed, not leaked.
func TestReviewRegistryReader_MigrationFailure(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "does-not-exist")
	r := reviewRegistryReader{
		resolvePaths: func() (runtime.PathProvider, error) { return reviewFakePaths{dataDir: dataDir, socket: "/unused"}, nil },
		clock:        runtime.NewSystemClock(),
	}
	if _, err := r.ListProviders(context.Background()); err == nil {
		t.Fatal("ListProviders against an unopenable data dir: err = nil, want a real migration error")
	}
}

// TestReviewRegistryReader_ReaderErrorPropagates proves every registry
// method -- not just ListProviders -- propagates a r.reader(ctx) failure
// rather than silently reporting an empty, false-confident result.
func TestReviewRegistryReader_ReaderErrorPropagates(t *testing.T) {
	wantErr := errors.New("boom: no data dir")
	r := reviewRegistryReader{resolvePaths: func() (runtime.PathProvider, error) { return nil, wantErr }, clock: runtime.NewSystemClock()}

	if _, err := r.GetProvider(context.Background(), "any"); !errors.Is(err, wantErr) {
		t.Errorf("GetProvider: err = %v, want it to wrap %v", err, wantErr)
	}
	if _, err := r.ListLanes(context.Background()); !errors.Is(err, wantErr) {
		t.Errorf("ListLanes: err = %v, want it to wrap %v", err, wantErr)
	}
	if _, err := r.ListPool(context.Background(), "any-pool"); !errors.Is(err, wantErr) {
		t.Errorf("ListPool: err = %v, want it to wrap %v", err, wantErr)
	}
	if _, err := r.GetByModel(context.Background(), "any-model"); !errors.Is(err, wantErr) {
		t.Errorf("GetByModel: err = %v, want it to wrap %v", err, wantErr)
	}
}
