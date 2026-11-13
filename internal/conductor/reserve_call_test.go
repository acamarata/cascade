package conductor

// Purpose (this file): the call-reservation door. Execute and ExecuteStream
//   reserve an unreserved call, check a named one, release only what they
//   reserved, and refuse with zero provider calls when no hold exists.

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// passReserver is the shared test reserver newReadyConfig installs: every
// hold runs fn directly, releases nothing, and a checked hold echoes its id.
type passReserver struct{}

func (passReserver) Reserve(context.Context, provider.Selection, provider.ModelRequest) (HeldCall, error) {
	return passHold{id: "pass"}, nil
}

func (passReserver) Check(_ context.Context, id string, _ string, _ provider.Selection) (HeldCall, error) {
	return passHold{id: id}, nil
}

type passHold struct{ id string }

func (h passHold) ID() string { return h.id }

func (passHold) WithPermit(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (passHold) Release(context.Context, provider.Usage, bool) error { return nil }

// recReserver records, in order, every reserve, check, adapter call and
// release; an adapter call made outside a held permit is logged as such.
type recReserver struct {
	mu       sync.Mutex
	log      []string
	permit   bool
	sel      provider.Selection
	usage    provider.Usage
	reported bool
	checkErr error
	checkID  string // the id a checked hold reports; "" echoes the asked id
}

func (r *recReserver) note(ev string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ev == "call" && !r.permit {
		ev = "call-outside-permit"
	}
	r.log = append(r.log, ev)
}

func (r *recReserver) events() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.log...)
}

func (r *recReserver) Reserve(_ context.Context, sel provider.Selection, _ provider.ModelRequest) (HeldCall, error) {
	r.note("reserve")
	r.sel = sel
	return recHold{r: r, id: "own-1"}, nil
}

func (r *recReserver) Check(_ context.Context, id string, _ string, _ provider.Selection) (HeldCall, error) {
	r.note("check")
	if r.checkErr != nil {
		return nil, r.checkErr
	}
	if r.checkID != "" {
		id = r.checkID
	}
	return recHold{r: r, id: id}, nil
}

type recHold struct {
	r  *recReserver
	id string
}

func (h recHold) ID() string { return h.id }

func (h recHold) setPermit(on bool) {
	h.r.mu.Lock()
	defer h.r.mu.Unlock()
	h.r.permit = on
}

func (h recHold) WithPermit(ctx context.Context, fn func(context.Context) error) error {
	h.setPermit(true)
	defer h.setPermit(false)
	return fn(ctx)
}

func (h recHold) Release(_ context.Context, u provider.Usage, reported bool) error {
	h.r.note("release")
	h.r.mu.Lock()
	defer h.r.mu.Unlock()
	h.r.usage, h.r.reported = u, reported
	return nil
}

// adapter makes p log each call to r and answer with u, or with fail.
func (r *recReserver) adapter(p *fakeProvider, u provider.Usage, fail error) {
	p.chatFn = func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		r.note("call")
		if fail != nil {
			return provider.ChatResponse{}, fail
		}
		return provider.ChatResponse{Message: provider.ChatMessage{Role: "assistant", Content: "ok"}, Usage: u}, nil
	}
	p.streamFn = func(_ context.Context, _ provider.ChatRequest, sink provider.StreamSink) error {
		r.note("call")
		if fail != nil {
			return fail
		}
		if err := sink(provider.StreamEvent{Kind: provider.StreamEventUsage, Usage: u}); err != nil {
			return err
		}
		return sink(provider.StreamEvent{Kind: provider.StreamEventDone})
	}
}

func newReserverExecutor(t *testing.T, r CallReserver, policyErr error) (*Executor, *readyDeps) {
	t.Helper()
	cfg, deps := newReadyConfig(t)
	cfg.Reserver = r
	if policyErr != nil {
		cfg.Policy = &fakePolicy{err: policyErr}
	}
	exec, err := NewExecutor(cfg)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	return exec, deps
}

// dispatchVerb runs req through Execute or ExecuteStream to completion and
// returns the refusal, the dispatch error or the stream's error event.
func dispatchVerb(ctx context.Context, exec *Executor, verb string, req provider.ModelRequest) error {
	if verb == "execute" {
		_, err := exec.Execute(ctx, req)
		return err
	}
	ch, cancel, err := exec.ExecuteStream(ctx, req)
	if err != nil {
		return err
	}
	defer cancel()
	var streamErr error
	for ev := range ch {
		if ev.Kind == provider.StreamEventError {
			streamErr = ev.Err
		}
	}
	return streamErr
}

var reserveVerbs = []string{"execute", "stream"}

func TestExecuteSelfReservesUnreservedCall(t *testing.T) {
	cases := []struct {
		name  string
		usage provider.Usage
		fail  error
	}{
		{"usage", provider.Usage{InputTokens: 3, OutputTokens: 4}, nil},
		{"provider failure", provider.Usage{}, cascade.New(cascade.KindUnavailable, "adapter down")},
		{"zero usage", provider.Usage{}, nil},
	}
	wantSel := provider.Selection{LaneID: "lane-1", Provider: "test", Model: "test-model"}
	for _, verb := range reserveVerbs {
		for _, c := range cases {
			t.Run(verb+"/"+c.name, func(t *testing.T) {
				rec := &recReserver{}
				exec, deps := newReserverExecutor(t, rec, nil)
				rec.adapter(deps.prov, c.usage, c.fail)
				err := dispatchVerb(context.Background(), exec, verb, validReq())
				if (err != nil) != (c.fail != nil) {
					t.Fatalf("dispatch error = %v, want failure %v", err, c.fail != nil)
				}
				if got := rec.events(); !reflect.DeepEqual(got, []string{"reserve", "call", "release"}) {
					t.Fatalf("events = %v, want [reserve call release]", got)
				}
				if !reflect.DeepEqual(rec.sel, wantSel) {
					t.Fatalf("Reserve selection = %+v, want %+v", rec.sel, wantSel)
				}
				if rec.usage != c.usage || rec.reported != (c.usage != provider.Usage{}) {
					t.Fatalf("Release(%+v, %v), want (%+v, %v)", rec.usage, rec.reported, c.usage, c.usage != provider.Usage{})
				}
			})
		}
	}
}

