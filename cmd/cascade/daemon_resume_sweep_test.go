//go:build !windows

// Purpose: the fanout-resume registration's claim table and sweep in
//
//	process: the scan and the sweep use the conductor handler's own claim
//	table (w.ConductorFanOut.Claims), the scan classifies only, an
//	unattached fan-out expires after fanout_record_ttl with nothing
//	dispatched, a claim held past the ttl keeps it, and a failing sweep is reported and retried, never fatal.
//
// Constraints: untagged and offline; a frozen clock drives the ttl.
// SPORT: cmd/cascade/daemon (CHANGE, P1-CORE-15).
package main

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/resume"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// resumeConfig sets the two [daemon] fan-out settings.
func resumeConfig(ttl, interval string) *runtime.Config {
	return &runtime.Config{Extra: map[string]interface{}{"daemon": map[string]interface{}{
		"fanout_record_ttl": ttl, "fanout_sweep_interval": interval}}}
}

// waitFor polls cond for up to 5s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestFanOutResumeSharesConductorClaims(t *testing.T) {
	store, a, b := storetest.NewMemStore(), "01ARZ3NDEKTSV4RRFFQ69G5FAV", "01BX5ZZKBKACTAV9WEVGEMMVRZ"
	seedResumable(t, store, a)
	seedResumable(t, store, b)
	clock := testkit.NewFrozenClock(resumeSeedInstant.Add(time.Minute))
	fan := daemon.NewConductorFanOut(store, clock) // the value the conductor registration stores
	if !fan.Claims.TryClaim(a) {
		t.Fatal("setup: claim a")
	}
	for _, want := range []string{"pending 1", "pending 2"} { // a held by a live call, then released
		ctx, cancel := context.WithCancel(context.Background())
		w := resumeWiring(ctx, store, fan, clock)
		w.Deps.Config = resumeConfig("1h", "1h")
		if err := wireFanOutResume(w); err != nil {
			t.Fatalf("wireFanOutResume: %v", err)
		}
		if got := subsystem(t, w, fanOutResumeSubsystem); got.State != daemon.SubsystemRunning || got.Detail != want {
			t.Fatalf("fanout-resume = %+v, want started %q (the scan skips an id the conductor's table holds)", got, want)
		}
		cancel()
		w.Manifest.Wait()
		fan.Claims.Release(a)
	}
	for _, id := range []string{a, b} {
		if st, kept := fanOutState(t, store, id); st.Final != "" || kept != 1 || len(journalEntries(t, store, id)) != 1 {
			t.Fatalf("%s: marker %q, records %d, entries %d; want the scan to classify only", id, st.Final, kept, len(journalEntries(t, store, id)))
		}
	}
}

func TestFanOutSweepExpiresUnattached(t *testing.T) {
	store, id := storetest.NewMemStore(), "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	seedResumable(t, store, id)
	clock := testkit.NewFrozenClock(resumeSeedInstant.Add(time.Minute))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := resumeWiring(ctx, store, daemon.NewConductorFanOut(store, clock), clock)
	w.Deps.Config = resumeConfig("1h", "10ms")
	if err := wireFanOutResume(w); err != nil {
		t.Fatalf("wireFanOutResume: %v", err)
	}
	if got := subsystem(t, w, fanOutSweepSubsystem); got.State != daemon.SubsystemRunning || got.Detail != "every 10ms, ttl 1h0m0s" {
		t.Fatalf("fanout-sweep = %+v, want started every 10ms, ttl 1h", got)
	}
	time.Sleep(50 * time.Millisecond) // several sweeps inside the ttl
	if st, kept := fanOutState(t, store, id); st.Final != "" || kept != 1 {
		t.Fatalf("inside the ttl: marker %q, records %d; want untouched", st.Final, kept)
	}
	clock.Advance(2 * time.Hour)
	waitFor(t, "the expired marker", func() bool { st, _ := fanOutState(t, store, id); return st.Final == resume.OutcomeExpired })
	if st, kept := fanOutState(t, store, id); kept != 0 || len(st.Starts) != 0 {
		t.Fatalf("after the ttl: records %d, starts %v; want every record deleted and nothing dispatched", kept, st.Starts)
	}
	cancel()
	w.Manifest.Wait()
	if got := subsystem(t, w, fanOutSweepSubsystem); got.State != daemon.SubsystemSkipped {
		t.Fatalf("fanout-sweep after cancel = %+v, want stopped", got)
	}
}

