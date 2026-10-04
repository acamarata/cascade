//go:build !windows && integration

// Purpose: P1-CORE-19 acceptance for the fan-out lifecycle edges: leg
//
//	budget, client cancel, unwired and cursor-write refusals, finalize under
//	cancel, the claim table against live calls, foreign entities, and
//	re-attach (with its attempt cap) over a store left unfinalized.
//
// SPORT: cmd/cascade/daemon (CHANGE, P1-CORE-19).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/fleet/resume"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

const reqR = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

// waitEntered waits for one provider call to start.
func waitEntered(t *testing.T, p *recordedProvider) {
	t.Helper()
	select {
	case <-p.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("no provider call started")
	}
}

// async runs one call in the background.
func async(ctx context.Context, t *testing.T, r *fanRig, p map[string]any) chan string {
	ch := make(chan string, 1)
	go func() { _, kind, msg := r.call(ctx, t, p); ch <- kind + "|" + msg }()
	return ch
}

func TestConductorExecuteFanOutRespectsBudget(t *testing.T) {
	r := newFanRig(t, rigOpts{block: true, edit: func(f *daemon.ConductorFanOut) { f.Budget = conductor.NewLegBudget(1) }})
	ctx, cancel := context.WithCancel(context.Background())
	first := async(ctx, t, r, fanParams(3, "", "budget"))
	waitEntered(t, r.prov)
	select {
	case <-r.prov.entered:
		t.Fatal("a second leg entered the provider while the budget's one slot was held")
	case <-time.After(300 * time.Millisecond):
	}
	if got := r.prov.peak.Load(); got != 1 {
		t.Fatalf("peak in flight %d under budget 1, want 1", got)
	}
	cancel()
	<-first
	// No wait and no peak reset across the cancel: the next call starts as
	// soon as the cancelled one returns, so a leg request that outlives its
	// budget slot shows up at the server as two in flight.
	second := async(context.Background(), t, r, fanParams(2, "", "after cancel"))
	waitEntered(t, r.prov) // the cancelled call released its slot
	r.prov.release()
	// The cancelled leg's request must also end at the server by its own
	// r.Context() (the client aborted it), not by a later release.
	if res := <-second; res != "|" || r.prov.peak.Load() != 1 || r.prov.ctxDone.Load() != 1 {
		t.Fatalf("second call %q, peak in flight %d (calls %d, in flight now %d, server-cancelled %d); want delivery, peak 1 and 1 server-cancelled across the cancel",
			res, r.prov.peak.Load(), r.prov.calls.Load(), r.prov.inflight.Load(), r.prov.ctxDone.Load())
	}
}

func TestConductorExecuteFanOutClientCancelFinalizes(t *testing.T) {
	r := newFanRig(t, rigOpts{block: true})
	ctx, cancel := context.WithCancel(context.Background())
	done := async(ctx, t, r, fanParams(3, reqR, "cancel me"))
	waitEntered(t, r.prov)
	cancel()
	<-done
	st, err := r.fan.Results.State(context.Background(), reqR)
	legs, reqs := r.counts(t)
	if err != nil || st.Final != resume.OutcomeCancelled || legs+reqs != 0 {
		t.Fatalf("state %+v err %v records %d; want a cancelled marker and no records", st, err, legs+reqs)
	}
	scan, err := resume.Scan(context.Background(), resume.FanOutDeps{Store: r.store, Heads: r.fan.Journal, Claims: resume.NewClaims(), Clock: r.clock})
	if err != nil || len(scan) != 1 || scan[0].FanOutID != reqR || scan[0].Class != resume.FanOutFinalMarked || scan[0].Err != nil {
		t.Fatalf("Scan = %+v, %v; want exactly one final-marked verdict for the cancelled fan-out", scan, err)
	}
}

func TestConductorExecuteFanOutUnwiredRefuses(t *testing.T) {
	for name, edit := range map[string]func(*daemon.ConductorFanOut){
		"nil journal": func(f *daemon.ConductorFanOut) { f.Journal = nil },
		"nil results": func(f *daemon.ConductorFanOut) { f.Results = nil },
		"nil budget":  func(f *daemon.ConductorFanOut) { f.Budget = nil },
		"nil claims":  func(f *daemon.ConductorFanOut) { f.Claims = nil },
		"failed construction": func(f *daemon.ConductorFanOut) {
			*f = daemon.NewConductorFanOut(nil, nil)
		},
	} {
		r, ctx := newFanRig(t, rigOpts{edit: edit}), context.Background()
		if _, kind, _ := r.call(ctx, t, fanParams(3, "", "p")); kind != "unavailable" || r.prov.calls.Load() != 0 {
			t.Fatalf("%s: kind %q calls %d; want unavailable and zero provider calls", name, kind, r.prov.calls.Load())
		}
		detail := ""
		for _, s := range r.manifest.Snapshot() {
			if s.Name == "conductor.executor" {
				detail = s.Detail
			}
		}
		if !strings.Contains(detail, "fan-out unavailable") {
			t.Fatalf("%s: manifest detail %q, want the fan-out unavailable clause", name, detail)
		}
		single := fanParams(1, "", "p")
		if res, kind, msg := r.call(ctx, t, single); kind != "" || res == nil || r.prov.calls.Load() != 1 {
			t.Fatalf("%s: fan_out=1 = %s %s, calls %d; want the single Execute path to serve", name, kind, msg, r.prov.calls.Load())
		}
	}
}

