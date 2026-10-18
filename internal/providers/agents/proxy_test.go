package agents

// Purpose: the spawn proxy's untagged lane: the allow list the config
// produces, the journal's audit binding and its effect ordering, and the
// proxy lifecycle. No test here imports net: the real-socket legs are in
// proxy_net_test.go (integration tag).
//
// Constraints: the journal is driven row by row with fakes that record the
// stored call, never an event; sentinel errors are asserted by identity and
// message.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/providers/agents/egressproxy"
	"github.com/acamarata/cascade/pkg/cascade"
)

var _ AuditSink = (*audit.Log)(nil)

// recSink records every appended event; err, when set, is returned for each.
type recSink struct {
	mu     sync.Mutex
	events []audit.Event
	err    error
}

func (s *recSink) Append(_ context.Context, e audit.Event) (audit.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return audit.Record{}, s.err
	}
	s.events = append(s.events, e)
	return audit.Record{}, nil
}

// effectCall is one recorded EffectWriter call.
type effectCall struct{ op, key, text string }

// recEffects records the effect calls in order; existing keys make
// BeginEffect return ErrEffectExists, beginErr fails it outright.
type recEffects struct {
	mu       sync.Mutex
	calls    []effectCall
	existing map[string]bool
	beginErr error
	endErr   error
	reqs     []audit.EffectRequest
}

func (e *recEffects) BeginEffect(_ context.Context, r audit.EffectRequest) (audit.EffectHandle, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.beginErr != nil {
		return audit.EffectHandle{}, e.beginErr
	}
	if e.existing[r.Key] {
		return audit.EffectHandle{}, audit.ErrEffectExists
	}
	e.reqs = append(e.reqs, r)
	e.calls = append(e.calls, effectCall{"begin", r.Key, ""})
	return audit.EffectHandle{Key: r.Key, Kind: r.Kind, IntentSeq: uint64(len(e.calls))}, nil
}

func (e *recEffects) ConfirmEffect(_ context.Context, h audit.EffectHandle, outcome string) (audit.Record, error) {
	return e.end("confirm", h, outcome)
}

func (e *recEffects) FailEffect(_ context.Context, h audit.EffectHandle, reason string) (audit.Record, error) {
	return e.end("fail", h, reason)
}

func (e *recEffects) end(op string, h audit.EffectHandle, text string) (audit.Record, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.endErr != nil {
		return audit.Record{}, e.endErr
	}
	e.calls = append(e.calls, effectCall{op, h.Key, text})
	return audit.Record{}, nil
}

// requireSentinel checks err carries want itself (pointer identity) and its
// message; errors.Is would match any error of the same Kind.
func requireSentinel(t *testing.T, err error, want *cascade.Error) {
	t.Helper()
	for e := err; e != nil; e = errors.Unwrap(e) {
		if e == error(want) {
			if !strings.Contains(err.Error(), want.Msg) {
				t.Fatalf("error %q carries the sentinel but not its message %q", err, want.Msg)
			}
			return
		}
	}
	t.Fatalf("error %v does not carry the sentinel %q", err, want.Msg)
}

func row(phase egressproxy.Phase, dest string, reason egressproxy.Reason) egressproxy.Decision {
	allowed := reason == egressproxy.ReasonAllowed || reason == egressproxy.ReasonConnected || reason == egressproxy.ReasonClosed
	return egressproxy.Decision{DriverID: egressproxy.DriverCodex, JobID: "job-1",
		Destination: egressproxy.Destination(dest), Phase: phase, Allowed: allowed, Reason: reason}
}