func TestFanOutSweepHeldClaimKeepsExpired(t *testing.T) {
	store, id := storetest.NewMemStore(), "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	seedResumable(t, store, id)
	clock := testkit.NewFrozenClock(resumeSeedInstant.Add(2 * time.Hour)) // the 1h ttl has elapsed
	fan := daemon.NewConductorFanOut(store, clock)
	if !fan.Claims.TryClaim(id) { // a live conductor.execute call holds the id
		t.Fatal("setup: claim the id")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := resumeWiring(ctx, store, fan, clock)
	w.Deps.Config = resumeConfig("1h", "10ms")
	if err := wireFanOutResume(w); err != nil {
		t.Fatalf("wireFanOutResume: %v", err)
	}
	time.Sleep(50 * time.Millisecond) // several sweeps past the ttl while the claim is held
	if st, kept := fanOutState(t, store, id); st.Final != "" || kept != 1 || len(journalEntries(t, store, id)) != 1 {
		t.Fatalf("claim held past the ttl: marker %q, records %d; want no marker and the records kept", st.Final, kept)
	}
	fan.Claims.Release(id)
	waitFor(t, "the expired marker once released", func() bool { st, _ := fanOutState(t, store, id); return st.Final == resume.OutcomeExpired })
	cancel()
	w.Manifest.Wait()
}

// failScanStore fails every listing: no sweep pass can run.
type failScanStore struct{ provider.Store }

func (failScanStore) Scan(context.Context, string, string) (provider.Iterator, error) {
	return nil, cascade.New(cascade.KindUnavailable, "injected: listing failed")
}

// chanTicker is a Ticker the test drives by hand.
type chanTicker struct{ c chan struct{} }

func (k chanTicker) C() <-chan struct{} { return k.c }
func (chanTicker) Stop()                {}

func TestFanOutSweepFailureIsReportedAndRetried(t *testing.T) {
	clock := testkit.NewFrozenClock(resumeSeedInstant)
	store := failScanStore{Store: storetest.NewMemStore()}
	deps := resume.FanOutDeps{Store: store, Heads: daemon.NewConductorFanOut(store, clock).Journal, Claims: resume.NewClaims(), Clock: clock}
	ctx, cancel := context.WithCancel(context.Background())
	tick, errs, done := chanTicker{c: make(chan struct{})}, make(chan error, 4), make(chan error, 1)
	go func() { done <- runFanOutSweep(ctx, deps, time.Hour, tick, func(err error) { errs <- err }) }()
	first := <-errs
	tick.c <- struct{}{}
	second := <-errs
	cancel()
	if err := <-done; err != nil || !cascade.HasKind(first, cascade.KindUnavailable) || !cascade.HasKind(second, cascade.KindUnavailable) {
		t.Fatalf("loop = %v, reports %v / %v; want two reported listing failures and a nil return on cancel", err, first, second)
	}

	w := resumeWiring(ctx, store, daemon.ConductorFanOut{}, clock)
	w.Ctx, cancel = context.WithCancel(context.Background())
	w.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	startFanOutSweep(w, deps, daemon.ResumeSettings{RecordTTL: time.Hour, SweepInterval: time.Hour})
	waitFor(t, "the failure in status.get", func() bool {
		return strings.Contains(subsystem(t, w, fanOutSweepSubsystem).Detail, "last sweep failed: unavailable")
	})
	cancel()
	w.Manifest.Wait()
}