// failTxStore fails every transaction: the cursor append cannot land.
type failTxStore struct{ provider.Store }

func (failTxStore) Tx(context.Context, func(context.Context, provider.Tx) error) error {
	return cascade.New(cascade.KindInternal, "injected: store write failed")
}

func TestConductorExecuteFanOutCursorFirst(t *testing.T) {
	r := newFanRig(t, rigOpts{store: failTxStore{Store: storetest.NewMemStore()}})
	if _, kind, msg := r.call(context.Background(), t, fanParams(3, "", "p")); kind != "unavailable" || r.prov.calls.Load() != 0 {
		t.Fatalf("kind %q (%s), calls %d; want unavailable and zero provider calls", kind, msg, r.prov.calls.Load())
	}
}

// failAckJournal fails the final-marker append (KindAck).
type failAckJournal struct{ journal.Store }

func (j failAckJournal) Append(ctx context.Context, id string, k journal.Kind, op string, p json.RawMessage) (journal.Entry, error) {
	if err := ctx.Err(); err != nil { // a done ctx fails the append, as the real store does
		return journal.Entry{}, err
	}
	if k == journal.KindAck {
		return journal.Entry{}, errors.New("injected marker failure")
	}
	return j.Store.Append(ctx, id, k, op, p)
}

func TestFanOutFinalizeSurvivesCancel(t *testing.T) {
	store := storetest.NewMemStore()
	r := newFanRig(t, rigOpts{store: store, block: true, edit: func(f *daemon.ConductorFanOut) {
		js := journal.New(store, runtime.NewSystemClock(), journal.DefaultNamespace)
		f.Results, _ = resume.NewFanOutStore(failAckJournal{Store: js}, ctxStore{Store: store})
	}})
	ctx, cancel := context.WithCancel(context.Background())
	done := async(ctx, t, r, fanParams(2, reqR, "p"))
	waitEntered(t, r.prov)
	cancel()
	res := <-done
	legs, reqs := r.counts(t)
	if !strings.Contains(res, "injected marker failure") || !strings.Contains(res, "canceled") || legs+reqs != 0 {
		t.Fatalf("result %q, records %d; want both causes joined and DeleteTask run", res, legs+reqs)
	}
}

// failDeleteStore keeps every record: a process that died before finalize.
type failDeleteStore struct{ provider.Store }

func (failDeleteStore) Delete(context.Context, string, string) error {
	return errors.New("injected: process died")
}

