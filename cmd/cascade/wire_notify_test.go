//go:build !windows

package main

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/notify"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
)

// countNotifyServices swaps newNotifyService for a counting wrapper that
// records every Service built, restored when the test ends.
func countNotifyServices(t *testing.T) func() []*notify.Service {
	t.Helper()
	orig := newNotifyService
	var mu sync.Mutex
	var built []*notify.Service
	newNotifyService = func(cfg notify.Config, clock runtime.Clock, log *slog.Logger) *notify.Service {
		s := orig(cfg, clock, log)
		mu.Lock()
		built = append(built, s)
		mu.Unlock()
		return s
	}
	t.Cleanup(func() { newNotifyService = orig })
	return func() []*notify.Service {
		mu.Lock()
		defer mu.Unlock()
		return append([]*notify.Service(nil), built...)
	}
}

// composeNotify runs composeDaemon over a fresh HOME and returns its
// manifest and the stop function that runs every cleanup (the daemon stop).
func composeNotify(t *testing.T) (*daemon.Manifest, func()) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	deps := newRunTestDeps(t, nonexistentExecutable(t))
	opts, cleanups, err := composeDaemon(context.Background(), deps, func(*rpc.Registry, *rpc.SSEMux) {})
	var once sync.Once
	stop := func() { once.Do(func() { runCleanupsLIFO(cleanups) }) }
	t.Cleanup(stop)
	if err != nil {
		t.Fatalf("composeDaemon: %v", err)
	}
	return opts.Manifest, stop
}

func subsystemState(m *daemon.Manifest, name string) (daemon.SubsystemStatus, bool) {
	for _, s := range m.Snapshot() {
		if s.Name == name {
			return s, true
		}
	}
	return daemon.SubsystemStatus{}, false
}

// TestNotifyServiceComposedOnce: the production composition builds exactly
// one notify.Service, and both of its loops run on the daemon manifest.
func TestNotifyServiceComposedOnce(t *testing.T) {
	built := countNotifyServices(t)
	m, _ := composeNotify(t)
	if n := len(built()); n != 1 {
		t.Fatalf("composeDaemon built %d notify.Service values, want exactly 1", n)
	}
	for _, name := range []string{daemon.NotifyDispatcherSubsystem, daemon.NotifyRouterSubsystem} {
		if _, ok := subsystemState(m, name); !ok {
			t.Fatalf("subsystem %q not on the manifest", name)
		}
	}
}

// panicSub panics in Receive, so the drain loop that fans out to it panics.
type panicSub struct{}

func (panicSub) ID() string                     { return "panics" }
func (panicSub) Match(notify.Notification) bool { return true }
func (panicSub) Receive(notify.Notification) error {
	panic("subscriber exploded")
}

// TestDispatcherRunsUnderSupervision: a panic in the drain is recovered and
// recorded Failed while the router keeps running, and daemon stop joins
// both loops (the router ends Stopped, the join returns).
func TestDispatcherRunsUnderSupervision(t *testing.T) {
	built := countNotifyServices(t)
	m, stop := composeNotify(t)
	svc := built()[0]
	svc.Registry.Subscribe(panicSub{}, notify.CandidateSession{SessionID: "s"})
	err := svc.Notifier.Deliver(context.Background(),
		notify.Notification{ID: "boom", Class: notify.ClassGlobalCritical, Priority: notify.PriorityUrgent})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		s, _ := subsystemState(m, daemon.NotifyDispatcherSubsystem)
		if s.State == daemon.SubsystemError {
			if !strings.HasPrefix(s.Detail, "panic:") {
				t.Fatalf("dispatcher failed with %q, want a recorded panic", s.Detail)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dispatcher state %v %q; the drain panic was never recorded", s.State, s.Detail)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if r, _ := subsystemState(m, daemon.NotifyRouterSubsystem); r.State == daemon.SubsystemError {
		t.Fatalf("router failed (%q) after the drain panic; the panic was not isolated", r.Detail)
	}
	joined := make(chan struct{})
	go func() { stop(); close(joined) }()
	select {
	case <-joined:
	case <-time.After(30 * time.Second):
		t.Fatal("daemon stop did not join the notify loops")
	}
	r, _ := subsystemState(m, daemon.NotifyRouterSubsystem)
	if r.State != daemon.SubsystemSkipped {
		t.Fatalf("router after stop = %v %q, want stopped (joined)", r.State, r.Detail)
	}
}
