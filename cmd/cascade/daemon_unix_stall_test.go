//go:build !windows

// Purpose: the supervision-stall registration over a test-built
//
//	daemonWiring: lifecycle under the run context, the construction-error
//	and lost-subscription paths, the idempotent capability, and the exact
//	approval request the human rung files. The end-to-end ladder is in
//	wire_context_drain_test.go.
//
// Constraints: in-memory store, real bus, real policy wiring (wirePolicy),
//
//	a mutex clock and a test-driven ticker; HOME, USERPROFILE and
//	CASCADE_HOME in t.TempDir(); no network.
//
// SPORT: cmd/cascade daemon stall supervision (tests, P1-SUP-04).
package main

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"
)

var stallNow = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

// stallClock is a runtime.Clock the test advances while the detector reads it.
type stallClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *stallClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *stallClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// stallRig is one daemon wiring with the stall registration's collaborators.
type stallRig struct {
	w      *daemonWiring
	clock  *stallClock
	ticker *widgetStepper
	cancel context.CancelFunc
}

// newStallRig builds a wiring over store (a fresh one when nil) whose config
// carries extra.
func newStallRig(t *testing.T, store provider.Store, extra map[string]interface{}) *stallRig {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CASCADE_HOME", home)
	if store == nil {
		store = storetest.NewMemStore()
	}
	clock := &stallClock{now: stallNow}
	ctx, cancel := context.WithCancel(context.Background())
	cfg := &runtime.Config{Extra: extra}
	pol, err := wirePolicy(ctx, store, clock, cfg, auditTestPaths(t))
	if err != nil {
		t.Fatalf("wirePolicy: %v", err)
	}
	r := &stallRig{clock: clock, cancel: cancel, ticker: &widgetStepper{ticks: make(chan struct{}), idle: make(chan struct{}, 1)}}
	r.w = &daemonWiring{Ctx: ctx, Registry: rpc.NewRegistry(), Manifest: daemon.NewManifest(nil, clock), Clock: clock,
		Store: store, Bus: events.New(store, clock), Paths: doctorTestPaths(t), Deps: daemonRuntime{Config: cfg, Policy: pol}}
	t.Cleanup(func() { cancel(); r.w.Manifest.Wait(); _ = r.w.Bus.Close() })
	return r
}

// rt is the injected runtime over bus.
func (r *stallRig) rt(bus supervision.StallBus) stallRuntime {
	return stallRuntime{bus: bus, newTicker: func(time.Duration) runtime.Ticker { return r.ticker }}
}

// state reports name's manifest entry.
func (r *stallRig) state(name string) (daemon.SubsystemStatus, bool) {
	for _, s := range r.w.Manifest.Snapshot() {
		if s.Name == name {
			return s, true
		}
	}
	return daemon.SubsystemStatus{}, false
}

// waitState waits for name to reach want.
func (r *stallRig) waitState(t *testing.T, name string, want daemon.SubsystemState) daemon.SubsystemStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s, ok := r.state(name); ok && s.State == want {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	s, _ := r.state(name)
	t.Fatalf("%s = %+v, never reached %v", name, s, want)
	return s
}

func TestWireStallDetectorStartsAndStopsWithRunContext(t *testing.T) {
	r := newStallRig(t, nil, nil)
	if reg := registrationNamed(t, "supervision-stall"); reg.Phase != phaseSupervised || reg.Order != 50 || reg.Wire == nil {
		t.Fatalf("supervision-stall registration = %+v, want phaseSupervised order 50", reg)
	}
	if err := wireStallDetectorWith(r.w, r.rt(r.w.Bus)); err != nil {
		t.Fatalf("Wire: %v", err)
	}
	for _, name := range []string{stallSubsystem, stallPollSubsystem} {
		if s, ok := r.state(name); !ok || s.State != daemon.SubsystemRunning {
			t.Fatalf("%s = %+v, want running", name, s)
		}
	}
	r.ticker.waitIdle(t)
	r.cancel()
	joined := make(chan struct{})
	go func() { r.w.Manifest.Wait(); close(joined) }()
	select {
	case <-joined:
	case <-time.After(10 * time.Second):
		t.Fatal("Manifest.Wait did not return after the run context ended")
	}
	for _, name := range []string{stallSubsystem, stallPollSubsystem} {
		if s, _ := r.state(name); s.State != daemon.SubsystemSkipped || s.Detail != "stopped" {
			t.Errorf("after cancel %s = %+v, want skipped stopped (joined by Wait)", name, s)
		}
	}
	if !r.ticker.stopped.Load() {
		t.Error("the poll loop returned without stopping its ticker")
	}
}

