package daemon

// Purpose: drives RegisterScheduler and its consumer/translator
//
//	(subsystems_scheduler.go, subsystems_scheduler_events.go) directly —
//	the same-package unit-level complement to
//	daemon_unix_scheduler_dag_test.go's cross-process buildRPCServer
//	proof, covering the Subscribe-failure path, the lease-event
//	translation table, and the consumer loop's three exit routes that a
//	real-socket test cannot cheaply force.
//
// SPORT: internal/daemon (ADD, merge-fix for P1-E29-W6-S59-T5).

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// fakeBindingBackend is a deterministic nodes.ControllerBindingBackend for
// this file's tests.
type fakeBindingBackend struct {
	binding nodes.ControllerBinding
	ok      bool
	err     error
}

func (b fakeBindingBackend) Load() (nodes.ControllerBinding, bool, error) {
	return b.binding, b.ok, b.err
}

func newSchedulerTestBus() *events.Bus {
	return events.New(storetest.NewMemStore(), runtime.NewSystemClock())
}

// TestRegisterScheduler_ControllerRoleStartsAndConsumesRealEvent proves
// RegisterScheduler resolves RoleController (nil binding, the documented
// safe default), subscribes for real, and its background consumer reaches
// Scheduler.Advance for a real bus-published lease_acquired event without
// error -- the composition root behavior daemon_unix_scheduler_dag_test.go
// proves is actually mounted; this test proves what runs once it is.
func TestRegisterScheduler_ControllerRoleStartsAndConsumesRealEvent(t *testing.T) {
	bus := newSchedulerTestBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := NewManifest(nil, runtime.NewSystemClock())
	sched, err := m.RegisterScheduler(ctx, bus, runtime.NewSystemClock(), nil, "sched-test-controller", nil, nil, 0)
	if err != nil {
		t.Fatalf("RegisterScheduler: %v", err)
	}
	if sched == nil {
		t.Fatal("RegisterScheduler returned a nil Scheduler")
	}

	found := false
	for _, s := range m.Snapshot() {
		if s.Name == jobSchedulerSubsystem {
			found = true
			if s.State != SubsystemRunning {
				t.Fatalf("jobs.scheduler subsystem state = %v, want Running", s.State)
			}
		}
	}
	if !found {
		t.Fatalf("Manifest snapshot has no %q entry: %+v", jobSchedulerSubsystem, m.Snapshot())
	}

	payload, err := json.Marshal(map[string]any{"repo_id": "repo1", "scope_glob": "**", "holder": "job-1", "epoch": int64(1), "state": "held"})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if _, err := bus.Publish(context.Background(), jobs.LeaseEventNamespace, jobs.EventLeaseAcquired, "test", payload); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Give the consumer goroutine a chance to process the event, then
	// confirm the subsystem is still Running (a delta.Errors return would
	// have flipped it to Failed via RegisterScheduler's own goroutine).
	time.Sleep(50 * time.Millisecond)
	for _, s := range m.Snapshot() {
		if s.Name == jobSchedulerSubsystem && s.State != SubsystemRunning {
			t.Fatalf("jobs.scheduler subsystem state after real event = %v, want Running (detail: %s)", s.State, s.Detail)
		}
	}
}

// TestRegisterScheduler_SubscribeFailureRecordsFailed proves the
// Subscribe-conflict error path: two RegisterScheduler calls sharing one
// cursor name on the same namespace conflict (events.Bus's own documented
// refusal), and the second is recorded Failed, not silently dropped.
func TestRegisterScheduler_SubscribeFailureRecordsFailed(t *testing.T) {
	bus := newSchedulerTestBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := NewManifest(nil, runtime.NewSystemClock())
	if _, err := m.RegisterScheduler(ctx, bus, runtime.NewSystemClock(), nil, "sched-test-dup", nil, nil, 0); err != nil {
		t.Fatalf("first RegisterScheduler: %v", err)
	}
	if _, err := m.RegisterScheduler(ctx, bus, runtime.NewSystemClock(), nil, "sched-test-dup", nil, nil, 0); err == nil {
		t.Fatal("second RegisterScheduler with a duplicate cursor name = nil error, want a conflict")
	}

	failed := false
	for _, s := range m.Snapshot() {
		if s.Name == jobSchedulerSubsystem && s.State == SubsystemError {
			failed = true
		}
	}
	if !failed {
		t.Fatalf("Manifest snapshot never recorded a Failed jobs.scheduler entry: %+v", m.Snapshot())
	}
}

// TestRegisterScheduler_ResolveRoleErrorRecordsFailed proves a
// ControllerBindingBackend.Load failure is recorded Failed and returned,
// never silently defaulted to a role.
func TestRegisterScheduler_ResolveRoleErrorRecordsFailed(t *testing.T) {
	bus := newSchedulerTestBus()
	m := NewManifest(nil, runtime.NewSystemClock())
	boom := context.DeadlineExceeded
	_, err := m.RegisterScheduler(context.Background(), bus, runtime.NewSystemClock(), fakeBindingBackend{err: boom}, "sched-test-resolve-err", nil, nil, 0)
	if err == nil {
		t.Fatal("RegisterScheduler with a failing binding = nil error, want one")
	}
	for _, s := range m.Snapshot() {
		if s.Name == jobSchedulerSubsystem && s.State != SubsystemError {
			t.Fatalf("jobs.scheduler subsystem state = %v, want Error", s.State)
		}
	}
}

