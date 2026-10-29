//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/notify"
)

// TestAwayIsolationGuardRefusesRealHome proves the guard is not vacuous.
func TestAwayIsolationGuardRefusesRealHome(t *testing.T) {
	for _, root := range []string{"/home/op", "/home/op/.cascade", "/home", "/"} {
		if awayIsolationViolation(root, "/home/op") == nil {
			t.Errorf("root %q was accepted", root)
		}
	}
	if err := awayIsolationViolation("/scratch/x/root", "/home/op"); err != nil {
		t.Errorf("a separate root was refused: %v", err)
	}
}

// TestAwaySubsystemInvalidNotifyTableRecordedDisabled: an invalid [notify]
// table records the validation error on the manifest and builds nothing.
func TestAwaySubsystemInvalidNotifyTableRecordedDisabled(t *testing.T) {
	for name, table := range map[string]map[string]interface{}{
		"unknown key":  {"away_thresold": "5m"},
		"bad duration": {"away_threshold": "soon"},
		"zero":         {"away_threshold": "0s"},
		"wrong type":   {"digest_urgent_deeplinks": "ten"},
	} {
		r := newAwayRig(t, table)
		r.wire()
		if s := r.row(awaySubsystem); s.State != daemon.SubsystemSkipped || !strings.Contains(s.Detail, "notify") {
			t.Errorf("%s: row = %v %q, want Skipped with the validation error", name, s.State, s.Detail)
		}
		if r.builds.Load() != 0 || r.svc.Router.Accumulating() {
			t.Errorf("%s: a controller was built or accumulation switched on", name)
		}
	}
}

// TestAwaySubsystemMissingDependencyNeverAccumulates: with each dependency
// removed in turn the row is Skipped with its reason, no controller exists,
// and a notification is delivered normally.
func TestAwaySubsystemMissingDependencyNeverAccumulates(t *testing.T) {
	cases := map[string]func(*daemonWiring){
		"notify runtime": func(w *daemonWiring) { w.Deps.Notify = nil },
		"presence":       func(w *daemonWiring) { w.Deps.Presence = nil },
		"fleet":          func(w *daemonWiring) { w.Deps.Fleet = nil },
		"store":          func(w *daemonWiring) { w.Store = nil },
		"bus":            func(w *daemonWiring) { w.Bus = nil },
	}
	for name, remove := range cases {
		r := newAwayRig(t, nil)
		remove(r.w)
		r.wire()
		if s := r.row(awaySubsystem); s.State != daemon.SubsystemSkipped || !strings.Contains(s.Detail, "missing dependency") {
			t.Errorf("%s: row = %v %q, want Skipped with a missing-dependency reason", name, s.State, s.Detail)
		}
		sub := r.subscribe("s-" + name)
		if err := r.svc.Notifier.Deliver(context.Background(), awayGlobal("n-"+name)); err != nil {
			t.Fatalf("%s: Deliver: %v", name, err)
		}
		r.svc.Dispatcher.Drain(context.Background())
		if r.builds.Load() != 0 || r.svc.Router.Accumulating() || len(sub.received()) != 1 {
			t.Errorf("%s: built %d, accumulating %v, delivered %d; want a notification delivered at once and no controller",
				name, r.builds.Load(), r.svc.Router.Accumulating(), len(sub.received()))
		}
	}
}

