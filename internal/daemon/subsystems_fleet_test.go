package daemon

// Purpose: proves NewFleetRuntime builds the one admission controller over a
// supervised sampler, reads [governor], reports the posture row and stops
// cleanly. No sleeps: fake tickers drive the sampler and the row refresher.
// SPORT: internal/daemon (ADD, P1-CAP-12).

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

const fleetTestBound = 30 * time.Second

// fleetFakeTicker is a manual runtime.Ticker; polled reports each C() call.
type fleetFakeTicker struct {
	c      chan struct{}
	polled chan struct{}
}

func newFleetFakeTicker() *fleetFakeTicker {
	return &fleetFakeTicker{c: make(chan struct{}), polled: make(chan struct{}, 64)}
}

func (f *fleetFakeTicker) C() <-chan struct{} {
	select {
	case f.polled <- struct{}{}:
	default:
	}
	return f.c
}

func (f *fleetFakeTicker) Stop() {}

// awaitPoll waits for the next C() call.
func (f *fleetFakeTicker) awaitPoll(t *testing.T) {
	t.Helper()
	select {
	case <-f.polled:
	case <-time.After(fleetTestBound):
		t.Fatal("the sampler never asked for its tick channel")
	}
}

// tick delivers one tick and returns once the sampler finished handling it.
func (f *fleetFakeTicker) tick(t *testing.T) {
	t.Helper()
	select {
	case f.c <- struct{}{}:
	case <-time.After(fleetTestBound):
		t.Fatal("the sampler never received the tick")
	}
	f.awaitPoll(t)
}

type fleetRig struct {
	deps      FleetRuntimeDeps
	ticker    *fleetFakeTicker
	rowTicker *fleetFakeTicker
	clock     *testkit.FrozenClock
	cancel    context.CancelFunc
	ctx       context.Context
}

