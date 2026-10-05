//go:build !windows

// Purpose: the fanout-resume registration in process over a test-built
//
//	daemonWiring: TestDaemonResumeExecutorUnavailable (acceptance: no
//	scan, the sweep backstop still runs), the three-way wiring rule, the
//	claim table shared with the conductor handler's fan-out, the
//	classify-only scan, and the supervised sweep expiring an unattached
//	fan-out after fanout_record_ttl.
//
// Constraints: untagged and offline (no net): the store is the in-memory
//
//	storetest store, status.get is dispatched through the real registry.
//
// SPORT: cmd/cascade/daemon (CHANGE, P1-CORE-15).
package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/fleet/resume"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// resumeSeedInstant is when seeded fan-outs were last written.
var resumeSeedInstant = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

// seedResumable writes a resumable fan-out (cursor and request record, no
// leg finished) the way the producer does, at resumeSeedInstant.
func seedResumable(t *testing.T, store provider.Store, id string) {
	t.Helper()
	js := journal.New(store, testkit.NewFrozenClock(resumeSeedInstant), journal.DefaultNamespace)
	fs, err := resume.NewFanOutStore(js, store)
	if err != nil {
		t.Fatal(err)
	}
	req := provider.ModelRequest{TaskID: "cli-chat", FanOut: 2}
	if _, err := fs.WriteCursor(context.Background(), resume.FanOutCursor{FanOutID: id, TaskID: req.TaskID, Legs: 2, RequestKey: id}); err != nil {
		t.Fatal(err)
	}
	if err := fs.PutRequest(context.Background(), id, req); err != nil {
		t.Fatal(err)
	}
}

// resumeWiring is a test-built wiring with status.get registered, clock
// and the given store and conductor fan-out.
func resumeWiring(ctx context.Context, store provider.Store, fan daemon.ConductorFanOut, clock runtime.Clock) *daemonWiring {
	w := &daemonWiring{Ctx: ctx, Registry: rpc.NewRegistry(), Manifest: daemon.NewManifest(nil, clock), Clock: clock,
		Store: store, ConductorFanOut: fan}
	w.Connections = registerStatusHandler(w.Registry, clock, w.Manifest, daemon.Settings{})
	return w
}

// fanOutResumeRegistration returns the production "fanout-resume" entry.
func fanOutResumeRegistration(t *testing.T) daemonRegistration {
	t.Helper()
	for _, r := range daemonRegistrations {
		if r.Name == fanOutResumeSubsystem {
			return r
		}
	}
	t.Fatal("no fanout-resume daemon registration")
	return daemonRegistration{}
}

// subsystem dispatches status.get through w's registry and returns name's
// entry (zero when absent).
func subsystem(t *testing.T, w *daemonWiring, name string) daemon.SubsystemStatus {
	t.Helper()
	res, errObj := w.Registry.Dispatch(context.Background(), &rpc.Request{Method: daemon.StatusMethod})
	if errObj != nil {
		t.Fatalf("status.get: %+v", errObj)
	}
	raw, _ := json.Marshal(res)
	var st daemon.StatusResponse
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("decode status.get: %v", err)
	}
	for _, s := range st.Subsystems {
		if s.Name == name {
			return s
		}
	}
	return daemon.SubsystemStatus{}
}

