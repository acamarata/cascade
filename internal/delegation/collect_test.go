package delegation

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/notify"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/providers/agents"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/envelope"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestCollectorDeadline(t *testing.T) {
	for _, tc := range []struct {
		wall, maximum, expiry time.Duration
		want                  time.Duration
		reason                string
	}{
		{time.Minute, time.Hour, 2 * time.Hour, time.Minute, "wall clock"},
		{time.Hour, time.Minute, 2 * time.Hour, time.Minute, "wall clock"},
		{time.Hour, 2 * time.Hour, time.Minute, time.Minute, "envelope expired"},
		{time.Minute, time.Minute, time.Minute, time.Minute, "envelope expired"},
	} {
		c := collector{env: envelope.Envelope{ExpiresAt: t0.Add(tc.expiry), Budget: envelope.Budget{WallClock: tc.wall}}, start: t0, maxWallClock: tc.maximum}
		deadline, reason := c.deadline()
		if !deadline.Equal(t0.Add(tc.want)) || reason != tc.reason {
			t.Fatalf("deadline = %v (%s), want %v (%s)", deadline, reason, t0.Add(tc.want), tc.reason)
		}
	}
}

type collectDriver struct {
	provider.AgentProvider
	status   func(context.Context) provider.AgentRunState
	collect  func(context.Context)
	collects int
}

func (d *collectDriver) Status(ctx context.Context, _ provider.AgentJobID) (provider.AgentRunState, error) {
	return d.status(ctx), nil
}

func (d *collectDriver) Collect(ctx context.Context, id provider.AgentJobID) (provider.CollectResult, error) {
	d.collects++
	if d.collect != nil {
		d.collect(ctx)
	}
	return provider.CollectResult{JobID: id, Output: "finished", Artifacts: []string{"result.txt"}, DataClass: provider.SensitivityRestricted}, nil
}

type collectFixture struct {
	c        *collector
	chain    *chain
	fleet    *fleetDouble
	clock    *runtime.FixedClock
	driver   *collectDriver
	resolve  func() (principal.Principal, error)
	releases int
}

func newCollectFixture(t *testing.T) *collectFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	ch, parent, svc, fleet, shim := setup(t)
	clock := runtime.NewFixedClock(t0)
	svc.deps.Clock = clock
	resp, err := svc.Delegate(context.Background(), &Request{AgentID: "collect-child", Parent: parent})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := ch.st.GetDelegation(context.Background(), resp.Record.Env.ID)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := AllocWorktree(t.TempDir(), rec.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	f := &collectFixture{chain: ch, fleet: fleet, clock: clock}
	f.resolve = func() (principal.Principal, error) {
		return svc.deps.Minter.Resolve(shim.reqs[0].PrincipalToken, t0)
	}
	f.driver = &collectDriver{status: func(context.Context) provider.AgentRunState { return provider.AgentRunAccepted }}
	f.c = &collector{service: svc, notify: notify.NewService(notify.DefaultConfig(), clock, nil), record: rec,
		env: resp.Record.Env, lease: capacity.Lease{ID: resp.LeaseID}, worktree: wt, start: t0, maxWallClock: time.Hour}
	f.c.handle = agents.SpawnHandle{JobID: provider.AgentJobID(rec.AgentJobID), Driver: f.driver,
		Release: func(_ context.Context) error {
			f.releases++
			if _, err := os.Stat(wt.Root); err != nil {
				t.Error("handle released after worktree", err)
			}
			if _, err := f.resolve(); err != nil {
				t.Error("principal revoked before handle release", err)
			}
			return nil
		}}
	return f
}

func (f *collectFixture) assertReleased(t *testing.T, state jobs.DelegationState, reason string) {
	t.Helper()
	rec, err := f.chain.st.GetDelegation(context.Background(), f.c.record.ID)
	if err != nil || rec.State != state || rec.Reason != reason {
		t.Fatalf("record = %+v, %v; want %s %q", rec, err, state, reason)
	}
	if f.releases != 1 || strings.Join(f.fleet.calls, ",") != "commit,release" {
		t.Fatalf("release calls = %d / %v", f.releases, f.fleet.calls)
	}
	if _, err := os.Stat(f.c.worktree.Root); !os.IsNotExist(err) {
		t.Fatalf("worktree remains: %v", err)
	}
	if _, err := f.resolve(); err == nil {
		t.Fatal("principal still resolves")
	}
}

func TestDelegateCommitAbortRelease(t *testing.T) {
	f := newCollectFixture(t)
	if err := f.c.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.assertReleased(t, jobs.DelegationSucceeded, "")
	_, parent, svc, fleet, _ := setup(t)
	failure := cascade.New(cascade.KindUnavailable, "spawn refused")
	svc.shim = &errShimDouble{err: failure}
	_, err := svc.Delegate(context.Background(), &Request{AgentID: "spawn-failure", Parent: parent})
	if err != failure || strings.Join(fleet.calls, ",") != "abort" {
		t.Fatalf("spawn failure = %v, calls %v", err, fleet.calls)
	}
}