// newFleetRig builds valid deps over a temp root holding configTOML.
func newFleetRig(t *testing.T, configTOML string) *fleetRig {
	t.Helper()
	paths := fakePathsFor(t, "")
	if configTOML != "" {
		if err := os.WriteFile(paths.ConfigPath(), []byte(configTOML), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
	}
	clock := testkit.NewFrozenClock(time.Unix(10_000, 0))
	store := storetest.NewMemStore()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ft, rowT := newFleetFakeTicker(), newFleetFakeTicker()
	savedTicker, savedCollect := newRowTicker, samplerCollect
	newRowTicker = func(time.Duration) runtime.Ticker { return rowT }
	// A fixed host reading: a real one needs two ticks on linux (no CPU baseline on the first).
	samplerCollect = func() (governor.ResourceSnapshot, error) {
		return governor.ResourceSnapshot{MemTotalBytes: 16 << 30, MemUsedBytes: 8 << 30}, nil
	}
	t.Cleanup(func() { newRowTicker, samplerCollect = savedTicker, savedCollect })
	return &fleetRig{ctx: ctx, cancel: cancel, ticker: ft, rowTicker: rowT, clock: clock, deps: FleetRuntimeDeps{
		Paths: paths, Clock: clock, Manifest: NewManifest(nil, clock), Bus: events.New(store, clock),
		Config: &runtime.Config{}, Store: store, Binding: fakeBindingBackend{}, SamplerTicker: ft,
	}}
}

// start builds the runtime and waits for both first polls.
func (g *fleetRig) start(t *testing.T) *FleetRuntime {
	t.Helper()
	r, err := NewFleetRuntime(g.ctx, g.deps)
	if err != nil {
		t.Fatalf("NewFleetRuntime: %v", err)
	}
	t.Cleanup(func() { g.cancel(); _ = r.Close() })
	g.ticker.awaitPoll(t)
	g.rowTicker.awaitPoll(t)
	return r
}

func (g *fleetRig) awaitJoin(t *testing.T) {
	t.Helper()
	joined := make(chan struct{})
	go func() { g.deps.Manifest.Wait(); close(joined) }()
	select {
	case <-joined:
	case <-time.After(fleetTestBound):
		t.Fatal("Manifest.Wait never returned: the sampler was not stopped by the run context or Close")
	}
}

func (g *fleetRig) row(name string) SubsystemStatus {
	for _, s := range g.deps.Manifest.Snapshot() {
		if s.Name == name {
			return s
		}
	}
	return SubsystemStatus{}
}

func TestNewFleetRuntimeComposesAdmission(t *testing.T) {
	g := newFleetRig(t, "")
	r := g.start(t)
	// Fail-closed probe: a sampler that never ticks leaves the posture stale and Admit refusing.
	_, err := r.Admission().Admit(context.Background(), governor.AdmissionRequest{Kind: "generic"})
	if p, _ := r.Admission().Posture(); p != governor.PostureStale || !governor.IsSignalRefusal(err) || r.Admission().QueueDepth() != 0 {
		t.Fatalf("before any tick: posture %q, Admit = %v (queue %d), want stale and a signal refusal", p, err, r.Admission().QueueDepth())
	}
	g.ticker.tick(t)
	if p, _ := r.Admission().Posture(); p == governor.PostureStale {
		t.Fatal("posture after one tick is still stale: the sampler was not started on the run context")
	}
	// Literal names: status.get clients read them.
	for _, name := range []string{"governor-sampler", "admission-posture", "admission"} {
		if got := g.row(name); got.State != SubsystemRunning {
			t.Fatalf("row %q = %+v, want running", name, got)
		}
	}
}

func TestNewFleetRuntimeRefusesNilDependency(t *testing.T) {
	for _, name := range []string{"Paths", "Clock", "Manifest", "Bus", "Config", "Store", "Binding"} {
		g := newFleetRig(t, "")
		reflect.ValueOf(&g.deps).Elem().FieldByName(name).SetZero()
		r, err := NewFleetRuntime(g.ctx, g.deps)
		if r != nil || !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(err.Error(), name) {
			t.Errorf("nil %s: runtime=%v err=%v, want nil runtime and KindInvalidInput naming %s", name, r, err, name)
		}
	}
}

func TestFleetRuntimeAdmissionConfig(t *testing.T) {
	// No [governor] table: an unpinned preset on a zero snapshot is the minimal envelope.
	if got := newFleetRig(t, "").start(t).Admission().EnforcedCeiling(); got != governor.DefaultMaxInflight/2 {
		t.Fatalf("default ceiling = %d, want %d", got, governor.DefaultMaxInflight/2)
	}
	open := newFleetRig(t, "[governor.admission]\nmax_inflight = 3\nmem_threshold = 1.0\nswap_threshold = 1.0\n")
	r := open.start(t)
	if got := r.Admission().EnforcedCeiling(); got != 3 {
		t.Fatalf("configured ceiling = %d, want 3 (max_inflight)", got)
	}
	open.ticker.tick(t)
	permit, err := r.Admission().Admit(context.Background(), governor.AdmissionRequest{Kind: "generic"})
	if err != nil {
		t.Fatalf("Admit with thresholds at 1.0: %v", err)
	}
	permit.Release()
	// mem_threshold reaches the controller: a tiny threshold queues the next request.
	tight := newFleetRig(t, "[governor.admission]\nmem_threshold = 0.0001\n")
	tr := tight.start(t)
	tight.ticker.tick(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := tr.Admission().Admit(ctx, governor.AdmissionRequest{Kind: "generic"}); done <- err }()
	deadline := time.After(fleetTestBound)
	for tr.Admission().QueueDepth() != 1 {
		select {
		case err := <-done:
			t.Fatalf("Admit returned %v instead of queueing under mem_threshold=0.0001", err)
		case <-deadline:
			t.Fatal("the request never queued under mem_threshold=0.0001")
		default:
		}
	}
	cancel()
	<-done
}

func TestFleetRuntimeMalformedGovernorRefuses(t *testing.T) {
	g := newFleetRig(t, "[governor\npreset = \n")
	_, _, wantErr := governor.Load(context.Background(), g.deps.Paths.ConfigPath(), governor.ResourceSnapshot{})
	if wantErr == nil {
		t.Fatal("fixture: governor.Load accepted the malformed table")
	}
	r, err := NewFleetRuntime(g.ctx, g.deps)
	wantKind, _ := cascade.KindOf(wantErr)
	// errors.Is on a cascade error compares Kind only, so check the message too.
	if r != nil || err == nil || !cascade.HasKind(err, wantKind) || !strings.Contains(err.Error(), wantErr.Error()) || !strings.Contains(err.Error(), `step "admission"`) {
		t.Fatalf("NewFleetRuntime = %v, %v; want the governor.Load error", r, err)
	}
	if got := g.row(fleetSamplerSubsystem); got.Name != "" {
		t.Fatalf("a failed step still started the sampler: %+v", got)
	}
}

// TestFleetRuntimeRowAgesWithoutSamples: a sampler wedged in a collection
// never returns to C(); the row refresher still shows the posture ageing.
func TestFleetRuntimeRowAgesWithoutSamples(t *testing.T) {
	g := newFleetRig(t, "")
	g.start(t)
	g.ticker.tick(t)
	if got := g.row(fleetAdmissionSubsystem).Detail; got == "posture=stale" {
		t.Fatalf("row after a sample = %q, want a memory posture", got)
	}
	g.clock.Advance(time.Minute)
	g.rowTicker.tick(t)
	if got := g.row(fleetAdmissionSubsystem); got.State != SubsystemRunning || got.Detail != "posture=stale" {
		t.Fatalf("row after the sample aged = %+v, want running posture=stale", got)
	}
}

func TestFleetRuntimeJoinsSamplerAndCloseDrains(t *testing.T) {
	g := newFleetRig(t, "")
	r := g.start(t)
	g.ticker.tick(t)
	g.cancel()
	g.awaitJoin(t)
	if got := g.row(fleetSamplerSubsystem); got.State != SubsystemSkipped || got.Detail != supervisedStoppedDetail {
		t.Fatalf("sampler row = %+v, want skipped/stopped", got)
	}
	gone, stop := context.WithCancel(context.Background())
	stop()
	permit, err := r.Admission().Admit(gone, governor.AdmissionRequest{Kind: "generic"})
	permit.Release()
	if governor.IsDraining(err) {
		t.Fatalf("Admit before Close = %v, want the gate open", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := r.Admission().Admit(context.Background(), governor.AdmissionRequest{Kind: "generic"}); !governor.IsDraining(err) {
		t.Fatalf("Admit after Close = %v, want draining", err)
	}
}

// TestFleetRuntimeCloseRunsClosersInReverse: Close alone (the run context
// still live) stops the sampler; a later step's teardowns run newest first,
// their errors are joined, and a second Close runs nothing.
func TestFleetRuntimeCloseRunsClosersInReverse(t *testing.T) {
	g := newFleetRig(t, "")
	r := g.start(t)
	var order []string
	boom := cascade.New(cascade.KindInternal, "closer two failed")
	r.addCloser(func() error { order = append(order, "one"); return nil })
	r.addCloser(func() error { order = append(order, "two"); return boom })
	err := r.Close()
	g.awaitJoin(t)
	if got := strings.Join(order, ","); got != "two,one" {
		t.Fatalf("closers ran %q, want two,one", got)
	}
	if err == nil || !strings.Contains(err.Error(), "closer two failed") {
		t.Fatalf("Close = %v, want the joined closer error", err)
	}
	if again := r.Close(); again == nil || len(order) != 2 {
		t.Fatalf("second Close = %v after %d closer runs, want the first result, no rerun", again, len(order))
	}
}