// fanOutState reads id's fan-out state and its kept record count.
func fanOutState(t *testing.T, store provider.Store, id string) (resume.FanOutState, int) {
	t.Helper()
	fs, _ := resume.NewFanOutStore(journal.New(store, runtime.SystemClock{}, journal.DefaultNamespace), store)
	st, err := fs.State(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	_, found, err := fs.GetRequest(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		return st, 1
	}
	return st, 0
}

func TestDaemonResumeExecutorUnavailable(t *testing.T) {
	store, id := storetest.NewMemStore(), "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	seedResumable(t, store, id)
	clock := testkit.NewFrozenClock(resumeSeedInstant.Add(time.Minute))
	fan := daemon.NewConductorFanOut(store, clock) // a FanOutStore failure keeps the journal and claim table
	fan.Err = cascade.New(cascade.KindUnavailable, "injected: executor construction failed")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := resumeWiring(ctx, store, fan, clock)
	w.Deps.Config = resumeConfig("1h", "10ms")
	before := len(journalEntries(t, store, id))
	if err := fanOutResumeRegistration(t).Wire(w); err != nil {
		t.Fatalf("fanout-resume with the executor unavailable: %v, want nil (the daemon keeps serving)", err)
	}
	if got := subsystem(t, w, fanOutResumeSubsystem); got.State != daemon.SubsystemSkipped || got.Detail != executorUnavailable {
		t.Fatalf("status.get fanout-resume = %+v, want skipped %q", got, executorUnavailable)
	}
	if sweep := subsystem(t, w, fanOutSweepSubsystem); sweep.State != daemon.SubsystemRunning {
		t.Fatalf("fanout-sweep = %+v, want running: the backstop still expires at fanout_record_ttl (R8d m-3)", sweep)
	}
	time.Sleep(50 * time.Millisecond) // several sweeps inside the ttl
	st, kept := fanOutState(t, store, id)
	if st.Final != "" || kept != 1 || len(journalEntries(t, store, id)) != before || len(st.Starts) != 0 {
		t.Fatalf("marker %q, request records %d, entries %d -> %d, starts %v; want no marker, the record kept, nothing dispatched",
			st.Final, kept, before, len(journalEntries(t, store, id)), st.Starts)
	}
	clock.Advance(2 * time.Hour)
	waitFor(t, "the expired marker", func() bool { st, _ := fanOutState(t, store, id); return st.Final == resume.OutcomeExpired })
	if st, kept := fanOutState(t, store, id); kept != 0 || len(st.Starts) != 0 {
		t.Fatalf("after the ttl: records %d, starts %v; want every record deleted and nothing dispatched", kept, st.Starts)
	}
	cancel()
	w.Manifest.Wait()
}

// journalEntries replays id's fan-out entity.
func journalEntries(t *testing.T, store provider.Store, id string) []journal.Entry {
	t.Helper()
	js := journal.New(store, runtime.SystemClock{}, journal.DefaultNamespace)
	es, err := js.Replay(context.Background(), resume.FanOutEntity(id), journal.Cursor{EntityID: resume.FanOutEntity(id)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return es
}

func TestFanOutResumeWiringRule(t *testing.T) {
	ctx, clock := context.Background(), testkit.NewFrozenClock(resumeSeedInstant)
	w := resumeWiring(ctx, nil, daemon.ConductorFanOut{}, clock)
	if err := wireFanOutResume(w); err != nil || subsystem(t, w, fanOutResumeSubsystem).Detail != "no daemon store" {
		t.Fatalf("no store: err %v, status %+v; want nil and Skipped \"no daemon store\"", err, subsystem(t, w, fanOutResumeSubsystem))
	}
	full := daemon.NewConductorFanOut(storetest.NewMemStore(), clock)
	for name, fan := range map[string]daemon.ConductorFanOut{
		"zero fan-out": {},
		"nil claims":   {Journal: full.Journal, Results: full.Results, Budget: full.Budget},
		"nil journal":  {Results: full.Results, Budget: full.Budget, Claims: full.Claims},
	} {
		err := wireFanOutResume(resumeWiring(ctx, storetest.NewMemStore(), fan, clock))
		if err != resume.ErrConstructionFailed || err.Error() != resume.ErrConstructionFailed.Error() { //nolint:errorlint // identity: errors.Is compares Kind only
			t.Fatalf("%s: %v, want resume.ErrConstructionFailed (startup refuses)", name, err)
		}
	}
	for name, fan := range map[string]daemon.ConductorFanOut{ // the claim/journal guard, not Scan, decides these
		"unavailable, nothing built":  daemon.NewConductorFanOut(nil, nil),
		"unavailable, no claim table": {Journal: full.Journal, Err: cascade.New(cascade.KindUnavailable, "injected")},
	} {
		w := resumeWiring(ctx, storetest.NewMemStore(), fan, clock)
		if err := wireFanOutResume(w); err != nil || subsystem(t, w, fanOutResumeSubsystem).Detail != executorUnavailable ||
			subsystem(t, w, fanOutSweepSubsystem).Name != "" {
			t.Fatalf("%s: err %v, resume %+v, sweep %+v; want nil, Skipped %q and no sweep (nothing to sweep with)",
				name, err, subsystem(t, w, fanOutResumeSubsystem), subsystem(t, w, fanOutSweepSubsystem), executorUnavailable)
		}
	}
	bad := resumeWiring(ctx, storetest.NewMemStore(), full, clock)
	bad.Deps.Config = &runtime.Config{Extra: map[string]interface{}{"daemon": map[string]interface{}{"fanout_record_ttl": "never"}}}
	if err := wireFanOutResume(bad); !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(err.Error(), "fanout_record_ttl") {
		t.Fatalf("bad fanout_record_ttl: %v, want KindInvalidInput naming the key", err)
	}
}
