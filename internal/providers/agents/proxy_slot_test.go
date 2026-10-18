package agents

// Purpose: the journal's per-destination dial slot is freed when the effect
// writer panics, so the next dial to the same destination is not stranded
// behind a claim nothing will ever release.
//
// Constraints: in-memory effect writer only; no socket.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/providers/agents/egressproxy"
)

// panicOnce is an effect writer whose first BeginEffect panics.
type panicOnce struct {
	audit.EffectWriter
	armed bool
}

func (p *panicOnce) BeginEffect(ctx context.Context, r audit.EffectRequest) (audit.EffectHandle, error) {
	if p.armed {
		p.armed = false
		panic("effect writer fault")
	}
	return p.EffectWriter.BeginEffect(ctx, r)
}

func TestProxyJournalPanicInBeginFreesTheSlot(t *testing.T) {
	w := &panicOnce{EffectWriter: &recEffects{}, armed: true}
	rec := ProxyJournal(&recSink{}, w)
	d := row(egressproxy.PhaseIntent, "api.example.test:443", egressproxy.ReasonAllowed)
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the effect writer fault must reach the caller that recovers it")
			}
		}()
		_ = rec(context.Background(), d)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rec(ctx, d); err != nil {
		t.Fatalf("intent after a recovered panic = %v, want the slot free", err)
	}
}

// partialIntent returns a committed handle together with the error, as the
// audit effect log does when only its notification failed.
type partialIntent struct{ recEffects }

func (p *partialIntent) BeginEffect(ctx context.Context, r audit.EffectRequest) (audit.EffectHandle, error) {
	h, _ := p.recEffects.BeginEffect(ctx, r)
	return h, errors.New("notify failed")
}

func TestProxyJournalFailsAnIntentThatCommittedWithAnError(t *testing.T) {
	w := &partialIntent{}
	rec := ProxyJournal(&recSink{}, w)
	d := row(egressproxy.PhaseIntent, "api.example.test:443", egressproxy.ReasonAllowed)
	if err := rec(context.Background(), d); err == nil {
		t.Fatal("intent = nil, want the begin error returned so the proxy refuses")
	}
	if len(w.calls) != 2 || w.calls[0].op != "begin" || w.calls[1].op != "fail" ||
		w.calls[1].text != string(egressproxy.ReasonJournalUnavailable) {
		t.Fatalf("effect calls = %+v, want begin then fail journal-unavailable", w.calls)
	}
	// The same journal takes the next dial to the destination: a leaked slot
	// would block here until the context ends instead of reaching the writer.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rec(ctx, d); err == nil || ctx.Err() != nil {
		t.Fatalf("second intent = %v (ctx %v), want the writer's error, not a wait on the slot", err, ctx.Err())
	}
}

// ctxEffects fails its terminal writes when their context is already done.
type ctxEffects struct{ recEffects }

func (c *ctxEffects) ConfirmEffect(ctx context.Context, h audit.EffectHandle, outcome string) (audit.Record, error) {
	if err := ctx.Err(); err != nil {
		return audit.Record{}, err
	}
	return c.recEffects.ConfirmEffect(ctx, h, outcome)
}

func TestProxyJournalConfirmSurvivesTheProxyClosing(t *testing.T) {
	w := &ctxEffects{}
	rec := ProxyJournal(&recSink{}, w)
	d := row(egressproxy.PhaseIntent, "api.example.test:443", egressproxy.ReasonAllowed)
	if err := rec(context.Background(), d); err != nil {
		t.Fatalf("intent: %v", err)
	}
	closed, cancel := context.WithCancel(context.Background())
	cancel() // the proxy closed between the dial and its confirm row
	if err := rec(closed, row(egressproxy.PhaseConfirm, "api.example.test:443", egressproxy.ReasonConnected)); err != nil {
		t.Fatalf("confirm after the proxy context ended = %v, want the effect settled", err)
	}
	if n := len(w.calls); n != 2 || w.calls[1].op != "confirm" {
		t.Fatalf("effect calls = %+v, want begin then confirm", w.calls)
	}
}

// blockingEffects blocks its terminal write until the context ends.
type blockingEffects struct{ recEffects }

func (b *blockingEffects) ConfirmEffect(ctx context.Context, _ audit.EffectHandle, _ string) (audit.Record, error) {
	<-ctx.Done()
	return audit.Record{}, ctx.Err()
}

func TestProxyJournalTerminalWriteIsBounded(t *testing.T) {
	old := settleTimeout
	settleTimeout = 50 * time.Millisecond
	t.Cleanup(func() { settleTimeout = old })
	rec := ProxyJournal(&recSink{}, &blockingEffects{})
	dest := "api.example.test:443"
	if err := rec(context.Background(), row(egressproxy.PhaseIntent, dest, egressproxy.ReasonAllowed)); err != nil {
		t.Fatalf("intent: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- rec(context.Background(), row(egressproxy.PhaseConfirm, dest, egressproxy.ReasonConnected))
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a confirm that never settled returned nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a stalled effect writer held the confirm open past the settle bound")
	}
	if err := rec(context.Background(), row(egressproxy.PhaseIntent, dest, egressproxy.ReasonAllowed)); err != nil {
		t.Fatalf("the slot stayed held after a timed-out confirm: %v", err)
	}
}

// blockingSink blocks its append until the context ends.
type blockingSink struct{}

func (blockingSink) Append(ctx context.Context, _ audit.Event) (audit.Record, error) {
	<-ctx.Done()
	return audit.Record{}, ctx.Err()
}

func TestProxyJournalDecisionWriteIsBounded(t *testing.T) {
	old := settleTimeout
	settleTimeout = 50 * time.Millisecond
	t.Cleanup(func() { settleTimeout = old })
	rec := ProxyJournal(blockingSink{}, &recEffects{})
	done := make(chan error, 1)
	go func() {
		done <- rec(context.WithoutCancel(context.Background()),
			row(egressproxy.PhaseDecide, "evil.example.test:443", egressproxy.ReasonDestinationNotAllowed))
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("an append that never settled returned nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a stalled audit sink held a decision row open past the settle bound")
	}
}
