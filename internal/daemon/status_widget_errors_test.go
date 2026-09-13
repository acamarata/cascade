package daemon

// Purpose (this file): branch/error-path coverage for status.widget
// (status_widget.go, status_widget_jobs.go, status_widget_sse.go) that
// status_widget_test.go's and status_widget_privacy_test.go's happy-path
// tests never drive. Added while fixing the CI coverage-floor gate
// (internal/daemon measured 84.29% against an 85% floor after
// P1-E38-W8-S74-T1 landed): these are genuinely uncovered branches in
// that ticket's own new code, covered with real assertions rather than by
// lowering the floor. See journals/FIX-windows-widget-handles-and-daemon-
// coverage.md for the full before/after measurement and which lines this
// file does and does not reach.
//
// SPORT: daemon.status_widget (coverage follow-up).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// erroringNodeSource is a capacity.NodeSource that always fails --
// exercises capacitySnapshot's UpdateNodes error branch and, transitively,
// buildSnapshot's and statusWidgetHandler's error propagation.
type erroringNodeSource struct{}

func (erroringNodeSource) List() ([]nodes.DeviceRecord, error) {
	return nil, cascade.New(cascade.KindUnavailable, "erroringNodeSource: forced failure")
}

// erroringProviderSource is a capacity.ProviderSource that always fails on
// ListProviders -- exercises capacitySnapshot's UpdateProviders error
// branch (ListLanes is never reached; Compositor.UpdateProviders returns
// before calling it).
type erroringProviderSource struct{}

func (erroringProviderSource) ListProviders(context.Context) ([]registry.ProviderRecord, error) {
	return nil, cascade.New(cascade.KindUnavailable, "erroringProviderSource: forced failure")
}

func (erroringProviderSource) ListLanes(context.Context) ([]registry.LaneRecord, error) {
	return nil, nil
}

// spyEventBus is a minimal statusWidgetEventBus double recording whether
// Publish was invoked -- used to prove emitStatusWidgetChanged's error
// branches return BEFORE reaching the wire, not just that they return.
type spyEventBus struct{ published bool }

func (s *spyEventBus) Publish(context.Context, string, events.EventKind, string, []byte) (events.Event, error) {
	s.published = true
	return events.Event{}, nil
}

// TestResolveWidgetScope_ExplicitScope proves the p != nil branch
// (status_widget_test.go's TestStatusWidgetRPC and friends only ever
// dispatch with an absent scope param, so this branch was never taken).
func TestResolveWidgetScope_ExplicitScope(t *testing.T) {
	want := supervision.ScopeRef{Kind: scope.ScopeKindProject, ID: "p1"}
	if got := resolveWidgetScope(&want); got != want {
		t.Errorf("resolveWidgetScope(&want) = %+v, want %+v", got, want)
	}
}

// TestCapacitySnapshot_ProviderSourceError proves UpdateProviders' error
// return is propagated, not swallowed.
func TestCapacitySnapshot_ProviderSourceError(t *testing.T) {
	_, deps := setupStatusWidget(t, nil)
	deps.providerSrc = erroringProviderSource{}
	if _, err := deps.capacitySnapshot(context.Background()); err == nil {
		t.Fatal("capacitySnapshot with an erroring providerSrc: want an error, got nil")
	}
}

// TestStatusWidgetHandler_BuildSnapshotError drives the RPC dispatch path
// with an erroring nodeSrc, proving THREE links in one real request:
// capacitySnapshot's UpdateNodes error branch, buildSnapshot's error
// propagation, and statusWidgetHandler's own error return.
func TestStatusWidgetHandler_BuildSnapshotError(t *testing.T) {
	reg, deps := setupStatusWidget(t, nil)
	deps.nodeSrc = erroringNodeSource{}
	req := &rpc.Request{JSONRPC: "2.0", Method: MethodStatusWidget, ID: json.RawMessage(`1`)}
	if _, errObj := reg.Dispatch(context.Background(), req); errObj == nil {
		t.Fatal("Dispatch with an erroring nodeSrc: want an error, got nil")
	}
}

// TestStatusWidgetHandler_MalformedParams proves the params-unmarshal
// error branch: a params payload that cannot decode as statusWidgetParams
// must fail closed with a typed error, never a panic or a silent default.
func TestStatusWidgetHandler_MalformedParams(t *testing.T) {
	reg, _ := setupStatusWidget(t, nil)
	req := &rpc.Request{JSONRPC: "2.0", Method: MethodStatusWidget, ID: json.RawMessage(`1`), Params: json.RawMessage(`{`)}
	if _, errObj := reg.Dispatch(context.Background(), req); errObj == nil {
		t.Fatal("Dispatch with malformed params: want a typed error, got nil")
	}
}

