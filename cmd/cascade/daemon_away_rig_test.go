//go:build !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/notify"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
)

// Test support for daemon_away_test.go (P1-HRN-11).
const awayTestBound = 10 * time.Second

// awayIsolationViolation is the guard behind awayRig: a root that is the
// operator home, its default runtime root or an ancestor of the home is refused.
func awayIsolationViolation(root, realHome string) error {
	root, realHome = filepath.Clean(root), filepath.Clean(realHome)
	rel, err := filepath.Rel(root, realHome)
	if root == realHome || root == filepath.Join(realHome, ".cascade") || (err == nil && !strings.HasPrefix(rel, "..")) {
		return os.ErrInvalid
	}
	return nil
}

// awayStepper is a runtime.Ticker the test fires by hand.
type awayStepper struct {
	c       chan struct{}
	stopped atomic.Bool
}

func (s *awayStepper) C() <-chan struct{} { return s.c }
func (s *awayStepper) Stop()              { s.stopped.Store(true) }

// awaySub records every notification it receives.
type awaySub struct {
	id  string
	mu  sync.Mutex
	got []notify.Notification
}

func (s *awaySub) ID() string                     { return s.id }
func (s *awaySub) Match(notify.Notification) bool { return true }
func (s *awaySub) Receive(n notify.Notification) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, n)
	return nil
}
func (s *awaySub) received() []notify.Notification {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]notify.Notification(nil), s.got...)
}

// awayRig is a daemonWiring over a fresh isolated home, a real fleet runtime
// and a captured controller; every collaborator is real except the tickers.
type awayRig struct {
	t      *testing.T
	w      *daemonWiring
	svc    *notify.Service
	clock  *testkit.FrozenClock
	tick   *awayStepper
	ctrl   *notify.AwayController
	builds atomic.Int32
	cancel context.CancelFunc
}

func newAwayRig(t *testing.T, notifyTable map[string]interface{}) *awayRig {
	t.Helper()
	home := t.TempDir()
	if err := awayIsolationViolation(home, mustHome(t)); err != nil {
		t.Fatalf("test root refused: %v", err)
	}
	for _, k := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(k, home)
	}
	clock := testkit.NewFrozenClock(time.Unix(40_000, 0))
	store := storetest.NewMemStore()
	ctx, cancel := context.WithCancel(context.Background())
	r := &awayRig{t: t, clock: clock, tick: &awayStepper{c: make(chan struct{})}, cancel: cancel}
	paths := fakeDaemonPaths{root: home}
	bus := events.New(store, clock)
	manifest := daemon.NewManifest(nil, clock)
	cfg := &runtime.Config{Extra: map[string]interface{}{"notify": notifyTable}}
	fleet, err := daemon.NewFleetRuntime(ctx, daemon.FleetRuntimeDeps{Paths: paths, Clock: clock, Manifest: manifest, Bus: bus, Config: cfg,
		Store: store, Binding: nodes.NewFileControllerBindingBackend(home), SamplerTicker: &awayStepper{c: make(chan struct{})}})
	if err != nil {
		t.Fatalf("NewFleetRuntime: %v", err)
	}
	r.svc = notify.NewService(notify.DefaultConfig(), clock, nil)
	r.w = &daemonWiring{Ctx: ctx, Registry: rpc.NewRegistry(), Manifest: manifest, Clock: clock, Store: store, Bus: bus, Paths: paths,
		Deps: daemonRuntime{Config: cfg, Notify: daemon.NewNotifyRuntime(r.svc), Presence: daemon.NewPresenceTracker(clock), Fleet: fleet,
			Daemon: daemonDeps{Getenv: func(string) string { return "" }, Environ: func() []string { return nil }}}}
	prevCtor, prevTicker := newAwayController, awayTicker
	newAwayController = func(d notify.AwayDeps, c notify.AwayConfig, rr notify.ReplayResult) *notify.AwayController {
		r.builds.Add(1)
		r.ctrl = prevCtor(d, c, rr)
		return r.ctrl
	}
	awayTicker = func() runtime.Ticker { return r.tick }
	t.Cleanup(func() {
		cancel()
		manifest.Wait()
		_ = fleet.Close()
		_ = bus.Close()
		newAwayController, awayTicker = prevCtor, prevTicker
	})
	return r
}

func mustHome(t *testing.T) string {
	t.Helper()
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

func (r *awayRig) wire() {
	r.t.Helper()
	if err := wireAwayMode(r.w); err != nil {
		r.t.Fatalf("wireAwayMode: %v", err)
	}
}

func (r *awayRig) row(name string) daemon.SubsystemStatus {
	r.t.Helper()
	s, ok := subsystemState(r.w.Manifest, name)
	if !ok {
		r.t.Fatalf("subsystem %q is not on the manifest", name)
	}
	return s
}

// driveToAway moves the controller to Away: idle past the default 30m
// threshold, two Ticks with a quiet machine.
func (r *awayRig) driveToAway() {
	r.t.Helper()
	r.clock.Advance(31 * time.Minute)
	r.ctrl.Tick(context.Background())
	if got := r.ctrl.Tick(context.Background()); got != notify.StateAway {
		r.t.Fatalf("second Tick = %v, want Away", got)
	}
}

// subscribe registers a recording subscriber for session id.
func (r *awayRig) subscribe(id string) *awaySub {
	s := &awaySub{id: id}
	r.svc.Registry.Subscribe(s, notify.CandidateSession{SessionID: id})
	return s
}

func awayGlobal(id string) notify.Notification {
	return notify.Notification{ID: id, Class: notify.ClassGlobalCritical, Priority: notify.PriorityUrgent}
}

// reloadRig writes body as config.toml, loads it as the daemon's boot config
// and returns the real hot reloader publishing on the rig's bus.
func (r *awayRig) reloadRig(body string) *runtime.HotReloader {
	r.t.Helper()
	path := r.w.Paths.ConfigPath()
	opts := testLoadOpts(path)
	writeConfig(r.t, path, body)
	cfg, err := runtime.Load(context.Background(), opts)
	if err != nil {
		r.t.Fatalf("Load: %v", err)
	}
	r.w.Deps.Config = cfg
	return runtime.NewHotReloader(path, opts, cfg, r.clock, &busEventPublisher{bus: r.w.Bus}, nil, nil)
}

func (r *awayRig) awaitConfig(want notify.AwayConfig) {
	r.t.Helper()
	deadline := time.Now().Add(awayTestBound)
	for r.ctrl.Config() != want {
		if time.Now().After(deadline) {
			r.t.Fatalf("running away config = %+v, want %+v", r.ctrl.Config(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

const awayTOML = "[notify]\naway_threshold = %q\ndigest_urgent_deeplinks = 4\n"