// TestDecodeSchedulerEvent covers the lease-bus-to-jobs.Event translation
// table: the two wired kinds, the three disclosed no-ops, and a malformed
// payload.
func TestDecodeSchedulerEvent(t *testing.T) {
	acquiredPayload, _ := json.Marshal(map[string]any{"repo_id": "r1", "scope_glob": "**", "holder": "job-a"})
	expiredPayload, _ := json.Marshal(map[string]any{"repo_id": "r1", "scope_glob": "**", "holder": "job-b"})

	cases := []struct {
		name    string
		ev      events.Event
		wantOK  bool
		wantErr bool
		check   func(t *testing.T, ev jobs.Event)
	}{
		{
			name: "lease acquired decodes to jobs.LeaseAcquired", wantOK: true,
			ev: events.Event{Kind: jobs.EventLeaseAcquired, Payload: acquiredPayload},
			check: func(t *testing.T, ev jobs.Event) {
				la, ok := ev.(jobs.LeaseAcquired)
				if !ok || la.JobID != "job-a" || la.LeaseID != "r1:**" {
					t.Fatalf("decoded = %#v, want LeaseAcquired{JobID:job-a,LeaseID:r1:**}", ev)
				}
			},
		},
		{
			name: "lease expired decodes to jobs.LeaseExpired", wantOK: true,
			ev: events.Event{Kind: jobs.EventLeaseExpired, Payload: expiredPayload},
			check: func(t *testing.T, ev jobs.Event) {
				le, ok := ev.(jobs.LeaseExpired)
				if !ok || le.JobID != "job-b" || le.RepoID != "r1" || le.ScopeGlob != "**" {
					t.Fatalf("decoded = %#v, want LeaseExpired{JobID:job-b,RepoID:r1,ScopeGlob:**}", ev)
				}
			},
		},
		{name: "lease released is a disclosed no-op", ev: events.Event{Kind: jobs.EventLeaseReleased, Payload: acquiredPayload}, wantOK: false},
		{name: "lease contended is a disclosed no-op", ev: events.Event{Kind: jobs.EventLeaseContended, Payload: acquiredPayload}, wantOK: false},
		{name: "lease renewed is a disclosed no-op", ev: events.Event{Kind: jobs.EventLeaseRenewed, Payload: acquiredPayload}, wantOK: false},
		{name: "malformed payload refuses", ev: events.Event{Kind: jobs.EventLeaseAcquired, Payload: []byte("not json")}, wantErr: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			runDecodeSchedulerEventCase(t, c.ev, c.wantOK, c.wantErr, c.check)
		})
	}
}

// runDecodeSchedulerEventCase is TestDecodeSchedulerEvent's per-case
// assertion body, split out to stay under the 50-line function cap.
func runDecodeSchedulerEventCase(t *testing.T, ev events.Event, wantOK, wantErr bool, check func(t *testing.T, ev jobs.Event)) {
	t.Helper()
	event, ok, err := decodeSchedulerEvent(ev)
	if wantErr {
		if err == nil {
			t.Fatal("decodeSchedulerEvent() error = nil, want one")
		}
		return
	}
	if err != nil {
		t.Fatalf("decodeSchedulerEvent() error = %v, want nil", err)
	}
	if ok != wantOK {
		t.Fatalf("decodeSchedulerEvent() ok = %v, want %v", ok, wantOK)
	}
	if ok && check != nil {
		check(t, event)
	}
}

// TestDispatchSchedulerEvent_NodeRoleRefusesWithoutError proves a
// node-role context's Guard refusal is a disclosed no-op (nil error),
// never a consumer-crashing failure -- the exact behavior R-21.169's
// controller-singleton guard requires of every non-controller daemon.
func TestDispatchSchedulerEvent_NodeRoleRefusesWithoutError(t *testing.T) {
	sched := jobs.NewScheduler(runtime.NewSystemClock())
	nodeCtx := nodes.WithRole(context.Background(), nodes.RoleNode)
	payload, _ := json.Marshal(map[string]any{"repo_id": "r1", "scope_glob": "**", "holder": "job-a"})
	if err := dispatchSchedulerEvent(nodeCtx, sched, events.Event{Kind: jobs.EventLeaseAcquired, Payload: payload}); err != nil {
		t.Fatalf("dispatchSchedulerEvent() on a node-role ctx = %v, want nil (a refusal, not a crash)", err)
	}
}

// TestRunSchedulerConsumer_ExitsOnContextCancellation proves the consumer
// loop's ctx.Done() branch returns cleanly rather than blocking forever.
func TestRunSchedulerConsumer_ExitsOnContextCancellation(t *testing.T) {
	bus := newSchedulerTestBus()
	ctx, cancel := context.WithCancel(context.Background())
	sub, err := bus.Subscribe(ctx, jobs.LeaseEventNamespace, "sched-test-cancel", 8)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	sched := jobs.NewScheduler(runtime.NewSystemClock())

	done := make(chan error, 1)
	go func() { done <- runSchedulerConsumer(nodes.WithRole(ctx, nodes.RoleController), sched, sub) }()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runSchedulerConsumer() = %v, want nil on cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runSchedulerConsumer did not exit within 2s of context cancellation")
	}
}
