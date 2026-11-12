package delegation

// Purpose: the daemon-bound seams: a Fleet bound through WithFleet, the supervised
// collector Delegate starts once a collection is bound, and the origin-checked Wait.

import (
	"context"
	"errors"
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

// manualTicker is a runtime.Ticker that never fires; it counts Stop calls.
type manualTicker struct {
	c     chan struct{}
	stops int
}

func (m *manualTicker) C() <-chan struct{} { return m.c }
func (m *manualTicker) Stop()              { m.stops++ }

// driverShim spawns a handle over driver and counts handle releases.
type driverShim struct {
	driver   provider.AgentProvider
	releases int
}

func (d *driverShim) Spawn(_ context.Context, r agents.SpawnRequest) (agents.SpawnHandle, error) {
	return agents.SpawnHandle{JobID: r.Job.JobID, Driver: d.driver,
		Release: func(context.Context) error { d.releases++; return nil }}, nil
}

// commitFleet refuses Commit with err and logs every call.
type commitFleet struct {
	fleetDouble
	err error
}

func (f *commitFleet) Commit(context.Context, capacity.Lease) error {
	f.calls = append(f.calls, "commit")
	return f.err
}

type wired struct {
	chain    *chain
	parent   jobs.DelegationRecord
	svc      *Service
	shim     *driverShim
	manifest *daemon.Manifest
	notify   *notify.Service
	tickers  []*manualTicker
}

// newWired builds a Service through WithFleet over fleet and binds a collection.
func newWired(t *testing.T, fleet Leaser) *wired {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	c := newChain(t)
	_, parent := c.add(c.root)
	clock := runtime.NewFixedClock(t0)
	w := &wired{chain: c, parent: parent, manifest: daemon.NewManifest(nil, clock),
		notify: notify.NewService(notify.DefaultConfig(), clock, nil),
		shim:   &driverShim{driver: &collectDriver{status: func(context.Context) provider.AgentRunState { return provider.AgentRunAccepted }}}}
	w.svc = NewService(Deps{Clock: clock, Store: c.st, Pinned: c.s.PublicKey(), Signer: c.s,
		Minter: principal.NewMinter(clock), Shim: w.shim}.WithFleet(fleet).WithFleet(nil))
	w.svc.SetAgentLanes([]capacity.AgentLane{{DriverID: "drv-1", Posture: provider.CompliancePosture{ProgrammaticEntitlement: true}}})
	err := w.svc.BindCollection(Collection{Ctx: context.Background(), Supervisor: w.manifest, Notify: w.notify,
		MaxWallClock: time.Hour, Ticks: func() runtime.Ticker {
			m := &manualTicker{c: make(chan struct{})}
			w.tickers = append(w.tickers, m)
			return m
		}})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *wired) delegate(t *testing.T) (*Response, *Worktree) {
	t.Helper()
	wt, err := AllocWorktree(t.TempDir(), "wired", false)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := w.svc.Delegate(context.Background(), &Request{AgentID: "wired-child", Parent: w.parent, Worktree: wt})
	if err != nil {
		t.Fatal(err)
	}
	return resp, wt
}

func TestDelegateWithBoundFleetStartsCollector(t *testing.T) {
	f := &fleetDouble{}
	w := newWired(t, f)
	resp, wt := w.delegate(t)
	w.manifest.Wait()
	if _, err := os.Stat(wt.Root); !os.IsNotExist(err) {
		t.Fatalf("the collector left the worktree: %v", err)
	}
	rec, err := w.chain.st.GetDelegation(context.Background(), resp.Record.Env.ID)
	if err != nil || rec.State != jobs.DelegationSucceeded {
		t.Fatalf("record = %+v, %v; want succeeded", rec, err)
	}
	if got := strings.Join(f.calls, ","); got != "commit,release" || w.shim.releases != 1 {
		t.Fatalf("fleet calls %q, handle releases %d; want commit,release and 1", got, w.shim.releases)
	}
	if len(w.tickers) != 1 || w.tickers[0].stops != 1 {
		t.Fatalf("collector tickers = %d; want one, stopped once", len(w.tickers))
	}
	w.notify.Dispatcher.Drain(context.Background())
	owner := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	got, err := w.svc.Wait(owner, rec.ID, time.Minute)
	if err != nil || got.Output != "finished" || got.State != jobs.DelegationSucceeded || got.JobID != rec.ID {
		t.Fatalf("Wait = %+v, %v; want the finished result", got, err)
	}
	if len(w.tickers) != 2 || w.tickers[1].stops != 1 {
		t.Fatal("Wait did not stop its ticker")
	}
}

func TestWaitRefusesForeignOrigin(t *testing.T) {
	w := newWired(t, &fleetDouble{})
	resp, _ := w.delegate(t)
	w.manifest.Wait()
	w.notify.Dispatcher.Drain(context.Background())
	intruder := principal.WithCaller(context.Background(), principal.Caller{Principal: &principal.Principal{ID: "intruder"}})
	got, err := w.svc.Wait(intruder, resp.Record.Env.ID, time.Minute)
	wantCascadeErr(t, err, cascade.KindPermissionDenied, "only the delegation origin may wait")
	if got.Output != "" {
		t.Fatalf("a refused Wait leaked %q", got.Output)
	}
	owner := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	if ok, err := w.svc.Wait(owner, resp.Record.Env.ID, time.Minute); err != nil || ok.Output != "finished" {
		t.Fatalf("origin Wait = %+v, %v; want the result", ok, err)
	}
}

func TestWaitAndCollectorNeedABoundCollection(t *testing.T) {
	c := newChain(t)
	_, parent := c.add(c.root)
	svc, f, _ := c.service(t0)
	_, err := svc.Wait(context.Background(), "any", time.Second)
	wantCascadeErr(t, err, cascade.KindInvalidInput, "Wait: no result collection is bound")
	resp, err := svc.Delegate(context.Background(), &Request{AgentID: "plain", Parent: parent})
	if err != nil || strings.Join(f.calls, ",") != "commit" {
		t.Fatalf("Delegate = %+v, %v (calls %v); want a commit and no collector", resp, err, f.calls)
	}
	for _, bad := range []Collection{{}, {Ctx: context.Background(), Supervisor: daemon.NewManifest(nil, fakeClock{now: t0}),
		Notify: &notify.Service{}, Ticks: func() runtime.Ticker { return nil }, MaxWallClock: time.Hour}} {
		err = svc.BindCollection(bad)
		wantCascadeErr(t, err, cascade.KindInvalidInput, "BindCollection: a context, supervisor")
	}
}

func TestDelegateCommitRefusalUnwindsSpawn(t *testing.T) {
	boom := cascade.New(cascade.KindConflict, "commit refused")
	f := &commitFleet{err: boom}
	w := newWired(t, f)
	_, err := w.svc.Delegate(context.Background(), &Request{AgentID: "wired-child", Parent: w.parent})
	if !errors.Is(err, boom) || err.Error() != boom.Error() {
		t.Fatalf("Delegate err = %v; want the commit refusal", err)
	}
	live, err := w.chain.st.LiveDelegations(context.Background())
	if err != nil || len(live) != 1 || live[0].ID != w.parent.ID {
		t.Fatalf("live = %+v, %v; want only the parent", live, err)
	}
	if w.shim.releases != 1 || len(w.tickers) != 0 {
		t.Fatalf("handle releases %d, tickers %d; want 1 and 0", w.shim.releases, len(w.tickers))
	}
}

func TestStartedUnreadableRecordReleasesLease(t *testing.T) {
	f := &fleetDouble{}
	w := newWired(t, f)
	child := envelope.Signed{Env: envelope.Envelope{ID: "never-stored", Principal: "ghost"}}
	wt, err := AllocWorktree(t.TempDir(), "unread", false)
	if err != nil {
		t.Fatal(err)
	}
	err = w.svc.started(context.Background(), &Request{Worktree: wt}, child, capacity.Lease{ID: "l-1"}, agents.SpawnHandle{})
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("started = %v; want KindNotFound", err)
	}
	if _, statErr := os.Stat(wt.Root); !os.IsNotExist(statErr) {
		t.Fatalf("worktree remains: %v", statErr)
	}
	if strings.Join(f.calls, ",") != "commit,release" || len(w.tickers) != 0 {
		t.Fatalf("fleet calls %v, tickers %d; want commit,release and none", f.calls, len(w.tickers))
	}
}
