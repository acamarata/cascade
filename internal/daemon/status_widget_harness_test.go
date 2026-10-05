package daemon

// Purpose (this file): the harness the status.widget refresh tests share: a
// real providers.db opened through two handles (the deps' own and "another
// process's"), the real registration over it, a stepping Ticker that tells
// the test when the loop has finished a tick, and a reader of the frames the
// bus recorded.
//
// Inputs: t, a fixed clock, a fault-injecting provider source.
// Outputs: none.
// Constraints: no network; HOME-independent (everything under t.TempDir());
// the loop runs under daemon.Manifest.GoSupervised exactly as in
// production, and cleanup cancels and joins it before the databases close.
//
// SPORT: daemon.status_widget (tests, P1-WID-08).

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"
)

// harnessNow is the fixed instant every harness clock starts at.
var harnessNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

// stepTicker is a runtime.Ticker driven by the test. step delivers one tick
// and returns once the loop has handled it and is waiting again: the loop
// calls C() each time it re-enters its select, which is the signal.
type stepTicker struct {
	ticks   chan struct{}
	idle    chan struct{}
	stopped atomic.Bool
}

func newStepTicker() *stepTicker {
	return &stepTicker{ticks: make(chan struct{}), idle: make(chan struct{}, 1)}
}

func (s *stepTicker) C() <-chan struct{} {
	select {
	case s.idle <- struct{}{}:
	default:
	}
	return s.ticks
}

func (s *stepTicker) Stop() { s.stopped.Store(true) }

// waitIdle blocks until the loop is waiting for a tick.
func (s *stepTicker) waitIdle(t *testing.T) {
	t.Helper()
	select {
	case <-s.idle:
	case <-time.After(10 * time.Second):
		t.Fatal("the refresh loop never reached its select")
	}
}

// step delivers one tick and waits for the loop to finish handling it.
func (s *stepTicker) step(t *testing.T) {
	t.Helper()
	select {
	case s.ticks <- struct{}{}:
	case <-time.After(10 * time.Second):
		t.Fatal("the refresh loop did not take the tick")
	}
	s.waitIdle(t)
}

// flakySource wraps a ProviderSource with switchable faults.
type flakySource struct {
	inner      capacity.ProviderSource
	failLanes  atomic.Bool
	panicLanes atomic.Bool
}

func (f *flakySource) ListProviders(ctx context.Context) ([]registry.ProviderRecord, error) {
	return f.inner.ListProviders(ctx)
}

func (f *flakySource) ListLanes(ctx context.Context) ([]registry.LaneRecord, error) {
	if f.panicLanes.CompareAndSwap(true, false) {
		panic("injected ListLanes panic")
	}
	if f.failLanes.Load() {
		return nil, errInjectedLanes
	}
	return f.inner.ListLanes(ctx)
}

var errInjectedLanes = &injectedError{"injected ListLanes failure"}

type injectedError struct{ msg string }

func (e *injectedError) Error() string { return e.msg }

// widgetHarness is one registration over a real providers.db.
type widgetHarness struct {
	t        *testing.T
	clock    *runtime.FixedClock
	bus      *events.Bus
	store    provider.Store
	reg      *rpc.Registry
	deps     *StatusWidgetDeps
	source   *flakySource
	other    *registry.Registry // a second handle on providers.db: another process's path
	ticker   *stepTicker
	runCtx   context.Context
	cancel   context.CancelFunc
	manifest *Manifest
	logs     *bytes.Buffer
}