// TestAwaySubsystemReplaysJournalAtStart: a daemon restarted mid-away resumes
// the open episode (accumulating before its first tick) and its return closes
// that same episode in the journal.
func TestAwaySubsystemReplaysJournalAtStart(t *testing.T) {
	ctx := context.Background()
	r := newAwayRig(t, nil)
	js := journal.New(r.w.Store, r.clock, awayJournalNamespace)
	if _, err := js.Append(ctx, awayEntityID, journal.KindIntent, "ep-restart", json.RawMessage(`{"episode_id":"ep-restart"}`)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	r.wire()
	if r.ctrl == nil || r.ctrl.State() != notify.StateAway || r.ctrl.EpisodeID() != "ep-restart" || !r.svc.Router.Accumulating() {
		t.Fatalf("restart did not resume the open episode: controller %v", r.ctrl)
	}
	sub := r.subscribe("s1")
	if err := r.svc.Notifier.Deliver(ctx, awayGlobal("during")); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	r.svc.Dispatcher.Drain(ctx)
	if len(sub.received()) != 0 {
		t.Fatal("a notification arriving in a resumed episode was dispatched, not accumulated")
	}
	r.ctrl.HandleEvent(ctx, notify.ActivityEvent{})
	if got, err := notify.ReplayState(ctx, js, awayEntityID); err != nil || got.State != notify.StateActive {
		t.Fatalf("after the return ReplayState = %+v, %v; want Active (the Ack closed ep-restart)", got, err)
	}
}

// TestAwayDigestAddressedPerSession: the return digest reaches each session
// alone: Addressed, TargetSession that session, VisibilityPrivate.
func TestAwayDigestAddressedPerSession(t *testing.T) {
	ctx := context.Background()
	r := newAwayRig(t, nil)
	r.wire()
	s1, s2 := r.subscribe("s1"), r.subscribe("s2")
	r.driveToAway()
	if err := r.svc.Notifier.Deliver(ctx, awayGlobal("while-away")); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	r.ctrl.HandleEvent(ctx, notify.ActivityEvent{})
	r.svc.Dispatcher.Drain(ctx)
	for id, sub := range map[string]*awaySub{"s1": s1, "s2": s2} {
		got := sub.received()
		if len(got) != 1 {
			t.Fatalf("%s received %d notifications, want exactly its own digest", id, len(got))
		}
		if d := got[0]; d.Class != notify.ClassAddressed || d.TargetSession != id || d.Visibility != notify.VisibilityPrivate {
			t.Fatalf("%s digest = class %v target %q visibility %v, want addressed to itself and private", id, d.Class, d.TargetSession, d.Visibility)
		}
	}
}

// TestAwayConfigHotReloadApplies: an accepted reload (the real HotReloader's
// own event) re-reads config.toml and swaps the validated away keys in.
func TestAwayConfigHotReloadApplies(t *testing.T) {
	r := newAwayRig(t, nil)
	hr := r.reloadRig(strings.Replace(awayTOML, "%q", `"10m"`, 1))
	r.wire()
	r.awaitConfig(notify.AwayConfig{Threshold: 10 * time.Minute, DigestUrgentDeepLinks: 4})
	writeConfig(t, r.w.Paths.ConfigPath(), strings.Replace(awayTOML, "%q", `"20m"`, 1))
	if out := hr.Reload(context.Background()); !out.Accepted {
		t.Fatalf("Reload = %+v, want accepted", out)
	}
	r.awaitConfig(notify.AwayConfig{Threshold: 20 * time.Minute, DigestUrgentDeepLinks: 4})
}

// TestAwayConfigInvalidReloadKeepsRunning: a reload whose away keys are
// refused changes nothing, publishes notify.reload.rejected, and the loop
// stays alive for the next good reload.
func TestAwayConfigInvalidReloadKeepsRunning(t *testing.T) {
	ctx := context.Background()
	r := newAwayRig(t, nil)
	hr := r.reloadRig(strings.Replace(awayTOML, "%q", `"10m"`, 1))
	r.wire()
	rejected, err := r.w.Bus.SubscribeFromHead(ctx, daemon.NotifyNamespace, "away-test", 8)
	if err != nil {
		t.Fatalf("SubscribeFromHead: %v", err)
	}
	t.Cleanup(func() { _ = rejected.Unsubscribe() })
	running := r.ctrl.Config()
	writeConfig(t, r.w.Paths.ConfigPath(), strings.Replace(awayTOML, "%q", `"soon"`, 1))
	if out := hr.Reload(ctx); !out.Accepted {
		t.Fatalf("Reload = %+v, want accepted by the generic reloader", out)
	}
	select {
	case ev := <-rejected.Events:
		if ev.Kind != awayReloadRejected || !strings.Contains(string(ev.Payload), "away_threshold") {
			t.Fatalf("event = %s %s, want notify.reload.rejected naming the key", ev.Kind, ev.Payload)
		}
	case <-time.After(awayTestBound):
		t.Fatal("no notify.reload.rejected event")
	}
	if got := r.ctrl.Config(); got != running {
		t.Fatalf("running config changed to %+v after a rejected reload", got)
	}
	writeConfig(t, r.w.Paths.ConfigPath(), strings.Replace(awayTOML, "%q", `"15m"`, 1))
	hr.Reload(ctx)
	r.awaitConfig(notify.AwayConfig{Threshold: 15 * time.Minute, DigestUrgentDeepLinks: 4})
}

// TestAwaySubsystemStopsWithRunContext: cancelling the run context joins all
// three loops, stops the ticker, and a tick before that reached the controller.
func TestAwaySubsystemStopsWithRunContext(t *testing.T) {
	r := newAwayRig(t, nil)
	r.wire()
	for _, name := range []string{awaySubsystem, awayStallSubsystem, awayReloadSubsystem} {
		if s := r.row(name); s.State != daemon.SubsystemRunning {
			t.Fatalf("%s = %v %q, want running", name, s.State, s.Detail)
		}
	}
	r.clock.Advance(31 * time.Minute)
	for i := 0; i < 3; i++ { // the third send returns once the second Tick finished
		select {
		case r.tick.c <- struct{}{}:
		case <-time.After(awayTestBound):
			t.Fatal("the tick loop never took a tick")
		}
	}
	if got := r.ctrl.State(); got != notify.StateAway {
		t.Fatalf("state after two ticks past the threshold = %v, want Away", got)
	}
	r.cancel()
	joined := make(chan struct{})
	go func() { r.w.Manifest.Wait(); close(joined) }()
	select {
	case <-joined:
	case <-time.After(awayTestBound):
		t.Fatal("daemon stop did not join the away loops")
	}
	for _, name := range []string{awaySubsystem, awayStallSubsystem, awayReloadSubsystem} {
		if s := r.row(name); s.State != daemon.SubsystemSkipped {
			t.Errorf("%s after stop = %v %q, want stopped", name, s.State, s.Detail)
		}
	}
	if !r.tick.stopped.Load() {
		t.Error("the tick loop left its ticker running")
	}
}

// TestAwaySubsystemConstructedInProductionDaemon: composeDaemon with the
// production registrations records notify.away Started over the daemon's one
// notify.Service and the fleet runtime's admission controller.
func TestAwaySubsystemConstructedInProductionDaemon(t *testing.T) {
	built := countNotifyServices(t)
	var deps []notify.AwayDeps
	prev := newAwayController
	newAwayController = func(d notify.AwayDeps, c notify.AwayConfig, rr notify.ReplayResult) *notify.AwayController {
		deps = append(deps, d)
		return prev(d, c, rr)
	}
	var admission notify.AdmissionIdleSource
	regs := daemonRegistrations
	daemonRegistrations = append(append([]daemonRegistration(nil), regs...), daemonRegistration{
		Name: "away-test-probe", Phase: phaseSupervised, Order: 61,
		Wire: func(w *daemonWiring) error { admission = w.Deps.Fleet.Admission(); return nil },
	})
	t.Cleanup(func() { newAwayController, daemonRegistrations = prev, regs })

	m, _ := composeNotify(t)
	svcs := built()
	if len(svcs) != 1 || len(deps) != 1 || admission == nil {
		t.Fatalf("composeDaemon built %d notify services and %d away controllers (admission %v), want 1 and 1", len(svcs), len(deps), admission)
	}
	if deps[0].Router != svcs[0].Router {
		t.Error("away mode is not over the daemon's one notify.Service")
	}
	if deps[0].Admission != admission {
		t.Error("away mode did not read the fleet runtime's admission controller")
	}
	if s, ok := subsystemState(m, awaySubsystem); !ok || s.State != daemon.SubsystemRunning {
		t.Fatalf("notify.away row = %+v (found %v), want running", s, ok)
	}
}