func TestDelegateMintsAndRevokesPrincipal(t *testing.T) {
	f := newCollectFixture(t)
	if p, err := f.resolve(); err != nil || p.ID != f.c.record.AgentPrincipal {
		t.Fatalf("running principal = %+v, %v", p, err)
	}
	if err := f.c.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.assertReleased(t, jobs.DelegationSucceeded, "")
}

func TestCollectorReleaseOnce(t *testing.T) {
	f := newCollectFixture(t)
	for range 2 {
		if err := f.c.release(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if f.releases != 1 || strings.Join(f.fleet.calls, ",") != "commit,release" {
		t.Fatalf("release calls = %d / %v", f.releases, f.fleet.calls)
	}
}

func TestDelegateWallClockCancels(t *testing.T)      { testCollectorExpiry(t, false) }
func TestDelegateEnvelopeExpiryCancels(t *testing.T) { testCollectorExpiry(t, true) }

func testCollectorExpiry(t *testing.T, expiry bool) {
	f := newCollectFixture(t)
	f.c.env.Budget.WallClock, f.c.env.ExpiresAt = time.Minute, t0.Add(time.Hour)
	reason := "wall clock"
	if expiry {
		f.c.env.Budget.WallClock, f.c.env.ExpiresAt, reason = time.Hour, t0.Add(time.Minute), "envelope expired"
	}
	ready, ticks := make(chan struct{}), make(chan struct{})
	f.driver.status = func(context.Context) provider.AgentRunState { close(ready); return provider.AgentRunRunning }
	f.c.ticks = ticks
	manifest := daemon.NewManifest(nil, fakeClock{now: t0})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.c.startSupervised(ctx, manifest)
	<-ready
	f.clock.Advance(time.Minute)
	ticks <- struct{}{}
	manifest.Wait()
	f.assertReleased(t, jobs.DelegationFailed, reason)
	if f.driver.collects != 0 {
		t.Fatal("expired child collected")
	}
}

func TestCollectorDeadlineInterruptsStatus(t *testing.T) {
	f := newCollectFixture(t)
	f.c.env.Budget.WallClock = time.Minute
	ready, ticks := make(chan struct{}), make(chan struct{})
	f.c.ticks = ticks
	f.driver.status = func(ctx context.Context) provider.AgentRunState {
		close(ready)
		<-ctx.Done()
		return provider.AgentRunRunning
	}
	manifest := daemon.NewManifest(nil, fakeClock{now: t0})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.c.startSupervised(ctx, manifest)
	<-ready
	f.clock.Advance(time.Minute)
	ticks <- struct{}{}
	manifest.Wait()
	f.assertReleased(t, jobs.DelegationFailed, "wall clock")
}

func TestCollectorDeadlineReleasesBlockedCollect(t *testing.T) {
	f := newCollectFixture(t)
	f.c.env.Budget.WallClock = time.Minute
	ready, released, ticks := make(chan struct{}), make(chan struct{}), make(chan struct{})
	f.c.ticks = ticks
	f.driver.collect = func(context.Context) {
		close(ready)
		<-released
	}
	release := f.c.handle.Release
	f.c.handle.Release = func(ctx context.Context) error {
		err := release(ctx)
		close(released)
		return err
	}
	manifest := daemon.NewManifest(nil, fakeClock{now: t0})
	f.c.startSupervised(context.Background(), manifest)
	<-ready
	f.clock.Advance(time.Minute)
	ticks <- struct{}{}
	manifest.Wait()
	f.assertReleased(t, jobs.DelegationFailed, "wall clock")
}

func TestCollectorDaemonStop(t *testing.T) {
	f := newCollectFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	manifest := daemon.NewManifest(nil, fakeClock{now: t0})
	f.c.startSupervised(ctx, manifest)
	manifest.Wait()
	f.assertReleased(t, jobs.DelegationFailed, "daemon stopped")
}

func TestCollectorAddressedWait(t *testing.T) {
	f := newCollectFixture(t)
	if err := f.c.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	owner := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	id := f.c.record.ID
	before, err := f.c.service.wait(owner, f.c.notify, id, 0, nil)
	if err != nil || before.Output != "" {
		t.Fatalf("before drain = %+v, %v", before, err)
	}
	f.c.notify.Dispatcher.Drain(context.Background())
	result, err := f.c.service.wait(owner, f.c.notify, id, 0, nil)
	if err != nil || result.Output != "finished" || result.State != jobs.DelegationSucceeded || len(result.Artifacts) != 1 {
		t.Fatalf("result = %+v, %v", result, err)
	}
	other := principal.WithCaller(context.Background(), principal.Caller{Principal: &principal.Principal{ID: "intruder"}})
	_, err = f.c.service.wait(other, f.c.notify, id, 0, nil)
	wantCascadeErr(t, err, cascade.KindPermissionDenied, "only the delegation origin may wait")
}
