package conductor

// Purpose (this file): fan-out legs metered by the Executor's CallReserver.
//   Each leg reserves its own row with the parent's ReservationID cleared,
//   the parent holds no permit while legs run, and every adapter call is
//   admitted exactly once by the governor.

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

// parentReservationID is the reservation the fan-out parent request names.
const parentReservationID = "parent-res"

// countingGovernor counts Admit calls and the permits it currently holds.
type countingGovernor struct {
	mu     sync.Mutex
	admits int
	active int
}

func (g *countingGovernor) admit() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.admits++
	g.active++
}

func (g *countingGovernor) done() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.active--
}

func (g *countingGovernor) counts() (admits, active int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.admits, g.active
}

// legLedger is a CallReserver writing one row per Reserve; every permit a
// hold takes is one Admit on its governor.
type legLedger struct {
	gov      countingGovernor
	mu       sync.Mutex
	rows     []string
	reqIDs   []string
	checks   []string
	released int
}

func (l *legLedger) Reserve(_ context.Context, _ provider.Selection, req provider.ModelRequest) (HeldCall, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	id := fmt.Sprintf("row-%d", len(l.rows)+1)
	l.rows = append(l.rows, id)
	l.reqIDs = append(l.reqIDs, req.ReservationID)
	return &ledgerHold{l: l, id: id}, nil
}

func (l *legLedger) Check(_ context.Context, id string, _ string, _ provider.Selection) (HeldCall, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.checks = append(l.checks, id)
	return &ledgerHold{l: l, id: id}, nil
}

func (l *legLedger) snapshot() (rows, reqIDs, checks []string, released int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.rows...), append([]string(nil), l.reqIDs...),
		append([]string(nil), l.checks...), l.released
}

type ledgerHold struct {
	l  *legLedger
	id string
}

func (h *ledgerHold) ID() string { return h.id }

func (h *ledgerHold) WithPermit(ctx context.Context, fn func(context.Context) error) error {
	h.l.gov.admit()
	defer h.l.gov.done()
	return fn(ctx)
}

func (h *ledgerHold) Release(context.Context, provider.Usage, bool) error {
	h.l.mu.Lock()
	defer h.l.mu.Unlock()
	h.l.released++
	return nil
}

// legRig is an Executor over a legLedger whose adapter counts calls and,
// when gate is set, parks every call until gate.release is closed.
type legRig struct {
	exec    *Executor
	ledger  *legLedger
	mu      sync.Mutex
	calls   int
	arrived chan struct{}
	release chan struct{}
}

func newLegRig(t *testing.T, gated bool) *legRig {
	t.Helper()
	cfg, deps := newReadyConfig(t)
	cfg.Router = concurrencySafeRouter{}
	r := &legRig{ledger: &legLedger{}, arrived: make(chan struct{}, 16), release: make(chan struct{})}
	cfg.Reserver = r.ledger
	if !gated {
		close(r.release)
	}
	deps.prov.chatFn = func(ctx context.Context, _ provider.ChatRequest) (provider.ChatResponse, error) {
		r.mu.Lock()
		r.calls++
		r.mu.Unlock()
		r.arrived <- struct{}{}
		select {
		case <-r.release:
		case <-ctx.Done():
			return provider.ChatResponse{}, ctx.Err()
		}
		return provider.ChatResponse{Message: provider.ChatMessage{Role: "assistant", Content: "ok"}}, nil
	}
	exec, err := NewExecutor(cfg)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	r.exec = exec
	return r
}

func (r *legRig) adapterCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// parentReq is a fan-out request naming the parent's reservation.
func parentReq() provider.ModelRequest {
	req := validReq()
	req.ReservationID = parentReservationID
	return req
}

// fanOut3 runs a three-leg fan-out of parentReq under the Executor's
// LegPermit through ExecuteFanOutResponse.
func (r *legRig) fanOut3(ctx context.Context) (provider.ModelResponse, error) {
	return r.exec.ExecuteFanOutResponse(ctx, "fo-legs", parentReq(), 3, nil, r.exec.LegPermit(), &spyJournal{}, newMemLegStore())
}

func TestFanOutLegReservation(t *testing.T) {
	r := newLegRig(t, false)
	resp, err := r.fanOut3(context.Background())
	if err != nil {
		t.Fatalf("fan-out: %v", err)
	}
	if len(resp.Legs) != 3 {
		t.Fatalf("legs = %d, want 3", len(resp.Legs))
	}
	rows, reqIDs, checks, released := r.ledger.snapshot()
	if len(rows) != 3 || released != 3 {
		t.Fatalf("reservation rows = %v released %d, want 3 rows each released once", rows, released)
	}
	seen := map[string]bool{}
	for i, id := range rows {
		if id == parentReservationID || seen[id] {
			t.Errorf("row %d = %q: a leg reused the parent's or another leg's row", i, id)
		}
		seen[id] = true
		if reqIDs[i] != "" {
			t.Errorf("leg %d reserved carrying reservation id %q, want it cleared", i, reqIDs[i])
		}
	}
	if len(checks) != 0 {
		t.Errorf("checked reservations = %v, want none (no leg carries the parent id)", checks)
	}
}

func TestFanOutParentHoldsNoPermit(t *testing.T) {
	r := newLegRig(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := r.fanOut3(ctx)
		done <- err
	}()
	for i := 0; i < 3; i++ {
		select {
		case <-r.arrived:
		case <-ctx.Done():
			t.Fatalf("only %d of 3 legs reached the adapter: %v", i, ctx.Err())
		}
	}
	// Every leg is inside its adapter call: the governor holds exactly one
	// permit per leg, so the parent holds none.
	_, active := r.ledger.gov.counts()
	rows, _, checks, _ := r.ledger.snapshot()
	close(r.release)
	if err := <-done; err != nil {
		t.Fatalf("fan-out: %v", err)
	}
	if active != 3 || len(rows) != 3 || len(checks) != 0 {
		t.Fatalf("while legs ran: permits %d rows %d checks %v, want 3 permits on 3 leg rows and no parent permit", active, len(rows), checks)
	}
	if _, after := r.ledger.gov.counts(); after != 0 {
		t.Fatalf("permits held after the fan-out = %d, want 0", after)
	}
}

func TestOneAdmitPerAdapterCall(t *testing.T) {
	execRig := newLegRig(t, false)
	if _, err := execRig.exec.Execute(context.Background(), validReq()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	fanRig := newLegRig(t, false)
	if _, err := fanRig.fanOut3(context.Background()); err != nil {
		t.Fatalf("fan-out: %v", err)
	}
	for name, r := range map[string]*legRig{"execute": execRig, "fan-out": fanRig} {
		admits, active := r.ledger.gov.counts()
		calls := r.adapterCalls()
		if calls == 0 || admits != calls || active != 0 {
			t.Errorf("%s: admits %d, adapter calls %d, permits still held %d; want one admit per adapter call", name, admits, calls, active)
		}
	}
}
