//go:build !windows

// Purpose: the status-widget registration in process over a test-built
//
//	daemonWiring: TestStatusWidgetRowsFromProviderEvidence (rows from
//	lane state the P1-WID-11 resolver path wrote through a second
//	registry handle), the fixture regenerated from that same path, and
//	TestStatusWidgetRefreshStopsOnCancel (the loop joins, the wire file is
//	its only caller).
//
// Inputs: the resolver helpers of daemon_unix_conductor_lane_test.go and the
//
//	in-tree captured openai 401 (internal/providers/dispatch/testdata).
//
// Constraints: no network (a fake Transport), HOME and USERPROFILE in
//
//	t.TempDir(), no vault and no keychain (the credential source is a fake
//	and no key is stored). The 429 and the 200 bodies are CONSTRUCTED, not
//	captured: no live 429 exists in the tree (dispatch/testdata README).
//	Fixture write mode needs CASCADE_TESTKIT_UPDATE_GOLDEN=1 and refuses CI.
//
// SPORT: cmd/cascade daemon registrations (tests, P1-WID-08).
package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	providerdispatch "github.com/acamarata/cascade/internal/providers/dispatch"
	providerregistry "github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// CONSTRUCTED anthropic bodies (not captured), the dispatch tests' own.
const (
	wireQuota429 = `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`
	wireOK200    = `{"id":"m1","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
	widgetEmail  = "user@example.com"
)

var widgetWireNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

// widgetStepper is a runtime.Ticker the test drives; step returns once the
// loop is waiting again (it calls C() on every re-entry to its select).
type widgetStepper struct {
	ticks, idle chan struct{}
	stopped     atomic.Bool
}

func (s *widgetStepper) C() <-chan struct{} {
	select {
	case s.idle <- struct{}{}:
	default:
	}
	return s.ticks
}
func (s *widgetStepper) Stop() { s.stopped.Store(true) }

// headerTransport answers every send with one status, headers and body.
type headerTransport struct {
	status  int
	headers map[string][]string
	body    string
}

func (h headerTransport) Send(context.Context, string, string, map[string]string, []byte) (int, map[string][]string, io.ReadCloser, error) {
	return h.status, h.headers, io.NopCloser(strings.NewReader(h.body)), nil
}

// widgetWire is the status-widget registration built from the production
// registry entry over a temp CASCADE_HOME, with a second providers.db handle.
type widgetWire struct {
	t       *testing.T
	w       *daemonWiring
	clock   *runtime.FixedClock
	reg     *providerregistry.Registry
	dataDir string
	ticker  *widgetStepper
	cancel  context.CancelFunc
}

func newWidgetWire(t *testing.T) *widgetWire {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	paths, err := runtime.NewPathProvider(func(k string) string {
		if k == "CASCADE_HOME" {
			return filepath.Join(home, "cascade")
		}
		return ""
	}, func() (string, error) { return home, nil })
	if err != nil {
		t.Fatalf("NewPathProvider: %v", err)
	}
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	clock := runtime.NewFixedClock(widgetWireNow)
	store := storetest.NewMemStore()
	ctx, cancel := context.WithCancel(context.Background())
	ww := &widgetWire{t: t, clock: clock, dataDir: paths.DataDir(), cancel: cancel,
		ticker: &widgetStepper{ticks: make(chan struct{}), idle: make(chan struct{}, 1)}}
	ww.w = &daemonWiring{Ctx: ctx, Registry: rpc.NewRegistry(), Manifest: daemon.NewManifest(nil, clock), Clock: clock,
		Store: store, Bus: events.New(store, clock), Paths: paths}
	ww.w.Connections = registerStatusHandler(ww.w.Registry, clock, ww.w.Manifest, daemon.Settings{})
	prev := statusWidgetTicker
	statusWidgetTicker = func() runtime.Ticker { return ww.ticker }
	t.Cleanup(func() { statusWidgetTicker = prev })
	t.Cleanup(func() { cancel(); ww.w.Manifest.Wait(); _ = ww.w.Bus.Close() })
	if err := statusWidgetRegistration(t).Wire(ww.w); err != nil {
		t.Fatalf("status-widget Wire: %v", err)
	}
	reg, closeReg := openLaneRegistry(t, ww.dataDir, clock)
	t.Cleanup(closeReg)
	ww.reg = reg
	return ww
}

// statusWidgetRegistration returns the production "status-widget" entry.
func statusWidgetRegistration(t *testing.T) daemonRegistration {
	t.Helper()
	for _, r := range daemonRegistrations {
		if r.Name == "status-widget" {
			return r
		}
	}
	t.Fatal("no status-widget daemon registration")
	return daemonRegistration{}
}

// seedProvider stores a key provider of driver with its one lane (none when
// pool and state are empty).
func (ww *widgetWire) seedProvider(name string, driver providerregistry.DriverKind, state providerregistry.LaneState, pool string) {
	ww.t.Helper()
	rec := providerregistry.ProviderRecord{Name: name, Driver: driver, BaseURL: "https://example.invalid/v1", Auth: providerregistry.AuthKey,
		AuthRef: providerregistry.VaultKeyRef("provider." + name + ".key"), AccountKind: providerregistry.AccountPersonal,
		Tier: providerregistry.TierMid, HealthStatus: providerregistry.HealthUnknown}
	if err := ww.reg.AddProvider(context.Background(), rec); err != nil {
		ww.t.Fatalf("AddProvider %q: %v", name, err)
	}
	if state == "" {
		return
	}
	lane := providerregistry.LaneRecord{LaneName: name, ProviderName: name, Weight: 1, PoolMembership: pool, Capacity: providerregistry.CapacityAPICredit, State: state}
	if err := ww.reg.UpsertLane(context.Background(), lane); err != nil {
		ww.t.Fatalf("UpsertLane %q: %v", name, err)
	}
}

// call runs one Chat on name's lane through the production resolver over the
// second handle, sending through tr.
func (ww *widgetWire) call(name string, tr headerTransport) error {
	ww.t.Helper()
	res, err := providerdispatch.NewResolver(ww.reg, mapCredentials{"provider." + name + ".key": "cred-value"}, ww.clock, tr)
	if err != nil {
		ww.t.Fatalf("NewResolver: %v", err)
	}
	p, err := res.Resolve(context.Background(), provider.Selection{LaneID: name, Model: "test-model"})
	if err != nil {
		ww.t.Fatalf("Resolve %q: %v", name, err)
	}
	_, err = p.Chat(context.Background(), provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}}})
	return err
}

// seedEvidence stores the five providers and drives lanes A, B and C
// through the resolver: A a captured 401, B a 429 with Retry-After 7200, C a
// 200. D has no lane; the email-named one is seeded available.
func (ww *widgetWire) seedEvidence() map[string]string {
	ww.t.Helper()
	names := map[string]string{"A": "acct-a-pool", "B": "acct-b-limit", "C": "acct-c-ok", "D": "acct-d-nolane", "E": widgetEmail}
	ww.seedProvider(names["A"], providerregistry.DriverOpenAICompat, providerregistry.LaneStateAvailable, "pool-a")
	ww.seedProvider(names["B"], providerregistry.DriverAnthropic, providerregistry.LaneStateAvailable, "")
	ww.seedProvider(names["C"], providerregistry.DriverAnthropic, providerregistry.LaneStateUnknown, "")
	ww.seedProvider(names["D"], providerregistry.DriverOpenAICompat, "", "")
	ww.seedProvider(names["E"], providerregistry.DriverOpenAICompat, providerregistry.LaneStateAvailable, "")
	if err := ww.call(names["A"], headerTransport{status: 401, body: string(openai401(ww.t))}); !cascade.HasKind(err, cascade.KindPermissionDenied) {
		ww.t.Fatalf("A: %v, want the openai driver's KindPermissionDenied", err)
	}
	tr := headerTransport{status: 429, headers: map[string][]string{"Retry-After": {"7200"}}, body: wireQuota429}
	if err := ww.call(names["B"], tr); !cascade.HasKind(err, cascade.KindQuotaExhausted) {
		ww.t.Fatalf("B: %v, want KindQuotaExhausted", err)
	}
	if err := ww.call(names["C"], headerTransport{status: 200, body: wireOK200}); err != nil {
		ww.t.Fatalf("C: %v", err)
	}
	return names
}

// snapshot dispatches status.widget through the wired registry.
func (ww *widgetWire) snapshot() capacity.WidgetSnapshot {
	ww.t.Helper()
	res, errObj := ww.w.Registry.Dispatch(context.Background(), &rpc.Request{JSONRPC: "2.0", Method: daemon.MethodStatusWidget, ID: json.RawMessage(`1`)})
	if errObj != nil {
		ww.t.Fatalf("status.widget: %+v", errObj)
	}
	return res.(capacity.WidgetSnapshot)
}

func rowByRef(t *testing.T, snap capacity.WidgetSnapshot, name string) capacity.WidgetRow {
	t.Helper()
	for _, r := range snap.Rows {
		if r.Ref == capacity.WidgetRef(name) {
			return r
		}
	}
	t.Fatalf("no row for %q in %+v", name, snap.Rows)
	return capacity.WidgetRow{}
}

func TestStatusWidgetRowsFromProviderEvidence(t *testing.T) {
	ww := newWidgetWire(t)
	n := ww.seedEvidence()
	snap := ww.snapshot()
	if len(snap.Rows) != 5 {
		t.Fatalf("rows = %d, want 5 (one per provider in providers.db): %+v", len(snap.Rows), snap.Rows)
	}
	a, b := rowByRef(t, snap, n["A"]), rowByRef(t, snap, n["B"])
	if a.State != "auth-required" || !a.ReauthRequired {
		t.Errorf("A after a 401 = %+v, want auth-required with reauth_required", a)
	}
	if b.State != "exhausted" || b.FiveHour.ResetsIn == nil || *b.FiveHour.ResetsIn != 7200*time.Second || b.SevenDay.ResetsIn != nil {
		t.Errorf("B after a 429 with Retry-After 7200 = %+v, want exhausted, five_hour 7200s, seven_day null", b)
	}
	for who, want := range map[string]string{"C": "available", "D": "unknown"} {
		if got := rowByRef(t, snap, n[who]); got.State != want || got.ReauthRequired || got.FiveHour.ResetsIn != nil {
			t.Errorf("%s = %+v, want %s without reauth or a reset", who, got, want)
		}
	}
	e := rowByRef(t, snap, widgetEmail)
	if !strings.HasPrefix(e.Ref, "ref-") || len(e.Ref) != 16 || e.Label != "redacted" {
		t.Errorf("email-named row ref %q label %q, want ref-<12 hex> and redacted", e.Ref, e.Label)
	}
	for _, r := range snap.Rows {
		if r.FiveHour.UtilizationPct != nil || r.SevenDay.UtilizationPct != nil {
			t.Errorf("row %q carries a utilization with no quota source: %+v", r.Ref, r)
		}
	}
}

func TestStatusWidgetRefreshStopsOnCancel(t *testing.T) {
	ww := newWidgetWire(t)
	ww.ticker.waitIdle(t)
	if sub := subsystem(t, ww.w, daemon.StatusWidgetRefreshSubsystem); sub.State != daemon.SubsystemRunning {
		t.Fatalf("status-widget-refresh = %+v, want running", sub)
	}
	ww.cancel()
	done := make(chan struct{})
	go func() { ww.w.Manifest.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Manifest.Wait did not return after daemonWiring.Ctx was cancelled")
	}
	if !ww.ticker.stopped.Load() {
		t.Error("the loop returned without stopping its ticker")
	}
	if sub := subsystem(t, ww.w, daemon.StatusWidgetRefreshSubsystem); sub.State != daemon.SubsystemSkipped || sub.Detail != "stopped" {
		t.Errorf("after cancel status-widget-refresh = %+v, want skipped stopped", sub)
	}
	assertOnlyWireCallsLoop(t)
}

func (s *widgetStepper) waitIdle(t *testing.T) {
	t.Helper()
	select {
	case <-s.idle:
	case <-time.After(10 * time.Second):
		t.Fatal("the refresh loop never reached its select")
	}
}