// TestComposeFrom_ActiveJobsCountError proves composeFrom propagates an
// activeJobsCount failure instead of reporting a fabricated count.
func TestComposeFrom_ActiveJobsCountError(t *testing.T) {
	_, deps := setupStatusWidget(t, nil)
	deps.activeJobsCount = func(context.Context) (*int, error) {
		return nil, cascade.New(cascade.KindUnavailable, "forced failure")
	}
	if _, err := deps.composeFrom(context.Background(), capacity.FleetSnapshot{}, defaultWidgetScope()); err == nil {
		t.Fatal("composeFrom with an erroring activeJobsCount: want an error, got nil")
	}
}

// TestKnownStatusWidgetEventKind proves the predicate cmd/cascade's
// buildRPCServer combines into its SSE knownEventKind actually
// discriminates -- true for status.widget_changed, false for anything
// else.
func TestKnownStatusWidgetEventKind(t *testing.T) {
	if !KnownStatusWidgetEventKind(StatusWidgetChangedKind) {
		t.Error("KnownStatusWidgetEventKind(StatusWidgetChangedKind) = false, want true")
	}
	if KnownStatusWidgetEventKind(events.EventKind("something.else")) {
		t.Error("KnownStatusWidgetEventKind(unrelated kind) = true, want false")
	}
}

// TestEmitStatusWidgetChanged_NilBusIsGuarded proves the deps/bus
// nil-guard returns BEFORE deps.seq is ever advanced. seq.Add(1) happens
// after the guard and before the eventual bus.Publish call, so a removed
// guard would still advance seq (then panic on the nil-interface Publish)
// -- this assertion, not mere panic-absence, is what the guard's removal
// would break.
func TestEmitStatusWidgetChanged_NilBusIsGuarded(t *testing.T) {
	_, deps := setupStatusWidget(t, nil)
	before := deps.seq.Load()
	emitStatusWidgetChanged(context.Background(), deps, nil, nil)
	if got := deps.seq.Load(); got != before {
		t.Errorf("seq = %d after a nil-bus emit, want unchanged %d (nil-bus guard must return first)", got, before)
	}
}

// TestEmitStatusWidgetChanged_CapacitySnapshotErrorSkipsPublish proves the
// preSnap-nil recompute path's error branch returns before ever calling
// bus.Publish or advancing seq.
func TestEmitStatusWidgetChanged_CapacitySnapshotErrorSkipsPublish(t *testing.T) {
	_, deps := setupStatusWidget(t, nil)
	deps.nodeSrc = erroringNodeSource{}
	spy := &spyEventBus{}
	emitStatusWidgetChanged(context.Background(), deps, spy, nil)
	if spy.published {
		t.Error("Publish called despite a capacitySnapshot error")
	}
	if deps.seq.Load() != 0 {
		t.Errorf("seq = %d, want 0 (error branch must return before Add)", deps.seq.Load())
	}
}

// TestEmitStatusWidgetChanged_ComposeFromErrorSkipsPublish proves
// composeFrom's error branch (a fixed, non-nil preSnap skips
// capacitySnapshot entirely, isolating this from the nodeSrc test above)
// also returns before publishing.
func TestEmitStatusWidgetChanged_ComposeFromErrorSkipsPublish(t *testing.T) {
	_, deps := setupStatusWidget(t, nil)
	deps.activeJobsCount = func(context.Context) (*int, error) {
		return nil, cascade.New(cascade.KindUnavailable, "forced failure")
	}
	spy := &spyEventBus{}
	emitStatusWidgetChanged(context.Background(), deps, spy, &capacity.FleetSnapshot{})
	if spy.published {
		t.Error("Publish called despite a composeFrom error")
	}
}

// TestRegisterStatusWidgetHandler_NilStore proves the documented
// "a nil store registers nothing" degradation: nil deps, nil error, and
// the method absent from the registry.
func TestRegisterStatusWidgetHandler_NilStore(t *testing.T) {
	reg := rpc.NewRegistry()
	clock := runtime.NewFixedClock(time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC))
	deps, err := RegisterStatusWidgetHandler(reg, nil, clock, nil, fakePaths{root: t.TempDir()}, func() bool { return false })
	if err != nil {
		t.Fatalf("RegisterStatusWidgetHandler(nil store): %v", err)
	}
	if deps != nil {
		t.Errorf("deps = %+v, want nil for a nil store", deps)
	}
	if reg.Registered(MethodStatusWidget) {
		t.Error("status.widget registered despite a nil store")
	}
}

