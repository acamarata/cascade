//go:build !windows

// Purpose: the stall ladder end to end over the real bus, stores, policy
//
//	wiring and approval queue: a session registered through the hook-event
//	path from the captured SessionStart fixture goes quiet, and every rung
//	lands where the contract says, one RungDelay apart, each directive in
//	the next context.slice for that session.
//
// Constraints: the stall rig of daemon_unix_stall_test.go (mutex clock,
//
//	test-driven ticker, HOME in t.TempDir()); the captured fixture's
//	provenance is internal/fleet/hookpacks/testdata/README.md; no network.
//
// SPORT: cmd/cascade daemon stall supervision (tests, P1-SUP-04).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/hookpacks"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
)

// registerFixtureSession posts the captured SessionStart through the
// hook-event handler exactly as `cascade fleet sessions hook-event` builds it.
func registerFixtureSession(t *testing.T, r *stallRig) string {
	t.Helper()
	if err := hookpacks.RegisterHookEventHandler(r.w.Registry, sessions.New(r.w.Store, r.clock, r.w.Bus), r.w.Bus, r.clock); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "fleet", "hookpacks", "testdata", "cc-hook-fixtures", "sessionstart.json"))
	if err != nil {
		t.Fatal(err)
	}
	in, ok := readSessionHookInput(bytes.NewReader(raw))
	if !ok {
		t.Fatal("the captured SessionStart did not decode")
	}
	payload := hookpacks.HookPayload{Harness: sessionHookHarness, EventType: hookpacks.EventSessionStart,
		SessionID: in.SessionID, PID: 4242, TimestampMs: r.clock.Now().UnixMilli()}
	params, _ := json.Marshal(payload)
	if _, errObj := r.w.Registry.Dispatch(context.Background(), &rpc.Request{JSONRPC: "2.0", Method: hookpacks.MethodHookEvent, ID: []byte("1"), Params: params}); errObj != nil {
		t.Fatalf("hook_event SessionStart: %+v", errObj)
	}
	return in.SessionID
}

// sliceDirectives dispatches context.slice for id as the owner CLI.
func sliceDirectives(t *testing.T, r *stallRig, id string) []string {
	t.Helper()
	cwd, _ := os.Getwd()
	params, _ := json.Marshal(daemon.ContextAssembleParams{Cwd: cwd, SessionID: id})
	ctx := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	res, errObj := r.w.Registry.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", Method: daemon.ContextSliceMethod, ID: []byte("2"), Params: params})
	if errObj != nil {
		t.Fatalf("context.slice: %+v", errObj)
	}
	return res.(daemon.ContextSliceResult).Directives
}

// tickUntil drives poll ticks at the current instant until done holds.
func tickUntil(t *testing.T, r *stallRig, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		r.ticker.waitIdle(t)
		select {
		case r.ticker.ticks <- struct{}{}:
		case <-time.After(10 * time.Second):
			t.Fatalf("the poll loop stopped taking ticks before: %s", what)
		}
		r.ticker.waitIdle(t)
		select {
		case r.ticker.idle <- struct{}{}: // the loop is parked in its select again
		default:
		}
		if done() {
			return
		}
	}
	t.Fatalf("never reached: %s", what)
}

// rungState is what the ladder has produced so far.
type rungState struct {
	directives []string
	stalls     int
	approvals  int
}

// ladderRig drives and observes one stalled session's ladder.
type ladderRig struct {
	t         *testing.T
	r         *stallRig
	id        string
	attention *supervision.Store
	seen      rungState
}

// observe drains the session's directives and counts items and approvals.
func (l *ladderRig) observe() rungState {
	l.seen.directives = append(l.seen.directives, sliceDirectives(l.t, l.r, l.id)...)
	items, _ := l.attention.ListInScopes(context.Background(), []supervision.ScopeRef{{Kind: scope.ScopeKindSession, ID: l.id}}, supervision.Filter{})
	l.seen.stalls = len(items)
	pending, _ := l.r.w.Deps.Policy.Queue.GetPending(context.Background())
	l.seen.approvals = len(pending)
	return l.seen
}

// quiet ticks a few times and fails if anything moved since before.
func (l *ladderRig) quiet(what string, before rungState) {
	l.t.Helper()
	for i := 0; i < 3; i++ {
		tickUntil(l.t, l.r, "a tick", func() bool { return true })
	}
	if got := l.observe(); len(got.directives) != len(before.directives) || got.stalls != before.stalls || got.approvals != before.approvals {
		l.t.Fatalf("%s: before %+v, after %+v", what, before, got)
	}
}

// step holds one minute short of RungDelay (nothing may fire), then crosses
// it and ticks until done holds.
func (l *ladderRig) step(what string, done func(rungState) bool) {
	l.t.Helper()
	before := l.observe()
	l.r.clock.advance(stallRungDelay - time.Minute)
	l.quiet("a rung fired before RungDelay elapsed ("+what+")", before)
	l.r.clock.advance(time.Minute)
	tickUntil(l.t, l.r, what, func() bool { return done(l.observe()) })
}

