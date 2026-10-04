package daemon

// Purpose: untagged unit coverage for the conductor.execute fan-out handler
//   (conductor_execute_fanout.go) via rpc.Registry.Dispatch over the in-memory
//   store, a fault-injecting wrapper and an in-process provider (no socket).
// SPORT: internal/daemon (CHANGE, P1-CORE-19; P1-BF-R99).

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/resume"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

const fuID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

// fuStore logs write events (cursor, request, leg, final, delete, call); armed ones fail.
type fuStore struct {
	*storetest.MemStore
	mu   sync.Mutex
	log  []string
	fail sync.Map
}

func (s *fuStore) note(ev string) error {
	if on, _ := s.fail.Load(ev); on == true {
		return cascade.New(cascade.KindUnavailable, "injected "+ev+" failure")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ev != "" {
		s.log = append(s.log, ev)
	}
	return nil
}

// gate classifies a write (v == "" is a Delete) as an event, notes it, runs do.
func (s *fuStore) gate(ns, v string, do func() error) error {
	ev := ""
	switch {
	case v == "": // only a Delete carries no value
		ev = "delete"
	case ns == "conductor.fanout.requests":
		ev = "request"
	case ns == "conductor.fanout.legs":
		ev = "leg"
	case strings.Contains(v, "fanout_final"):
		ev = "final"
	case strings.Contains(v, `"request_key"`):
		ev = "cursor"
	}
	if err := s.note(ev); err != nil {
		return err
	}
	return do()
}
func (s *fuStore) arm(on bool, evs ...string) {
	for _, ev := range evs {
		s.fail.Store(ev, on)
	}
}
func (s *fuStore) Delete(ctx context.Context, ns, key string) error {
	return s.gate(ns, "", func() error { return s.MemStore.Delete(ctx, ns, key) })
}
func (s *fuStore) Tx(ctx context.Context, fn func(context.Context, provider.Tx) error) error {
	return s.MemStore.Tx(ctx, func(c context.Context, tx provider.Tx) error { return fn(c, fuTx{Tx: tx, s: s}) })
}

type fuTx struct {
	provider.Tx
	s *fuStore
}

func (t fuTx) Put(ctx context.Context, ns, k string, v []byte) error {
	return t.s.gate(ns, string(v), func() error { return t.Tx.Put(ctx, ns, k, v) })
}
func (t fuTx) CompareAndSwap(ctx context.Context, ns, k string, old, v []byte) error {
	return t.s.gate(ns, string(v), func() error { return t.Tx.CompareAndSwap(ctx, ns, k, old, v) })
}

// fuProvider is the one lane's registry, quota, resolver and provider.
type fuProvider struct {
	provider.ModelProvider
	fakeRegistryReader
	st    *fuStore
	calls atomic.Int32
	err   error
	hook  func()
}

func (p *fuProvider) Chat(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	p.calls.Add(1)
	_ = p.st.note("call")
	if p.hook != nil {
		p.hook()
	}
	return provider.ChatResponse{Message: provider.ChatMessage{Role: "assistant", Content: "ok"}, FinishReason: "stop"}, p.err
}
func (p *fuProvider) Resolve(context.Context, provider.Selection) (provider.ModelProvider, error) {
	return p, nil
}
func (p *fuProvider) NextLane(context.Context, []conductor.LaneID) (conductor.LaneID, error) {
	return "mem", nil
}
func (p *fuProvider) ListLanes(context.Context) ([]provider.LaneInfo, error) {
	return []provider.LaneInfo{{LaneName: "mem", ProviderName: "mem"}}, nil
}

type fuEnv struct {
	t   *testing.T
	ctx context.Context
	reg *rpc.Registry
	fan ConductorFanOut
	st  *fuStore
	p   *fuProvider
}

func newFuEnv(t *testing.T, mint func() (cascade.ID, error)) *fuEnv {
	t.Helper()
	st := &fuStore{MemStore: storetest.NewMemStore()}
	p, clock, reg := &fuProvider{st: st}, runtime.NewSystemClock(), rpc.NewRegistry()
	fan := NewConductorFanOut(st, clock)
	fan.IDSource = mint
	if err := RegisterConductorExecuteHandler(reg, NewManifest(nil, clock), p, p, p, newTestAuditWriter(t), clock, fullSecurity(t), ConductorAccounting{}, fan); err != nil {
		t.Fatal(err)
	}
	return &fuEnv{t: t, ctx: context.Background(), reg: reg, fan: fan, st: st, p: p}
}
func (e *fuEnv) check(ok bool, format string, args ...any) {
	e.t.Helper()
	if !ok {
		e.t.Fatalf(format, args...)
	}
}

func (e *fuEnv) call(over map[string]any) (fanOutResponse, *rpc.ErrorObject) {
	params := map[string]any{"task_id": "t", "task_class": "chat", "sensitivity": "public", "fan_out": 2, "request_id": fuID,
		"inputs": []map[string]string{{"role": "user", "content": "prompt"}}}
	for k, v := range over {
		params[k] = v
	}
	raw, _ := json.Marshal(params)
	res, eo := e.reg.Dispatch(e.ctx, &rpc.Request{Method: ConductorExecuteMethod, Params: raw})
	got, _ := res.(fanOutResponse)
	return got, eo
}

func (e *fuEnv) final() string {
	st, _ := e.fan.Results.State(e.ctx, fuID)
	return st.Final
}

func (e *fuEnv) gone() bool {
	_, req, _ := e.fan.Results.GetRequest(e.ctx, fuID)
	_, leg, _ := e.fan.Results.GetLegResult(e.ctx, fuID, 0)
	return !req && !leg
}
func (e *fuEnv) want(eo *rpc.ErrorObject, kind cascade.Kind, sub string) {
	e.check(eo != nil, "got success, want %v %q", kind, sub)
	k, _ := cascade.KindFromJSONRPCCode(eo.Code)
	e.check(k == kind && strings.Contains(eo.Message, sub), "got (%v, %q), want %v containing %q", k, eo.Message, kind, sub)
}

func (e *fuEnv) refused(over map[string]any, kind cascade.Kind, sub string) {
	_, eo := e.call(over)
	e.want(eo, kind, sub)
	e.check(len(e.st.log) == 0 && e.p.calls.Load() == 0, "writes %v, calls %d; want none", e.st.log, e.p.calls.Load())
}

// strand runs a call whose finalize fails (evs armed), leaving the fan-out open.
func (e *fuEnv) strand(evs ...string) {
	e.st.arm(true, evs...)
	_, eo := e.call(nil)
	e.st.arm(false, evs...)
	e.check(eo != nil, "stranding call reported success")
}

// refusedReattach re-attaches, expecting a refusal, no new call and marker.
func (e *fuEnv) refusedReattach(kind cascade.Kind, sub, marker string) {
	calls := e.p.calls.Load()
	_, eo := e.call(nil)
	e.want(eo, kind, sub)
	e.check(e.p.calls.Load() == calls && e.final() == marker, "calls %d -> %d, marker %q; want no new call, %q", calls, e.p.calls.Load(), e.final(), marker)
}

func TestFanOutUnit_RefusalsBeforeAnyWrite(t *testing.T) {
	bad := cascade.KindInvalidInput
	newFuEnv(t, nil).refused(map[string]any{"request_id": "not-an-id"}, bad, "request_id")
	newFuEnv(t, nil).refused(map[string]any{"fan_out": conductor.MaxFanOut + 1}, bad, "exceeds the maximum")
	newFuEnv(t, nil).refused(map[string]any{"inputs": []string{}}, bad, "")
	e := newFuEnv(t, nil) // a held claim
	e.fan.Claims.TryClaim(fuID)
	e.refused(nil, cascade.KindConflict, "fan-out in progress")
	e.check(!e.fan.Claims.TryClaim(fuID), "a refused call released a claim it never held")
	e.fan.Claims.Release(fuID)
	_, eo := e.call(nil)
	e.check(eo == nil && e.fan.Claims.TryClaim(fuID), "after release: %+v; the claim must be free again after the call", eo)
	e = newFuEnv(t, nil)                                                      // an entity whose first entry is not a cursor
	_, _ = e.fan.Results.AppendLeg(e.ctx, "fanout_leg_started", fuID, 0, nil) // seq 1 is not a cursor
	e.refused(nil, cascade.KindNotFound, "no fan-out with this request_id")
}

func TestFanOutUnit_DeliversInOrderAndFinalizesWhenCancelled(t *testing.T) {
	e := newFuEnv(t, nil)
	got, eo := e.call(nil)
	e.check(eo == nil && got.RequestID == fuID && len(got.Legs) == 2 && e.p.calls.Load() == 2, "call = (%+v, %+v), calls %d", got, eo, e.p.calls.Load())
	ev := e.st.log
	at := func(name string) int { return slices.Index(ev, name) }
	e.check(at("cursor") == 0 && at("cursor") < at("request") && at("request") < at("call") && at("call") < at("leg") &&
		at("leg") < at("final") && at("final") < at("delete"), "write order = %v; want cursor, request, calls, legs, final, deletes", ev)
	e.check(e.final() == resume.OutcomeDelivered && e.gone(), "marker %q, gone %v; want delivered and cleaned", e.final(), e.gone())
	e = newFuEnv(t, nil) // cancelled mid-call: the detached finalize still marks and cleans
	var cancel context.CancelFunc
	e.ctx, cancel = context.WithCancel(e.ctx)
	e.p.hook = cancel
	_, eo = e.call(nil)
	e.check(eo != nil && e.final() == resume.OutcomeCancelled && e.gone(), "cancelled call: %+v, marker %q, gone %v", eo, e.final(), e.gone())
}

func TestFanOutUnit_MintedIDsAndStoreFailures(t *testing.T) {
	mint := map[string]any{"request_id": ""}
	newFuEnv(t, func() (cascade.ID, error) { return "", errors.New("no entropy") }).refused(mint, cascade.KindInternal, "minting a fan-out id")
	e := newFuEnv(t, func() (cascade.ID, error) { return fuID, nil })
	got, eo := e.call(mint)
	e.check(eo == nil && got.RequestID == fuID, "minted call = (%+v, %+v)", got, eo)
	_, eo = e.call(mint)
	e.want(eo, cascade.KindInternal, "freshly minted")
	e = newFuEnv(t, nil)
	e.st.arm(true, "cursor")
	e.refused(nil, cascade.KindUnavailable, "writing the fan-out cursor")
	e.st.arm(false, "cursor")
	e.st.arm(true, "request")
	_, eo = e.call(nil)
	e.check(eo != nil && e.p.calls.Load() == 0 && e.final() == resume.OutcomeTerminal, "request failure: %+v, calls %d, marker %q", eo, e.p.calls.Load(), e.final())
}

func TestFanOutUnit_ReattachRefusesMismatchThenReplays(t *testing.T) {
	e := newFuEnv(t, nil)
	e.strand("final", "delete")
	_, eo := e.call(map[string]any{"inputs": []map[string]string{{"role": "user", "content": "other"}}})
	e.want(eo, cascade.KindConflict, "differs from the fan-out's stored request")
	e.check(e.final() == "" && !e.gone() && e.p.calls.Load() == 2, "a mismatched re-attach touched the fan-out: marker %q", e.final())
	got, eo := e.call(nil)
	e.check(eo == nil && len(got.Legs) == 2 && e.p.calls.Load() == 2 && e.final() == resume.OutcomeDelivered && e.gone(),
		"re-attach = (%+v, %+v), calls %d, marker %q; want replayed legs, no new call, delivered", got, eo, e.p.calls.Load(), e.final())
}

func TestFanOutUnit_ReattachNeverRedispatchesTerminalOrExhaustedLegs(t *testing.T) {
	e := newFuEnv(t, nil)
	e.p.err = errors.New("provider down")
	for i := 0; i < resume.MaxLegStarts; i++ {
		e.strand("final", "delete")
	}
	e.refusedReattach(cascade.KindConflict, "start cap", resume.OutcomeUnknown)
	e = newFuEnv(t, nil)
	e.p.err = cascade.New(cascade.KindPolicyDenied, "refused")
	e.strand("final", "delete")
	e.refusedReattach(cascade.KindPolicyDenied, "refused terminally", resume.OutcomeTerminal)
	e.refusedReattach(cascade.KindConflict, "already finalized: terminal", resume.OutcomeTerminal)
	e = newFuEnv(t, nil)
	e.strand("final") // the deletes ran: no request record is left
	_, eo := e.call(nil)
	e.check(eo != nil && e.p.calls.Load() == 2 && e.final() == resume.OutcomeTerminal, "missing record: %+v, calls %d, marker %q", eo, e.p.calls.Load(), e.final())
}

func TestFanOutUnit_OutcomeTable(t *testing.T) {
	live := context.Background()
	expired, stop := context.WithTimeout(live, -1)
	defer stop()
	twin, plain := cascade.New(cascade.KindConflict, resume.ErrLegAttemptsExhausted.Error()), errors.New("x")
	for _, c := range []struct {
		ctx  context.Context
		err  error
		want string
	}{
		{live, nil, resume.OutcomeDelivered}, {live, cascade.New(cascade.KindPolicyDenied, "x"), resume.OutcomeTerminal},
		{live, cascade.New(cascade.KindInvalidInput, "x"), resume.OutcomeTerminal}, {live, plain, resume.OutcomeFailed},
		{live, errors.Join(plain, resume.ErrLegAttemptsExhausted), resume.OutcomeUnknown}, {live, twin, resume.OutcomeFailed},
		{expired, plain, resume.OutcomeUnknown},
	} {
		if got := fanOutOutcome(c.ctx, c.err); got != c.want {
			t.Errorf("outcome(%v, %v) = %q, want %q", c.ctx.Err(), c.err, got, c.want)
		}
	}
	if holds(nil, plain) || !holds(cascade.Wrap(cascade.KindInternal, plain, "w"), plain) || holds(cascade.Wrap(cascade.KindInternal, twin, "w"), resume.ErrLegAttemptsExhausted) {
		t.Error("holds must see a target through Unwrap, miss on nil and miss on a same-text lookalike")
	}
}