func TestStallBuildErrorRecordedFailed(t *testing.T) {
	bad := map[string]interface{}{"fleet": map[string]interface{}{"supervision": map[string]interface{}{"stall_threshold": "soon"}}}
	r := newStallRig(t, nil, bad)
	if err := wireStallDetectorWith(r.w, r.rt(r.w.Bus)); err != nil {
		t.Fatalf("a construction error failed the wiring (the daemon would not boot): %v", err)
	}
	s, _ := r.state(stallSubsystem)
	if s.State != daemon.SubsystemError || !strings.Contains(s.Detail, "stall_threshold") {
		t.Fatalf("supervision.stall = %+v, want error naming stall_threshold", s)
	}
	if _, ok := r.state(stallPollSubsystem); ok {
		t.Error("the poll loop started for a detector that was never built")
	}
	if r.w.Registry.Registered(daemon.ContextSliceMethod) {
		t.Error("context.slice was re-bound with a drain although no detector exists")
	}

	r2 := newStallRig(t, nil, nil)
	r2.w.Deps.Policy = nil
	if err := wireStallDetectorWith(r2.w, r2.rt(r2.w.Bus)); err != nil {
		t.Fatalf("missing policy wiring failed the wiring: %v", err)
	}
	if s, _ := r2.state(stallSubsystem); s.State != daemon.SubsystemError {
		t.Fatalf("without a policy wiring supervision.stall = %+v, want error", s)
	}
}