func TestStallEndToEndPublishesAndFilesApproval(t *testing.T) {
	r := newStallRig(t, nil, map[string]interface{}{"fleet": map[string]interface{}{"supervision": map[string]interface{}{"stall_threshold": "1m"}}})
	if err := wireStallDetectorWith(r.w, r.rt(r.w.Bus)); err != nil {
		t.Fatalf("Wire: %v", err)
	}
	l := &ladderRig{t: t, r: r, id: registerFixtureSession(t, r),
		attention: supervision.NewStore(r.w.Store, r.clock, nil, supervision.NewSystemIDGenerator(), 0)}
	r.clock.advance(time.Minute + time.Second)
	tickUntil(t, r, "first retry directive", func() bool { return len(l.observe().directives) == 1 })
	l.step("second retry directive", func(s rungState) bool { return len(s.directives) == 2 })
	l.step("context directive", func(s rungState) bool { return len(s.directives) == 3 })
	if l.seen.stalls != 0 || l.seen.approvals != 0 {
		t.Fatalf("supervisor or human rung fired early: %+v", l.seen)
	}
	l.step("supervisor attention item", func(s rungState) bool { return s.stalls == 1 })
	if l.seen.approvals != 0 {
		t.Fatal("the human rung fired with the supervisor rung")
	}
	l.step("L2 approval", func(s rungState) bool { return s.approvals == 1 })
	// The queue's approval lives 5 minutes (policy.MaxApprovalTTL): still
	// exactly one inside it, and after 2x RungDelay it has lapsed with no
	// second request filed behind it.
	r.clock.advance(4 * time.Minute)
	l.quiet("the ladder moved inside the approval's lifetime", l.seen)
	assertLadderOutcome(t, l)
	r.clock.advance(2 * stallRungDelay)
	l.quiet("a second approval or directive followed the human rung", rungState{directives: l.seen.directives, stalls: 1, approvals: 0})
}

// assertLadderOutcome checks the directive texts, the filed approval and the
// single stored stall event.
func assertLadderOutcome(t *testing.T, l *ladderRig) {
	t.Helper()
	r, d := l.r, l.seen.directives
	if !strings.HasPrefix(d[0], "Cascade supervision: this session has made no progress") || d[1] != d[0] ||
		d[2] != "Cascade supervision: extra context budget granted for this turn." {
		t.Fatalf("directive sequence = %q, want retry, retry, context", d)
	}
	pending, _ := r.w.Deps.Policy.Queue.GetPending(context.Background())
	if want := stallSummary(l.id, supervision.StallEvent{StallKind: "idle", StalledSince: stallNow.UnixMilli()}); !strings.HasPrefix(pending[0].Summary, want) {
		t.Errorf("approval summary = %q", pending[0].Summary)
	}
	if pending[0].ActionClass != policy.ClassLocalDev {
		t.Errorf("approval ActionClass = %v, want %v (the capability's class)", pending[0].ActionClass, policy.ClassLocalDev)
	}
	log, err := r.w.Bus.Replay(context.Background(), "supervision", 0)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, ev := range log {
		if ev.Kind == "supervision.stalled" {
			kinds = append(kinds, string(ev.Kind))
		}
	}
	if !reflect.DeepEqual(kinds, []string{"supervision.stalled"}) {
		t.Fatalf("stored supervision.stalled events = %d, want exactly one", len(kinds))
	}
}

// registrationNamed returns the production daemon registration called name.
func registrationNamed(t *testing.T, name string) daemonRegistration {
	t.Helper()
	for _, reg := range daemonRegistrations {
		if reg.Name == name {
			return reg
		}
	}
	t.Fatalf("no %s daemon registration", name)
	return daemonRegistration{}
}

// The production context-engine registration binds context.slice without a
// drain; the stall registration (a later phase) overwrites it. Run both in
// phase order on one registry and prove the drain is what remains.
func TestStallDrainSurvivesContextEngineBinding(t *testing.T) {
	r := newStallRig(t, nil, nil)
	ce := registrationNamed(t, "context-engine")
	if ce.Phase >= phaseSupervised || ce.Wire == nil {
		t.Fatalf("context-engine registration = %+v, want a phase below phaseSupervised", ce)
	}
	if err := ce.Wire(r.w); err != nil {
		t.Fatalf("context-engine Wire: %v", err)
	}
	if err := wireStallDetectorWith(r.w, r.rt(r.w.Bus)); err != nil {
		t.Fatalf("stall Wire: %v", err)
	}
	ctx := context.Background()
	if err := sessions.New(r.w.Store, r.clock, r.w.Bus).Upsert(ctx, sessions.SessionRecord{SessionID: "live-1", State: "active"}); err != nil {
		t.Fatal(err)
	}
	d := supervision.Directive{SessionID: "live-1", Kind: supervision.DirectiveRetry, Text: "retry now", StalledSince: 1}
	if err := supervision.NewDirectiveStore(r.w.Store, r.clock).Enqueue(ctx, d); err != nil {
		t.Fatal(err)
	}
	if got := sliceDirectives(t, r, "live-1"); len(got) != 1 || !strings.Contains(got[0], "retry now") {
		t.Fatalf("context.slice directives = %q, want the queued retry (the drain was lost to the context-engine binding)", got)
	}
}