func TestSpawnProxyAllowlistFromConfig(t *testing.T) {
	extra := map[string]any{"agents": map[string]any{"egress": map[string]any{
		"codex": map[string]any{"allow": []any{"api.example.test:443", "auth.example.test:443"}},
	}}}
	lists, err := egressproxy.ParseEgressAllowlists(extra)
	if err != nil {
		t.Fatalf("ParseEgressAllowlists: %v", err)
	}
	want := egressproxy.DestinationAllowlist{"api.example.test:443", "auth.example.test:443"}
	if got := lists[egressproxy.DriverCodex]; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("codex allow = %v, want %v", got, want)
	}
	for _, id := range []egressproxy.DriverID{egressproxy.DriverClaude, egressproxy.DriverOpenCode, egressproxy.DriverAntigravity} {
		if got, ok := lists[id]; !ok || len(got) != 0 {
			t.Fatalf("driver %s without a table = %v (present %v), want an empty list that denies all", id, got, ok)
		}
	}
	// The parsed list is what the spawn proxy starts over; an absent table's
	// empty list starts a proxy that holds no entry to admit.
	j := ProxyJournal(&recSink{}, &recEffects{})
	for _, id := range []egressproxy.DriverID{egressproxy.DriverCodex, egressproxy.DriverClaude} {
		p, err := StartSpawnProxy(context.Background(), id, "job-1", lists[id], j)
		if err != nil {
			t.Fatalf("StartSpawnProxy(%s): %v", id, err)
		}
		_ = p.Close()
	}
}

func TestSpawnProxyClosedAtRelease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	p, err := StartSpawnProxy(ctx, egressproxy.DriverCodex, "job-1", nil, ProxyJournal(&recSink{}, &recEffects{}))
	if err != nil {
		t.Fatalf("StartSpawnProxy: %v", err)
	}
	if env := p.Env(); len(env) != 6 || !strings.HasPrefix(env[0], "HTTPS_PROXY=http://cascade:") {
		t.Fatalf("Env = %v, want the six proxy variables", env)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("release (Close): %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("a second release must be a no-op, got %v", err)
	}
	// The socket-level half (a CONNECT to the old port is refused) is
	// TestSpawnProxyClosedAtReleaseRefusesConnect in proxy_net_test.go.
}

func TestSpawnProxyJournalBindsAuditEvent(t *testing.T) {
	sink, eff := &recSink{}, &recEffects{}
	j := ProxyJournal(sink, eff)
	rows := []egressproxy.Decision{
		row(egressproxy.PhaseDecide, "evil.example.test:443", egressproxy.ReasonDestinationNotAllowed),
		row(egressproxy.PhaseDecide, "", egressproxy.ReasonMalformedDestination),
		row(egressproxy.PhaseConfirm, "api.example.test:443", egressproxy.ReasonClosed),
	}
	for _, r := range rows {
		if err := j(context.Background(), r); err != nil {
			t.Fatalf("journal(%+v): %v", r, err)
		}
	}
	if len(sink.events) != len(rows) || len(eff.calls) != 0 {
		t.Fatalf("events = %d, effect calls = %d; want one event per row and no effect call", len(sink.events), len(eff.calls))
	}
	wantVerdict := []string{"deny", "deny", "allow"}
	for i, e := range sink.events {
		if e.Kind != audit.KindPolicyDecide || e.Actor != "agent-driver:codex" || e.Action != "agent.egress.connect" ||
			e.Verdict != wantVerdict[i] || e.Outcome != string(rows[i].Reason) {
			t.Fatalf("event %d = %+v, want the SEC-13 binding for %+v", i, e, rows[i])
		}
		var got map[string]string
		if err := json.Unmarshal(e.Explain, &got); err != nil {
			t.Fatalf("event %d explain %s: %v", i, e.Explain, err)
		}
		want := map[string]string{"destination": string(rows[i].Destination), "job_id": "job-1", "phase": string(rows[i].Phase)}
		if len(got) != len(want) || got["destination"] != want["destination"] || got["job_id"] != "job-1" || got["phase"] != want["phase"] {
			t.Fatalf("event %d explain = %v, want exactly %v", i, got, want)
		}
	}
	// A sink error is the journal's error, so the proxy refuses.
	boom := cascade.New(cascade.KindUnavailable, "sink down")
	sink.err = boom
	if err := j(context.Background(), rows[0]); err != error(boom) {
		t.Fatalf("a sink error must come back unchanged, got %v", err)
	}
}

func TestSpawnProxyJournalWithoutSinkRefuses(t *testing.T) {
	for name, j := range map[string]egressproxy.Journal{
		"nil sink":    ProxyJournal(nil, &recEffects{}),
		"nil effects": ProxyJournal(&recSink{}, nil),
	} {
		err := j(context.Background(), row(egressproxy.PhaseIntent, "api.example.test:443", egressproxy.ReasonAllowed))
		if !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Fatalf("%s: err = %v, want KindUnavailable (the proxy then refuses)", name, err)
		}
	}
}

