package executive

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/pkg/cascade"
)

// syncBuffer is a log sink safe for the mount's handler goroutines.
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

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func mountForTest(ctx context.Context, t *testing.T, bus *events.Bus, logger *slog.Logger) *Mounted {
	t.Helper()
	m, err := Mount(ctx, bus, newTestDetector(t), logger)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop() })
	return m
}

func publishRaw(ctx context.Context, t *testing.T, bus *events.Bus, ns string, kind events.EventKind, payload string) {
	t.Helper()
	if _, err := bus.Publish(ctx, ns, kind, "test", []byte(payload)); err != nil {
		t.Fatalf("Publish %s %s: %v", ns, kind, err)
	}
}

// waitFor polls cond until it holds or five seconds pass.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func nextExec(t *testing.T, sub *events.Subscription) events.Event {
	t.Helper()
	select {
	case ev := <-sub.Events:
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no exec event published")
	}
	return events.Event{}
}

func execReader(ctx context.Context, t *testing.T, bus *events.Bus) *events.Subscription {
	t.Helper()
	sub, err := bus.Subscribe(ctx, ExecNamespace, "test-reader", 8)
	if err != nil {
		t.Fatalf("Subscribe exec: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	return sub
}

// TestMountPublishesExecEvents verifies mapped bus events become validated exec.* events.
func TestMountPublishesExecEvents(t *testing.T) {
	isolateHome(t)
	ctx := context.Background()
	bus := newTestBus()
	m := mountForTest(ctx, t, bus, quietLogger())
	sub := execReader(ctx, t, bus)

	publishRaw(ctx, t, bus, nsJobs, busJobFailed, `{"job_id":"j1","consecutive_failed_attempts":2}`)
	ev := nextExec(t, sub)
	if ev.Kind != events.EventKind(KindTaskFailed) || string(ev.Payload) != `{"task_id":"j1","consecutive_failed_attempts":2}` ||
		ev.StreamID != "j1" || ev.RunID != "j1" || ev.Producer != "executive-adapter/jobs" || ev.PayloadVersion != 1 ||
		!strings.HasPrefix(ev.Source, "jobs:") {
		t.Fatalf("job.failed gave %+v payload %s", ev, ev.Payload)
	}

	publishRaw(ctx, t, bus, nsCI, busCIRunCompleted, `{"run_id":7,"passed":true}`)
	ev = nextExec(t, sub)
	if ev.Kind != events.EventKind(KindVerifyPassed) || ev.RunID != "ci:7" || ev.StreamID != "ci:7" ||
		string(ev.Payload) != `{"subject_id":"7"}` || ev.Producer != "executive-adapter/ci_results" {
		t.Fatalf("ci.run.completed gave %+v payload %s", ev, ev.Payload)
	}

	publishRaw(ctx, t, bus, nsJobs, busJobTransitioned, `{"job_id":"j2","from":"reviewing","to":"accepted"}`)
	publishRaw(ctx, t, bus, nsJobs, busJobFailed, `{"job_id":"j3","consecutive_failed_attempts":1}`)
	ev = nextExec(t, sub)
	if ev.RunID != "j3" {
		t.Fatalf("job.transitioned to accepted published %+v; want nothing before j3", ev)
	}
	waitFor(t, "three published", func() bool { return m.Published() == 3 })
	if m.Refused() != 0 || m.Dropped() != 0 {
		t.Fatalf("refused %d dropped %d; want 0", m.Refused(), m.Dropped())
	}
}

// TestMountRefusesInvalidPayload verifies a body failing validation or the guard is never published.
func TestMountRefusesInvalidPayload(t *testing.T) {
	isolateHome(t)
	ctx := context.Background()
	bus := newTestBus()
	logs := &syncBuffer{}
	m := mountForTest(ctx, t, bus, slog.New(slog.NewTextHandler(logs, nil)))

	publishRaw(ctx, t, bus, nsJobs, busJobFailed, `{"job_id":"j1","consecutive_failed_attempts":0}`)
	publishRaw(ctx, t, bus, nsJobs, busJobFailed, `{"job_id":"","consecutive_failed_attempts":1}`)
	publishRaw(ctx, t, bus, nsJobs, busJobFailed, `{"job_id":"`+skCanary()+`","consecutive_failed_attempts":1}`)
	waitFor(t, "three refused", func() bool { return m.Refused() == 3 })
	if err := m.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if m.Published() != 0 {
		t.Fatalf("published %d; want 0", m.Published())
	}
	if got, err := bus.Replay(ctx, ExecNamespace, 0); err != nil || len(got) != 0 {
		t.Fatalf("exec holds %d events, %v; want nothing published", len(got), err)
	}
	out := logs.String()
	if strings.Count(out, "executive adapter refused event") != 3 {
		t.Fatalf("want 3 WARN lines, got:\n%s", out)
	}
	if strings.Contains(out, skCanary()) {
		t.Fatalf("a WARN line holds the canary:\n%s", out)
	}
}

// TestMountStopAndConflict verifies the cursor conflict, idempotent Stop and nil inputs.
func TestMountStopAndConflict(t *testing.T) {
	isolateHome(t)
	ctx := context.Background()
	bus := newTestBus()
	m := mountForTest(ctx, t, bus, quietLogger())
	if _, err := Mount(ctx, bus, newTestDetector(t), quietLogger()); !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("second Mount: %v; want KindConflict", err)
	}
	publishRaw(ctx, t, bus, nsJobs, busJobFailed, `{"job_id":"j1","consecutive_failed_attempts":1}`)
	waitFor(t, "first mount publishes", func() bool { return m.Published() == 1 })
	if err := m.Stop(); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	if err := m.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	if _, err := Mount(ctx, nil, newTestDetector(t), nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Mount(nil bus): %v", err)
	}
	if _, err := Mount(ctx, bus, nil, nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Mount(nil det): %v", err)
	}
}

// TestMountRefusesDormantKind verifies a mapped kind with no live adapter
// route is refused and never published.
func TestMountRefusesDormantKind(t *testing.T) {
	isolateHome(t)
	ctx := context.Background()
	bus := newTestBus()
	m := mountForTest(ctx, t, bus, quietLogger())
	publishRaw(ctx, t, bus, nsNotify, busNotification, `{"id":"n1"}`)
	waitFor(t, "dormant refused", func() bool { return m.Refused() == 1 })
	if err := m.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got, err := bus.Replay(ctx, ExecNamespace, 0); err != nil || len(got) != 0 {
		t.Fatalf("exec holds %d events, %v; want nothing published", len(got), err)
	}
}

// TestLiveRoutesFollowAdapterTable verifies the routing holds every live
// adapter row and no dormant kind.
func TestLiveRoutesFollowAdapterTable(t *testing.T) {
	routes := liveRoutes()
	for _, k := range DormantKinds() {
		for ns, kinds := range routes {
			if kinds[k] {
				t.Fatalf("dormant %s routed from %s", k, ns)
			}
		}
	}
	for _, r := range AdapterRows() {
		if r.Exec != "" && r.Live && !routes[r.Namespace][r.Exec] {
			t.Fatalf("live row %s/%s -> %s not routed", r.Namespace, r.BusKind, r.Exec)
		}
	}
	if !routes[nsJobs][KindTaskFailed] || routes[nsNotify][KindDecisionRequired] {
		t.Fatalf("routes = %v", routes)
	}
}
