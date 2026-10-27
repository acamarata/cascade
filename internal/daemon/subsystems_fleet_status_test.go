package daemon

// Purpose: the admission row seen through status.get (every posture, including
// the no-signal error row), and the sampler start/stop handshake: Close before
// the sampler body starts, and Close racing its start.
// SPORT: internal/daemon (ADD, P1-CAP-12).

import (
	"runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/governor"
)

// admissionStatus returns the admission row and the overall health as
// status.get serves them.
func (g *fleetRig) admissionStatus(t *testing.T) (SubsystemStatus, string) {
	t.Helper()
	resp, err := NewStatusProvider(g.deps.Clock, g.deps.Clock.Now(), "/x", nil, g.deps.Manifest).Handler()(g.ctx, nil)
	if err != nil {
		t.Fatalf("status.get: %v", err)
	}
	status := resp.(StatusResponse)
	for _, s := range status.Subsystems {
		if s.Name == "admission" {
			return s, status.Health
		}
	}
	t.Fatal("status.get has no admission subsystem")
	return SubsystemStatus{}, ""
}

// TestStatusReportsAdmissionPosture drives the posture through the seam and
// reads it back through status.get: running with posture=<posture> for every
// posture but no-signal, an error row naming the refusal (health degraded)
// for no-signal, and running again once the signal returns.
func TestStatusReportsAdmissionPosture(t *testing.T) {
	g := newFleetRig(t, "")
	r := g.start(t)
	if s, health := g.admissionStatus(t); s.State != SubsystemRunning || s.Detail != "posture=stale" || health != "ok" {
		t.Fatalf("admission before a tick = %+v health %q, want running posture=stale, ok", s, health)
	}
	g.ticker.tick(t)
	if s, _ := g.admissionStatus(t); s.State != SubsystemRunning || (s.Detail != "posture=memory-only" && s.Detail != "posture=swap+memory") {
		t.Fatalf("admission after a tick = %+v, want running with a memory posture", s)
	}
	drive := func(p governor.SignalPosture) (SubsystemStatus, string) {
		r.postureOverride.Store(&p)
		g.rowTicker.tick(t)
		return g.admissionStatus(t)
	}
	s, health := drive(governor.PostureNoSignal)
	if s.State != SubsystemError || !strings.Contains(s.Detail, "posture=no-signal: "+governor.ErrNoResourceSignal.Error()) || health != "degraded" {
		t.Fatalf("no-signal row = %+v health %q, want an error row naming the refusal, degraded", s, health)
	}
	if s, health = drive(governor.PostureMemoryOnly); s.State != SubsystemRunning || s.Detail != "posture=memory-only" || health != "ok" {
		t.Fatalf("after the signal returned = %+v health %q, want running memory-only, ok", s, health)
	}
	// Stale is a running row by design (see admissionRow): the ticket reads "Started with posture=<posture>".
	if s, _ = drive(governor.PostureStale); s.State != SubsystemRunning || s.Detail != "posture=stale" {
		t.Fatalf("stale row = %+v, want running posture=stale", s)
	}
}

// settledPolls reports whether the fake ticker saw any C() call, giving the
// scheduler a bounded number of yields so a wrongly started loop shows up.
func (f *fleetFakeTicker) settledPolls() int {
	for i := 0; i < 2000 && len(f.polled) == 0; i++ {
		runtime.Gosched()
	}
	return len(f.polled)
}

// TestFleetRuntimeCloseBeforeSamplerStarts: Close runs exactly between the
// build and the sampler body's start. The body must not start the sampler
// afterwards (no loop polls the ticker), must still return, and the ticker
// must be stopped by Close itself.
func TestFleetRuntimeCloseBeforeSamplerStarts(t *testing.T) {
	g := newFleetRig(t, "")
	saved := beforeSamplerStart
	beforeSamplerStart = func(r *FleetRuntime) { _ = r.Close() }
	t.Cleanup(func() { beforeSamplerStart = saved })
	r, err := NewFleetRuntime(g.ctx, g.deps)
	if err != nil {
		t.Fatalf("NewFleetRuntime: %v", err)
	}
	g.awaitJoin(t)
	select {
	case <-r.ticker.stopped:
	default:
		t.Fatal("Close before the sampler started left the ticker running")
	}
	if n := g.ticker.settledPolls(); n != 0 {
		t.Fatalf("the sampler loop ran after Close (%d ticker polls): Start-after-Stop leaked a goroutine", n)
	}
	if got := g.row(fleetSamplerSubsystem); got.State != SubsystemSkipped {
		t.Fatalf("sampler row = %+v, want skipped/stopped", got)
	}
}

// TestFleetRuntimeStartCloseStress races Close against the sampler start many
// times; run with -race. Every round must join promptly whichever side wins.
func TestFleetRuntimeStartCloseStress(t *testing.T) {
	for i := 0; i < 150; i++ {
		g := newFleetRig(t, "")
		r, err := NewFleetRuntime(g.ctx, g.deps)
		if err != nil {
			t.Fatalf("round %d: NewFleetRuntime: %v", i, err)
		}
		done := make(chan struct{})
		go func() { _ = r.Close(); close(done) }()
		g.awaitJoin(t)
		<-done
		g.cancel()
	}
}