// crashedFanOut leaves fan-out reqR unfinalized in a fresh store: leg 0
// done ok, legs 1-2 started, cursor and request record kept (the marker
// and deletes fail, standing in for process death before finalize).
func crashedFanOut(t *testing.T, prompt string) provider.Store {
	t.Helper()
	store := storetest.NewMemStore()
	r := newFanRig(t, rigOpts{store: store, block: true, edit: func(f *daemon.ConductorFanOut) {
		js := journal.New(store, runtime.NewSystemClock(), journal.DefaultNamespace)
		f.Results, _ = resume.NewFanOutStore(failAckJournal{Store: js}, failDeleteStore{Store: store})
	}})
	r.prov.free.Store(1)
	ctx, cancel := context.WithCancel(context.Background())
	done := async(ctx, t, r, fanParams(3, reqR, prompt))
	for i := 0; i < 3; i++ {
		waitEntered(t, r.prov)
	}
	for i := 0; i < 500; i++ {
		if st, _ := r.fan.Results.State(context.Background(), reqR); len(st.Completed) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	return store
}

func TestConductorExecuteFanOutReattach(t *testing.T) {
	store, pol := crashedFanOut(t, "reattach prompt"), &countingPolicy{}
	r, ctx := newFanRig(t, rigOpts{store: store, policy: pol}), context.Background()
	if _, kind, _ := r.call(ctx, t, fanParams(3, reqR, "changed prompt")); kind != "conflict" || r.prov.calls.Load() != 0 {
		t.Fatalf("changed prompt: kind %q calls %d; want conflict and zero calls", kind, r.prov.calls.Load())
	}
	auths := pol.n.Load()
	res, kind, msg := r.call(ctx, t, fanParams(3, reqR, "reattach prompt"))
	if got := pol.n.Load() - auths; got != 4 { // parent, replayed leg 0, dispatched legs 1 and 2
		t.Fatalf("re-attach ran AuthorizeFn %d times, want 4 (parent, one replay, two legs)", got)
	}
	legs, reqs := r.counts(t)
	st, _ := r.fan.Results.State(ctx, reqR)
	if kind != "" || len(res["legs"].([]any)) != 3 || r.prov.calls.Load() != 2 || st.Final != resume.OutcomeDelivered || legs+reqs != 0 {
		t.Fatalf("re-attach: %s %s calls %d marker %q records %d; want 3 outputs, 2 calls, delivered, none", kind, msg, r.prov.calls.Load(), st.Final, legs+reqs)
	}
	if _, kind, msg := r.call(ctx, t, fanParams(3, reqR, "reattach prompt")); kind != "conflict" || !strings.Contains(msg, "delivered") {
		t.Fatalf("re-attach to delivered: %s %s; want conflict naming delivered", kind, msg)
	}
}

func TestConductorExecuteFanOutReattachAttemptCap(t *testing.T) {
	store, pol := crashedFanOut(t, "cap prompt"), &countingPolicy{}
	r, ctx := newFanRig(t, rigOpts{store: store, policy: pol}), context.Background()
	leg := 1 // any leg the crashed run did not finish (which leg answered first is a race)
	if st, _ := r.fan.Results.State(ctx, reqR); len(st.Completed) == 1 {
		if _, done := st.Completed[1]; done {
			leg = 2
		}
	}
	for st, _ := r.fan.Results.State(ctx, reqR); st.Starts[leg] < resume.MaxLegStarts; st, _ = r.fan.Results.State(ctx, reqR) {
		if _, err := r.fan.Results.AppendLeg(ctx, "fanout_leg_started", reqR, leg, nil); err != nil {
			t.Fatal(err)
		}
	}
	before := kindCount(r.entries(t, reqR), journal.KindFanOutLegDone)
	_, kind, _ := r.call(ctx, t, fanParams(3, reqR, "cap prompt"))
	if got := pol.n.Load(); got != 1 {
		t.Fatalf("capped re-attach ran AuthorizeFn %d times, want 1 (the parent; zero replays)", got)
	}
	es := r.entries(t, reqR)
	st, _ := r.fan.Results.State(ctx, reqR)
	if kind != "conflict" || r.prov.calls.Load() != 0 || st.Final != resume.OutcomeUnknown || kindCount(es, journal.KindFanOutLegDone) != before {
		t.Fatalf("kind %q calls %d marker %q; want conflict, zero calls, zero replays, unknown_outcome", kind, r.prov.calls.Load(), st.Final)
	}
	seen := map[string]bool{}
	for _, e := range es {
		if e.Kind == journal.KindFanOutLegStarted && strings.HasPrefix(e.OperationID, reqR+"#"+string(rune('0'+leg))+"#") {
			seen[e.OperationID] = true
		}
	}
	if len(seen) != resume.MaxLegStarts {
		t.Fatalf("leg %d start opIDs %v, want %d distinct attempts", leg, seen, resume.MaxLegStarts)
	}
}

func TestConductorExecuteFanOutReattachWhileLiveRefuses(t *testing.T) {
	r := newFanRig(t, rigOpts{block: true})
	live := async(context.Background(), t, r, fanParams(3, reqR, "live"))
	for i := 0; i < 3; i++ { // every live leg is held before the count is taken
		waitEntered(t, r.prov)
	}
	n := r.prov.calls.Load()
	if _, kind, msg := r.call(context.Background(), t, fanParams(3, reqR, "live")); kind != "conflict" || !strings.Contains(msg, "fan-out in progress") || r.prov.calls.Load() != n {
		t.Fatalf("re-attach while live: %s %s; want conflict \"fan-out in progress\" and no new call", kind, msg)
	}
	r.prov.release()
	if res := <-live; res != "|" {
		t.Fatalf("live call: %q, want delivery", res)
	}
	r2 := newFanRig(t, rigOpts{store: crashedFanOut(t, "twice"), block: true})
	first := async(context.Background(), t, r2, fanParams(3, reqR, "twice"))
	for i := 0; i < 2; i++ { // the two legs the crash left unfinished
		waitEntered(t, r2.prov)
	}
	n = r2.prov.calls.Load()
	if _, kind, _ := r2.call(context.Background(), t, fanParams(3, reqR, "twice")); kind != "conflict" || r2.prov.calls.Load() != n {
		t.Fatalf("second concurrent re-attach: kind %q; want conflict and zero calls", kind)
	}
	r2.prov.release()
	if res := <-first; res != "|" {
		t.Fatalf("first re-attach: %q, want delivery", res)
	}
}