func TestStallSubscriptionLossRecordedFailed(t *testing.T) {
	r := newStallRig(t, nil, nil)
	if err := registerStallCapability(r.w.Ctx, r.w.Deps.Policy.Registry); err != nil {
		t.Fatal(err)
	}
	parts, err := buildStall(r.w)
	if err != nil {
		t.Fatalf("buildStall: %v", err)
	}
	subBus := events.New(storetest.NewMemStore(), r.clock)
	startStall(r.w, r.rt(subBus), parts)
	deadline := time.Now().Add(10 * time.Second)
	for !parts.detector.Alive() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !parts.detector.Alive() {
		t.Fatal("the detector never subscribed")
	}
	_ = subBus.Close()

	failed := r.waitState(t, stallSubsystem, daemon.SubsystemError)
	if !strings.Contains(failed.Detail, "subscription lost") {
		t.Errorf("supervision.stall detail = %q, want the lost-subscription error", failed.Detail)
	}
	r.waitState(t, stallPollSubsystem, daemon.SubsystemSkipped)
	items, err := parts.attention.ListInScopes(context.Background(), []supervision.ScopeRef{{Kind: scope.ScopeKindGlobal}}, supervision.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Kind != supervision.KindError || items[0].SourceRef != stallFailureSource {
		t.Fatalf("attention = %+v, want one error item for %s", items, stallFailureSource)
	}
	pending, err := r.w.Deps.Policy.Queue.GetPending(context.Background())
	if err != nil || len(pending) != 0 {
		t.Fatalf("approvals after the loss = %+v (%v), want none", pending, err)
	}
}

func TestStallCapabilityRegisteredIdempotent(t *testing.T) {
	store := storetest.NewMemStore()
	r := newStallRig(t, store, nil)
	if err := wireStallDetectorWith(r.w, r.rt(r.w.Bus)); err != nil {
		t.Fatalf("first start: %v", err)
	}
	if err := registerStallCapability(r.w.Ctx, r.w.Deps.Policy.Registry); err != nil {
		t.Fatalf("re-adding the identical capability to the same registry: %v", err)
	}
	r.cancel()
	r.w.Manifest.Wait()

	second := newStallRig(t, store, nil)
	if err := wireStallDetectorWith(second.w, second.rt(second.w.Bus)); err != nil {
		t.Fatalf("second daemon start over the same store: %v", err)
	}
	if s, _ := second.state(stallSubsystem); s.State != daemon.SubsystemRunning {
		t.Fatalf("second start supervision.stall = %+v, want running", s)
	}

	clash := policy.NewMemoryRegistry()
	other := policy.Capability{Name: stallCapability, Desc: "something else", DefaultPolicy: policy.ClassLocalDev}
	if err := clash.Add(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	if err := registerStallCapability(context.Background(), clash); err == nil {
		t.Fatal("a different capability under the same name was accepted")
	}
}

// recordingQueue captures the EnqueueRequest the requester sends.
type recordingQueue struct {
	policy.ApprovalQueue
	got []policy.EnqueueRequest
}

func (q *recordingQueue) Enqueue(_ context.Context, req policy.EnqueueRequest) (policy.EnqueueResult, error) {
	q.got = append(q.got, req)
	return policy.EnqueueResult{RequestID: "req-1"}, nil
}

func TestStallApprovalRequesterEnqueuesL2(t *testing.T) {
	q := &recordingQueue{}
	ev := supervision.StallEvent{SessionID: "s1", StallKind: "idle", StalledSince: stallNow.UnixMilli(), ElapsedSeconds: 600}
	id, err := stallApprovalRequester{queue: q}.RequestApproval(context.Background(), "s1", ev)
	if err != nil || id != "req-1" || len(q.got) != 1 {
		t.Fatalf("RequestApproval = %q, %v (%d requests), want req-1 from one Enqueue", id, err, len(q.got))
	}
	req := q.got[0]
	wantParams := `{"session_id":"s1","stall_kind":"idle","stalled_since":` + strconv.FormatInt(stallNow.UnixMilli(), 10) + `,"elapsed_seconds":600}`
	if req.Subject != schedulerSubject() || req.Capability != "fleet.session.stall" || req.Verb != "" ||
		req.Level != policy.L2 || req.Action != "fleet.session.stall s1" || string(req.Params) != wantParams ||
		req.Summary != "s1 stalled (idle) since 2026-10-12T09:00:00Z" {
		t.Fatalf("EnqueueRequest = %+v (params %s)", req, req.Params)
	}
	unknown := supervision.StallEvent{SessionID: "s2", StallKind: "unknown"}
	if got := stallSummary("s2", unknown); got != "s2 stalled (unknown) since unknown" {
		t.Errorf("summary with no since = %q", got)
	}

	r := newStallRig(t, nil, nil)
	if err := registerStallCapability(r.w.Ctx, r.w.Deps.Policy.Registry); err != nil {
		t.Fatal(err)
	}
	// A harness session id is a UUID: the contract's full summary would be
	// 78 bytes, past the queue's 64-byte display limit, so the id shortens.
	const uuid = "6fab1b60-b545-4cbb-9c39-0b51b24c5db6"
	if got := stallSummary(uuid, ev); got != "6fab1b60-b545-4cbb-... stalled (idle) since 2026-10-12T09:00:00Z" || len(got) != approvalTextLimit {
		t.Errorf("UUID summary = %q (%d bytes)", got, len(got))
	}
	if got := stallAction(uuid); got != "fleet.session.stall "+uuid {
		t.Errorf("UUID action = %q, want the full id (it fits)", got)
	}
	if got := fitID("abcdef", 5); got != "ab..." {
		t.Errorf("fitID = %q", got)
	}
	realID, err := stallApprovalRequester{queue: r.w.Deps.Policy.Queue}.RequestApproval(context.Background(), uuid, ev)
	if err != nil || realID == "" {
		t.Fatalf("the real queue refused a UUID session's request: %q, %v", realID, err)
	}
	if _, err := (stallApprovalRequester{}).RequestApproval(context.Background(), "s1", ev); err == nil {
		t.Error("a requester with no queue reported success")
	}
}