// TestRegisterStatusWidgetHandler_JobsStoreOpenError proves
// openWidgetJobsStore's failure is propagated, not swallowed: DataDir is
// seeded as a plain FILE (not a directory), so opening
// "<DataDir>/cascade.db" genuinely fails when the sqlite driver connects
// -- a real filesystem error, not a fabricated one.
func TestRegisterStatusWidgetHandler_JobsStoreOpenError(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "data"), []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("seed blocking file: %v", err)
	}
	paths := fakePaths{root: root}
	clock := runtime.NewFixedClock(time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC))
	reg := rpc.NewRegistry()

	store := storetest.NewMemStore()
	deps, err := RegisterStatusWidgetHandler(reg, store, clock, nil, paths, func() bool { return false })
	if err == nil {
		t.Fatal("RegisterStatusWidgetHandler over an unusable DataDir: want an error, got nil")
	}
	if deps != nil {
		t.Errorf("deps = %+v, want nil on error", deps)
	}
}

// TestRegisterStatusWidgetHandler_NilBusPushDoesNotAdvanceSeq proves the
// bus==nil registration branch actually wires a real, invokable onPush
// closure (not a no-op left uncalled): a real attention Push through
// deps.attention triggers attentionForwardBus.Publish -> onPush ->
// emitStatusWidgetChanged(ctx, deps, nil, nil), which must hit the
// nil-bus guard and leave seq unchanged.
func TestRegisterStatusWidgetHandler_NilBusPushDoesNotAdvanceSeq(t *testing.T) {
	_, deps := setupStatusWidget(t, nil)
	before := deps.seq.Load()
	if _, err := deps.attention.Push(context.Background(), supervision.AttentionItem{
		Kind: supervision.KindStall, SourceRef: "session-nil-bus", ScopeRef: defaultWidgetScope(),
	}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if got := deps.seq.Load(); got != before {
		t.Errorf("seq = %d after a push through a nil-bus registration, want unchanged %d", got, before)
	}
}

// twoPageJobsCounter proves countActiveJobs' cursor-advance line
// ("cursor = next"): the Leased state's first page reports a non-empty
// next cursor, and the SECOND call must carry that exact cursor value --
// a fake that only counted calls (rather than checking the value) would
// not detect a dropped assignment.
type twoPageJobsCounter struct {
	t             *testing.T
	firstCallSeen bool
}

func (c *twoPageJobsCounter) ListJobs(_ context.Context, filter jobs.JobFilter) ([]jobs.Job, string, error) {
	if filter.State != jobs.JobStateLeased {
		return []jobs.Job{{}}, "", nil
	}
	if !c.firstCallSeen {
		c.firstCallSeen = true
		return []jobs.Job{{}}, "page-2", nil
	}
	if filter.Cursor != "page-2" {
		c.t.Errorf("second ListJobs(Leased) Cursor = %q, want %q (cursor must advance)", filter.Cursor, "page-2")
	}
	return []jobs.Job{{}}, "", nil
}

func TestCountActiveJobs_Paginates(t *testing.T) {
	counter := &twoPageJobsCounter{t: t}
	total, err := countActiveJobs(context.Background(), counter)
	if err != nil {
		t.Fatalf("countActiveJobs: %v", err)
	}
	// Leased: 2 pages of 1 job each = 2; Running: 1 page of 1 job = 1.
	if total != 3 {
		t.Errorf("total = %d, want 3 (Leased 2 pages + Running 1 page)", total)
	}
}

// erroringJobsCounter always fails -- proves countActiveJobs propagates a
// ListJobs error instead of reporting a partial, fabricated total.
type erroringJobsCounter struct{}

func (erroringJobsCounter) ListJobs(context.Context, jobs.JobFilter) ([]jobs.Job, string, error) {
	return nil, "", cascade.New(cascade.KindUnavailable, "erroringJobsCounter: forced failure")
}

func TestCountActiveJobs_PropagatesError(t *testing.T) {
	if _, err := countActiveJobs(context.Background(), erroringJobsCounter{}); err == nil {
		t.Fatal("countActiveJobs with an erroring store: want an error, got nil")
	}
}