// openProvidersDB opens and migrates providers.db at dir through a fresh
// handle and closes it on cleanup.
func openProvidersDB(t *testing.T, dir string, clock runtime.Clock) *registry.Registry {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "providers.db"))
	if err != nil {
		t.Fatalf("open providers.db: %v", err)
	}
	db.SetMaxOpenConns(1)
	if err := registry.ApplyMigrationSchema(context.Background(), db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("migrate providers.db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return registry.NewRegistry(db, clock)
}

// newWidgetHarness registers status.widget over a fresh providers.db and
// starts its refresh loop under a Manifest, as the wire file does.
func newWidgetHarness(t *testing.T) *widgetHarness {
	t.Helper()
	paths := fakePaths{root: t.TempDir()}
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatalf("MkdirAll(DataDir): %v", err)
	}
	clock := runtime.NewFixedClock(harnessNow)
	h := &widgetHarness{t: t, clock: clock, store: storetest.NewMemStore(), reg: rpc.NewRegistry(), ticker: newStepTicker(), logs: &bytes.Buffer{}}
	h.bus = events.New(h.store, clock)
	t.Cleanup(func() { _ = h.bus.Close() })
	h.source = &flakySource{inner: openProvidersDB(t, paths.DataDir(), clock)}
	h.other = openProvidersDB(t, paths.DataDir(), clock)
	h.runCtx, h.cancel = context.WithCancel(context.Background())
	deps, err := RegisterStatusWidgetHandler(h.runCtx, h.reg, h.store, h.source, clock, h.bus, paths, func() bool { return false })
	if err != nil {
		t.Fatalf("RegisterStatusWidgetHandler: %v", err)
	}
	h.deps = deps
	h.manifest = NewManifest(slog.New(slog.NewTextHandler(h.logs, nil)), clock)
	h.manifest.GoSupervised(h.runCtx, StatusWidgetRefreshSubsystem, "test", func(ctx context.Context) error {
		return RunStatusWidgetRefresh(ctx, deps, h.bus, h.ticker, slog.New(slog.NewTextHandler(h.logs, nil)))
	})
	t.Cleanup(func() {
		h.cancel()
		h.manifest.Wait()
		_ = deps.Close()
	})
	h.ticker.waitIdle(t)
	return h
}

// seed stores a key provider and, when state is non-empty, its one lane.
func (h *widgetHarness) seed(name string, state registry.LaneState, reset time.Time) {
	h.t.Helper()
	ctx := context.Background()
	rec := registry.ProviderRecord{
		Name: name, Driver: registry.DriverOpenAICompat, BaseURL: "https://example.invalid/v1",
		Auth: registry.AuthKey, AuthRef: registry.VaultKeyRef("provider." + name + ".key"),
		AccountKind: registry.AccountPersonal, Tier: registry.TierMid, HealthStatus: registry.HealthUnknown,
	}
	if err := h.other.AddProvider(ctx, rec); err != nil {
		h.t.Fatalf("AddProvider %q: %v", name, err)
	}
	if state != "" {
		h.setLane(name, state, reset)
	}
}

// setLane writes name's lane through the second handle.
func (h *widgetHarness) setLane(name string, state registry.LaneState, reset time.Time) {
	h.t.Helper()
	lane := registry.LaneRecord{LaneName: name, ProviderName: name, Weight: 1, Capacity: registry.CapacityAPICredit, State: state, ResetEstimate: reset}
	if err := h.other.UpsertLane(context.Background(), lane); err != nil {
		h.t.Fatalf("UpsertLane %q: %v", name, err)
	}
}

// frames returns every status.widget_changed frame the bus recorded, decoded.
func (h *widgetHarness) frames() []capacity.WidgetSnapshot {
	h.t.Helper()
	evs, err := h.bus.Replay(context.Background(), statusWidgetNamespace, 0)
	if err != nil {
		h.t.Fatalf("Replay: %v", err)
	}
	var out []capacity.WidgetSnapshot
	for _, ev := range evs {
		if ev.Kind != StatusWidgetChangedKind {
			continue
		}
		var snap capacity.WidgetSnapshot
		if err := json.Unmarshal(ev.Payload, &snap); err != nil {
			h.t.Fatalf("decode frame: %v", err)
		}
		out = append(out, snap)
	}
	return out
}

// rpcSnapshot dispatches status.widget and returns its result.
func (h *widgetHarness) rpcSnapshot() (capacity.WidgetSnapshot, error) {
	h.t.Helper()
	res, errObj := h.reg.Dispatch(context.Background(), &rpc.Request{JSONRPC: "2.0", Method: MethodStatusWidget, ID: json.RawMessage(`1`)})
	if errObj != nil {
		return capacity.WidgetSnapshot{}, &injectedError{errObj.Message}
	}
	return res.(capacity.WidgetSnapshot), nil
}

// row returns the named row of snap.
func rowOf(t *testing.T, snap capacity.WidgetSnapshot, ref string) capacity.WidgetRow {
	t.Helper()
	for _, r := range snap.Rows {
		if r.Ref == ref {
			return r
		}
	}
	t.Fatalf("no row %q in %+v", ref, snap.Rows)
	return capacity.WidgetRow{}
}