func TestExecuteRejectsUnknownReservationID(t *testing.T) {
	for _, verb := range reserveVerbs {
		rec := &recReserver{checkErr: errors.New("no such reservation")}
		exec, deps := newReserverExecutor(t, rec, nil)
		rec.adapter(deps.prov, provider.Usage{}, nil)
		req := validReq()
		req.ReservationID = "r-9"
		err := dispatchVerb(context.Background(), exec, verb, req)
		if !cascade.HasKind(err, cascade.KindPolicyDenied) || !strings.Contains(err.Error(), `"r-9"`) {
			t.Fatalf("%s: error = %v, want KindPolicyDenied naming r-9", verb, err)
		}
		if got := rec.events(); !reflect.DeepEqual(got, []string{"check"}) {
			t.Fatalf("%s: events = %v, want only [check]", verb, got)
		}
	}
}

func TestExecuteRejectsForeignReservationID(t *testing.T) {
	for _, verb := range reserveVerbs {
		rec := &recReserver{checkID: "r-other"}
		exec, deps := newReserverExecutor(t, rec, nil)
		rec.adapter(deps.prov, provider.Usage{InputTokens: 1}, nil)
		req := validReq()
		req.ReservationID = "r-1"
		err := dispatchVerb(context.Background(), exec, verb, req)
		if !cascade.HasKind(err, cascade.KindPolicyDenied) || !strings.Contains(err.Error(), `"r-1"`) {
			t.Fatalf("%s: foreign hold error = %v, want KindPolicyDenied naming r-1", verb, err)
		}
		if got := rec.events(); !reflect.DeepEqual(got, []string{"check"}) {
			t.Fatalf("%s: foreign hold events = %v, want only [check]", verb, got)
		}
		rec.checkID, rec.log = "", nil
		if err := dispatchVerb(context.Background(), exec, verb, req); err != nil {
			t.Fatalf("%s: matching hold: %v", verb, err)
		}
		if got := rec.events(); !reflect.DeepEqual(got, []string{"check", "call"}) {
			t.Fatalf("%s: matching hold events = %v, want [check call] and no release", verb, got)
		}
	}
}

func TestExecuteNilReserverRefuses(t *testing.T) {
	for _, verb := range reserveVerbs {
		rec := &recReserver{}
		exec, deps := newReserverExecutor(t, nil, nil)
		rec.adapter(deps.prov, provider.Usage{}, nil)
		err := dispatchVerb(context.Background(), exec, verb, validReq())
		if !errors.Is(err, ErrUnreservedDispatch) || err.Error() != ErrUnreservedDispatch.Error() || !strings.Contains(err.Error(), "conductor: dispatch has no call reservation") {
			t.Fatalf("%s: error = %v, want ErrUnreservedDispatch", verb, err)
		}
		if got := rec.events(); len(got) != 0 {
			t.Fatalf("%s: adapter calls = %v, want none", verb, got)
		}
		if n := deps.audit.count(); n != 1 || deps.audit.events[0].Verdict != "refusal" {
			t.Fatalf("%s: audit count = %d, want one refusal", verb, n)
		}
	}
}

func TestOwnerDenyPrecedesReservation(t *testing.T) {
	for _, verb := range reserveVerbs {
		for _, id := range []string{"", "r-1"} {
			rec := &recReserver{}
			exec, deps := newReserverExecutor(t, rec, errors.New("owner denied task class chat"))
			rec.adapter(deps.prov, provider.Usage{}, nil)
			req := validReq()
			req.ReservationID = id
			err := dispatchVerb(context.Background(), exec, verb, req)
			if !cascade.HasKind(err, cascade.KindPolicyDenied) {
				t.Fatalf("%s %q: error = %v, want KindPolicyDenied", verb, id, err)
			}
			if got := rec.events(); len(got) != 0 {
				t.Fatalf("%s %q: events = %v, want no Reserve, Check or call", verb, id, got)
			}
		}
	}
}

func TestReserveRefusalKeepsGovernorSentinel(t *testing.T) {
	cases := map[error]string{
		governor.ErrDraining:            "node draining",
		governor.ErrNoResourceSignal:    "no fresh resource signal",
		governor.ErrStaleResourceSignal: "no fresh resource signal",
		errors.New("plain"):             "reserving call",
	}
	for in, want := range cases {
		got := reserveRefusal(in)
		if !cascade.HasKind(got, cascade.KindUnavailable) || !strings.Contains(got.Error(), want) || !errors.Is(got, in) {
			t.Errorf("reserveRefusal(%v) = %v, want KindUnavailable naming %q wrapping the input", in, got, want)
		}
	}
}