func TestProxyJournalEffectOrdering(t *testing.T) {
	eff := &recEffects{existing: map[string]bool{"agent.egress:job-1:1": true}}
	j := ProxyJournal(&recSink{}, eff)
	ctx := context.Background()
	steps := []egressproxy.Decision{
		row(egressproxy.PhaseIntent, "a.example.test:443", egressproxy.ReasonAllowed),
		row(egressproxy.PhaseConfirm, "a.example.test:443", egressproxy.ReasonConnected),
		row(egressproxy.PhaseIntent, "b.example.test:443", egressproxy.ReasonAllowed),
		row(egressproxy.PhaseConfirm, "b.example.test:443", egressproxy.ReasonResolvedAddressForbidden),
	}
	for _, s := range steps {
		if err := j(ctx, s); err != nil {
			t.Fatalf("journal(%+v): %v", s, err)
		}
	}
	want := []effectCall{
		{"begin", "agent.egress:job-1:2", ""}, {"confirm", "agent.egress:job-1:2", "connected"},
		{"begin", "agent.egress:job-1:3", ""}, {"fail", "agent.egress:job-1:3", "resolved-address-forbidden"},
	}
	if len(eff.calls) != len(want) {
		t.Fatalf("effect calls = %v, want %v (key 1 exists, so the first intent takes key 2)", eff.calls, want)
	}
	for i := range want {
		if eff.calls[i] != want[i] {
			t.Fatalf("call %d = %v, want %v", i, eff.calls[i], want[i])
		}
	}
	if r := eff.reqs[0]; r.Kind != audit.KindExternalEffect || r.Actor != "agent-driver:codex" || r.Action != "agent.egress.connect" ||
		r.ParamsHash != audit.HashParams([]byte("a.example.test:443")) {
		t.Fatalf("intent request = %+v, want the egress binding", r)
	}
}

func TestProxyJournalIntentFailureRefusesAndFreesTheSlot(t *testing.T) {
	boom := cascade.New(cascade.KindUnavailable, "log down")
	eff := &recEffects{beginErr: boom}
	j := ProxyJournal(&recSink{}, eff)
	intent := row(egressproxy.PhaseIntent, "a.example.test:443", egressproxy.ReasonAllowed)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i := 0; i < 2; i++ { // a held slot would make the second call block until ctx expires
		if err := j(ctx, intent); err != error(boom) {
			t.Fatalf("attempt %d: err = %v, want the writer's error unchanged", i, err)
		}
	}
	if len(eff.calls) != 0 {
		t.Fatalf("effect calls after a failed intent = %v, want none", eff.calls)
	}
}

func TestProxyJournalConfirmNeedsItsIntent(t *testing.T) {
	eff := &recEffects{}
	j := ProxyJournal(&recSink{}, eff)
	err := j(context.Background(), row(egressproxy.PhaseConfirm, "a.example.test:443", egressproxy.ReasonConnected))
	if !cascade.HasKind(err, cascade.KindInternal) || len(eff.calls) != 0 {
		t.Fatalf("confirm with no intent: err = %v, calls = %v; want KindInternal and no write", err, eff.calls)
	}
}

func TestProxyJournalOneDialPerDestinationAtATime(t *testing.T) {
	eff := &recEffects{}
	j := ProxyJournal(&recSink{}, eff)
	dest := "a.example.test:443"
	if err := j(context.Background(), row(egressproxy.PhaseIntent, dest, egressproxy.ReasonAllowed)); err != nil {
		t.Fatalf("first intent: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := j(ctx, row(egressproxy.PhaseIntent, dest, egressproxy.ReasonAllowed))
	if !cascade.HasKind(err, cascade.KindCanceled) {
		t.Fatalf("second intent while the first is open: err = %v, want KindCanceled", err)
	}
	if err := j(context.Background(), row(egressproxy.PhaseConfirm, dest, egressproxy.ReasonConnected)); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if err := j(context.Background(), row(egressproxy.PhaseIntent, dest, egressproxy.ReasonAllowed)); err != nil {
		t.Fatalf("intent after the terminal row: %v", err)
	}
}
